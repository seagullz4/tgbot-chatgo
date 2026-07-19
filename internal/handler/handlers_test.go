package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/command"
	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/model"
	"telegram-interactive-bot/go-bot/internal/service"
	storesqlite "telegram-interactive-bot/go-bot/internal/store/sqlite"
)

func TestMatchesCommand(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		command     string
		botUsername string
		want        bool
	}{
		{name: "bare command", text: "/help", command: "help", botUsername: "SupportBot", want: true},
		{name: "addressed command", text: "/help@SupportBot", command: "help", botUsername: "SupportBot", want: true},
		{name: "case insensitive", text: "/HELP@supportbot", command: "help", botUsername: "SupportBot", want: true},
		{name: "addressed command with arguments", text: "/help@SupportBot details", command: "help", botUsername: "SupportBot", want: true},
		{name: "other bot", text: "/help@AnotherBot", command: "help", botUsername: "SupportBot", want: false},
		{name: "different command", text: "/status@SupportBot", command: "help", botUsername: "SupportBot", want: false},
		{name: "missing bot username", text: "/help@SupportBot", command: "help", want: false},
		{name: "not a command", text: "help", command: "help", botUsername: "SupportBot", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update := &models.Update{Message: &models.Message{Text: test.text}}
			if got := command.Matches(update, test.command, test.botUsername); got != test.want {
				t.Fatalf("matchesCommand(%q, %q, %q) = %v, want %v", test.text, test.command, test.botUsername, got, test.want)
			}
		})
	}
}

type handlerRecordingHTTPClient struct {
	paths   []string
	texts   []string
	markups []string
}

func (client *handlerRecordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		return nil, err
	}
	client.paths = append(client.paths, request.URL.Path)
	client.texts = append(client.texts, request.FormValue("text"))
	client.markups = append(client.markups, request.FormValue("reply_markup"))
	result := "{\"message_id\":20,\"date\":0,\"chat\":{\"id\":123,\"type\":\"private\"}}"
	if strings.HasSuffix(request.URL.Path, "/getChat") {
		result = "{\"id\":-100,\"type\":\"supergroup\",\"title\":\"Admin Group\",\"is_forum\":true}"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{\"ok\":true,\"result\":" + result + "}")),
	}, nil
}

func TestClosedConversationDoesNotSendForwardAcknowledgement(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "closed.sqlite3"), 4)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer messageStore.Close()

	if _, err := messageStore.EnsureUser(&model.User{UserID: 123, FirstName: "User"}); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	if err := messageStore.UpdateUserThreadID(123, 77); err != nil {
		t.Fatalf("set thread: %v", err)
	}
	if err := messageStore.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 77, Status: "closed"}); err != nil {
		t.Fatalf("close forum status: %v", err)
	}

	httpClient := &handlerRecordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token",
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Second, httpClient),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := service.New(&config.Config{
		AdminGroupID:        -100,
		AdminUserIDs:        map[int64]struct{}{999: {}},
		DisableVerification: false,
		UserForwardAck:      true,
		MessageInterval:     0,
	}, messageStore, job.New(), logger)
	handlers := New(services, logger)

	handlers.userToAdmin(context.Background(), telegramBot, &models.Message{
		ID:   10,
		Text: "还有问题",
		From: &models.User{ID: 123, FirstName: "User"},
		Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate},
	})

	joined := strings.Join(httpClient.texts, "\n")
	if !strings.Contains(joined, "客服已关闭") {
		t.Fatalf("closed notice not sent, messages=%q", httpClient.texts)
	}
	if strings.Contains(joined, "已转达客服") {
		t.Fatalf("forward acknowledgement sent for closed conversation, messages=%q", httpClient.texts)
	}
	for _, requestPath := range httpClient.paths {
		if strings.HasSuffix(requestPath, "/sendPhoto") {
			t.Fatalf("closed conversation incorrectly requested verification: paths=%q", httpClient.paths)
		}
	}
}

func TestAdminPrivateAccessAndKeyboard(t *testing.T) {
	handlers := New(&service.Services{Cfg: &config.Config{AdminGroupID: -100, AdminUserIDs: map[int64]struct{}{999: {}}}}, nil)
	message := &models.Message{From: &models.User{ID: 999}, Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate}}
	if !handlers.validAdminAccess(context.Background(), nil, message) {
		t.Fatal("configured admin should have private admin access")
	}
	keyboard := handlers.privateKeyboard(999)
	if keyboard.InputFieldPlaceholder != "管理员快捷操作" {
		t.Fatalf("admin keyboard placeholder = %q", keyboard.InputFieldPlaceholder)
	}
}

