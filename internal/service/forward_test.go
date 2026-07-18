package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/model"
	storesqlite "telegram-interactive-bot/go-bot/internal/store/sqlite"
)

func TestDirectContactURL(t *testing.T) {
	tests := []struct {
		name string
		user *models.User
		want string
	}{
		{name: "username", user: &models.User{ID: 42, Username: "support_user"}, want: "https://t.me/support_user"},
		{name: "telegram id", user: &models.User{ID: 42}, want: "tg://user?id=42"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := directContactURL(test.user); got != test.want {
				t.Fatalf("directContactURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatEditedContent(t *testing.T) {
	got := formatEditedContent("我好", "你好", 4096)
	want := "我好(修改前：你好)"
	if got != want {
		t.Fatalf("formatEditedContent() = %q, want %q", got, want)
	}
}

func TestFormatEditedContentUsesLatestPreviousVersion(t *testing.T) {
	firstEdit := formatEditedContent("我好", "你好", 4096)
	if firstEdit != "我好(修改前：你好)" {
		t.Fatalf("first edit = %q", firstEdit)
	}
	secondEdit := formatEditedContent("大家好", "我好", 4096)
	if secondEdit != "大家好(修改前：我好)" {
		t.Fatalf("second edit = %q", secondEdit)
	}
}

type recordingHTTPClient struct {
	path      string
	chatID    string
	messageID string
	text      string
}

func (client *recordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		return nil, err
	}
	client.path = request.URL.Path
	client.chatID = request.FormValue("chat_id")
	client.messageID = request.FormValue("message_id")
	client.text = request.FormValue("text")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			`{"ok":true,"result":{"message_id":20,"date":0,"chat":{"id":-100,"type":"supergroup"}}}`,
		)),
	}, nil
}

func TestEditMirroredUserMessage(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "edit.sqlite3"), 4)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer messageStore.Close()
	if err := messageStore.SaveMessageMap(&model.MessageMap{
		UserChatMessageID:  10,
		GroupChatMessageID: 20,
		UserID:             123,
		MessageText:        "你好",
	}); err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	httpClient := &recordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token",
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Second, httpClient),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := New(&config.Config{AdminGroupID: -100}, messageStore, job.New(), logger)

	err = services.EditMirroredMessage(context.Background(), telegramBot, &models.Message{
		ID:   10,
		Text: "我好",
		Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate},
	})
	if err != nil {
		t.Fatalf("edit mirrored message: %v", err)
	}
	if !strings.HasSuffix(httpClient.path, "/editMessageText") {
		t.Fatalf("API path = %q", httpClient.path)
	}
	if httpClient.chatID != "-100" || httpClient.messageID != "20" {
		t.Fatalf("edit target chat=%q message=%q", httpClient.chatID, httpClient.messageID)
	}
	if httpClient.text != "我好(修改前：你好)" {
		t.Fatalf("edited text = %q", httpClient.text)
	}
	mapping, err := messageStore.GetByUserMessageID(123, 10)
	if err != nil {
		t.Fatalf("reload mapping: %v", err)
	}
	if mapping.MessageText != "我好" {
		t.Fatalf("stored latest text = %q", mapping.MessageText)
	}
}
func TestAdminForwardFailureMessageForDeletedReply(t *testing.T) {
	err := errors.New("bad request, Bad Request: message to be replied not found")
	if got := adminForwardFailureMessage(err); got != "当前回复引用的信息被对方删除" {
		t.Fatalf("message = %q", got)
	}
}

func TestAdminForwardFailureMessagePreservesOtherErrors(t *testing.T) {
	err := errors.New("network unavailable")
	if got := adminForwardFailureMessage(err); got != "发送失败: network unavailable" {
		t.Fatalf("message = %q", got)
	}
}

type deletedReplyHTTPClient struct {
	noticeText string
}

func (client *deletedReplyHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		_ = request.ParseForm()
	}
	body := `{"ok":true,"result":{"message_id":31,"date":0,"chat":{"id":-100,"type":"supergroup"}}}`
	if strings.HasSuffix(request.URL.Path, "/copyMessage") {
		body = `{"ok":false,"error_code":400,"description":"Bad Request: message to be replied not found"}`
	}
	if strings.HasSuffix(request.URL.Path, "/sendMessage") {
		client.noticeText = request.FormValue("text")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestForwardAdminToUserExplainsDeletedReply(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "deleted-reply.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer messageStore.Close()
	if _, err := messageStore.EnsureUser(&model.User{UserID: 123}); err != nil {
		t.Fatal(err)
	}
	if err := messageStore.UpdateUserThreadID(123, 55); err != nil {
		t.Fatal(err)
	}
	if err := messageStore.SaveMessageMap(&model.MessageMap{UserChatMessageID: 10, GroupChatMessageID: 20, UserID: 123}); err != nil {
		t.Fatal(err)
	}

	httpClient := &deletedReplyHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	services := New(&config.Config{AdminGroupID: -100}, messageStore, job.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	err = services.ForwardAdminToUser(context.Background(), telegramBot, &models.Message{
		ID:              30,
		Text:            "reply",
		MessageThreadID: 55,
		Chat:            models.Chat{ID: -100, Type: models.ChatTypeSupergroup},
		ReplyToMessage:  &models.Message{ID: 20},
	})
	if err == nil {
		t.Fatal("expected Telegram reply error")
	}
	if httpClient.noticeText != "当前回复引用的信息被对方删除" {
		t.Fatalf("notice = %q", httpClient.noticeText)
	}
}
