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

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/model"
	"telegram-interactive-bot/go-bot/internal/service"
	storesqlite "telegram-interactive-bot/go-bot/internal/store/sqlite"
)

func TestStartCommandPatternMatchesTelegramCommand(t *testing.T) {
	called := false
	telegramBot, err := bot.New("123456:test-token",
		bot.WithSkipGetMe(),
		bot.WithNotAsyncHandlers(),
		bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {
			t.Fatal("default handler received /start")
		}),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	telegramBot.RegisterHandler(
		bot.HandlerTypeMessageText,
		commandStart,
		bot.MatchTypeCommandStartOnly,
		func(context.Context, *bot.Bot, *models.Update) { called = true },
	)

	telegramBot.ProcessUpdate(context.Background(), &models.Update{
		ID: 1,
		Message: &models.Message{
			Text: "/start",
			Entities: []models.MessageEntity{{
				Type:   models.MessageEntityTypeBotCommand,
				Offset: 0,
				Length: 6,
			}},
		},
	})

	if !called {
		t.Fatal("/start did not match the registered command")
	}
}

type handlerRecordingHTTPClient struct {
	paths []string
	texts []string
}

func (client *handlerRecordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		return nil, err
	}
	client.paths = append(client.paths, request.URL.Path)
	client.texts = append(client.texts, request.FormValue("text"))
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