func TestGroupOnlyAdminCommandGivesPrivateChatGuidance(t *testing.T) {
	httpClient := &handlerRecordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token",
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Second, httpClient),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	handlers := New(&service.Services{Cfg: &config.Config{
		AdminGroupID: -100,
		AdminUserIDs: map[int64]struct{}{999: {}},
	}}, nil)

	handlers.Close(context.Background(), telegramBot, &models.Update{Message: &models.Message{
		Text: "/close",
		From: &models.User{ID: 999},
		Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate},
	}})
	if len(httpClient.texts) != 1 || !strings.Contains(httpClient.texts[0], "只能在管理群") {
		t.Fatalf("admin private guidance = %q", httpClient.texts)
	}

	httpClient.texts = nil
	handlers.Close(context.Background(), telegramBot, &models.Update{Message: &models.Message{
		Text: "/close",
		From: &models.User{ID: 123},
		Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate},
	}})
	if len(httpClient.texts) != 1 || !strings.Contains(httpClient.texts[0], "仅供管理员") {
		t.Fatalf("user private rejection = %q", httpClient.texts)
	}
}

func TestOpenConversationStillSendsForwardAcknowledgement(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "opened.sqlite3"), 4)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer messageStore.Close()
	if _, err := messageStore.EnsureUser(&model.User{UserID: 123, FirstName: "User"}); err != nil {
		t.Fatalf("ensure user: %v", err)
	}
	if err := messageStore.UpdateUserThreadID(123, 77); err != nil {
		t.Fatalf("set thread: %v", err)
	}
	if err := messageStore.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 77, Status: "opened"}); err != nil {
		t.Fatalf("open forum status: %v", err)
	}
	httpClient := &handlerRecordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, httpClient))
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers := New(service.New(&config.Config{
		AdminGroupID:        -100,
		AdminUserIDs:        map[int64]struct{}{999: {}},
		DisableVerification: true,
		UserForwardAck:      true,
		MessageInterval:     0,
	}, messageStore, job.New(), logger), logger)

	handlers.userToAdmin(context.Background(), telegramBot, &models.Message{
		ID:   10,
		Text: "正常消息",
		From: &models.User{ID: 123, FirstName: "User"},
		Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate},
	})
	joined := strings.Join(httpClient.texts, "\n")
	if !strings.Contains(joined, "已转达客服") {
		t.Fatalf("successful forward acknowledgement missing: texts=%q paths=%q", httpClient.texts, httpClient.paths)
	}
	foundCopy := false
	for _, requestPath := range httpClient.paths {
		if strings.HasSuffix(requestPath, "/copyMessage") {
			foundCopy = true
		}
	}
	if !foundCopy {
		t.Fatalf("open conversation message was not copied: %q", httpClient.paths)
	}
}

func TestAdminStartDoesNotCreateOrdinaryUserRecord(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "admin-start.sqlite3"), 4)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer messageStore.Close()
	httpClient := &handlerRecordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, httpClient))
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers := New(service.New(&config.Config{
		AppName:      "Support Bot",
		AdminGroupID: -100,
		AdminUserIDs: map[int64]struct{}{999: {}},
	}, messageStore, job.New(), logger), logger)

	handlers.Start(context.Background(), telegramBot, &models.Update{Message: &models.Message{
		Text: "/start",
		From: &models.User{ID: 999, FirstName: "Admin"},
		Chat: models.Chat{ID: 999, Type: models.ChatTypePrivate},
	}})
	storedAdmin, err := messageStore.GetUserByTelegramID(999)
	if err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if storedAdmin != nil {
		t.Fatalf("admin was stored as ordinary user: %+v", storedAdmin)
	}
	if !strings.Contains(strings.Join(httpClient.texts, "\n"), "管理员账号不会进入普通用户会话") {
		t.Fatalf("admin welcome text = %q", httpClient.texts)
	}
}
func TestOwnerPrivateKeyboardUsesFunctionManagement(t *testing.T) {
	handlers := New(&service.Services{Cfg: &config.Config{OwnerUserIDs: map[int64]struct{}{10: {}}}}, nil)
	keyboard := handlers.privateKeyboard(10)
	found := false
	for _, row := range keyboard.Keyboard {
		for _, button := range row {
			if button.Text == "功能管理" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("owner private keyboard missing function management")
	}
}

func TestUserInfoContactKeyboard(t *testing.T) {
	tests := []struct {
		name      string
		username  string
		wantLabel string
		wantURL   string
	}{
		{name: "username", username: "alice", wantLabel: "联系 @alice", wantURL: "https://t.me/alice"},
		{name: "normalized username", username: " @alice ", wantLabel: "联系 @alice", wantURL: "https://t.me/alice"},
		{name: "telegram id", wantLabel: "直接联系用户", wantURL: "tg://user?id=123"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keyboard := userInfoContactKeyboard(123, test.username)
			if keyboard == nil || len(keyboard.InlineKeyboard) != 1 || len(keyboard.InlineKeyboard[0]) != 1 {
				t.Fatalf("unexpected keyboard: %+v", keyboard)
			}
			button := keyboard.InlineKeyboard[0][0]
			if button.Text != test.wantLabel || button.URL != test.wantURL {
				t.Fatalf("button = text %q url %q, want text %q url %q", button.Text, button.URL, test.wantLabel, test.wantURL)
			}
		})
	}
}

type privacyRestrictedInfoHTTPClient struct {
	texts   []string
	markups []string
}

func (client *privacyRestrictedInfoHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		return nil, err
	}
	text := request.FormValue("text")
	markup := request.FormValue("reply_markup")
	client.texts = append(client.texts, text)
	client.markups = append(client.markups, markup)
	body := `{"ok":true,"result":{"message_id":20,"date":0,"chat":{"id":-100,"type":"supergroup"}}}`
	if strings.Contains(markup, "tg://user?id=123") {
		body = `{"ok":false,"error_code":400,"description":"Bad Request: BUTTON_USER_PRIVACY_RESTRICTED"}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestDefaultRejectsUnconfiguredAdminGroupSender(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "unauthorized-reply.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer messageStore.Close()
	if _, err := messageStore.EnsureUser(&model.User{UserID: 123, FirstName: "User"}); err != nil {
		t.Fatal(err)
	}
	if err := messageStore.UpdateUserThreadID(123, 77); err != nil {
		t.Fatal(err)
	}

	httpClient := &handlerRecordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := service.New(&config.Config{AdminGroupID: -100, AdminUserIDs: map[int64]struct{}{999: {}}}, messageStore, job.New(), logger)
	handlers := New(services, logger)

	handlers.Default(context.Background(), telegramBot, &models.Update{Message: &models.Message{
		ID:              30,
		Text:            "越权回复",
		MessageThreadID: 77,
		From:            &models.User{ID: 555},
		Chat:            models.Chat{ID: -100, Type: models.ChatTypeSupergroup},
	}})

	for _, requestPath := range httpClient.paths {
		if strings.HasSuffix(requestPath, "/copyMessage") {
			t.Fatalf("unauthorized message was forwarded: paths=%q", httpClient.paths)
		}
	}
	if joined := strings.Join(httpClient.texts, "\n"); !strings.Contains(joined, "未转达用户") {
		t.Fatalf("unauthorized sender did not receive rejection: texts=%q", httpClient.texts)
	}
}

func TestDefaultRejectsUnauthorizedAdminGroupEdit(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "unauthorized-edit.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer messageStore.Close()
	if err := messageStore.SaveMessageMap(&model.MessageMap{UserChatMessageID: 10, GroupChatMessageID: 20, UserID: 123, MessageText: "原消息"}); err != nil {
		t.Fatal(err)
	}

	httpClient := &handlerRecordingHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := service.New(&config.Config{AdminGroupID: -100, AdminUserIDs: map[int64]struct{}{999: {}}}, messageStore, job.New(), logger)
	handlers := New(services, logger)

	handlers.Default(context.Background(), telegramBot, &models.Update{EditedMessage: &models.Message{
		ID:              20,
		Text:            "被修改的越权消息",
		MessageThreadID: 77,
		From:            &models.User{ID: 555},
		Chat:            models.Chat{ID: -100, Type: models.ChatTypeSupergroup},
	}})

	for _, requestPath := range httpClient.paths {
		if strings.HasSuffix(requestPath, "/editMessageText") || strings.HasSuffix(requestPath, "/editMessageCaption") {
			t.Fatalf("unauthorized edit was mirrored: paths=%q", httpClient.paths)
		}
	}
}

func TestInfoFallsBackWhenDirectContactIsPrivacyRestricted(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "privacy-info.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer messageStore.Close()
	if _, err := messageStore.EnsureUser(&model.User{UserID: 123, FirstName: "Private User"}); err != nil {
		t.Fatal(err)
	}
	if err := messageStore.UpdateUserThreadID(123, 77); err != nil {
		t.Fatal(err)
	}

	httpClient := &privacyRestrictedInfoHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := service.New(&config.Config{AdminGroupID: -100, AdminUserIDs: map[int64]struct{}{999: {}}, DisableVerification: true}, messageStore, job.New(), logger)
	handlers := New(services, logger)

	handlers.Info(context.Background(), telegramBot, &models.Update{Message: &models.Message{
		ID:              31,
		Text:            "/info",
		MessageThreadID: 77,
		From:            &models.User{ID: 999},
		Chat:            models.Chat{ID: -100, Type: models.ChatTypeSupergroup},
	}})

	if len(httpClient.texts) != 2 {
		t.Fatalf("send attempts = %d, texts=%q", len(httpClient.texts), httpClient.texts)
	}
	if httpClient.markups[0] == "" || httpClient.markups[1] != "" {
		t.Fatalf("fallback markups = %q", httpClient.markups)
	}
	fallback := httpClient.texts[1]
	for _, expected := range []string{"用户信息", "ID：<code>123</code>", "会话状态：已打开", "该用户无法直接联系"} {
		if !strings.Contains(fallback, expected) {
			t.Fatalf("fallback info missing %q: %q", expected, fallback)
		}
	}
}
