package service

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
)

func TestCommandMenusSeparateUserAndAdminCommands(t *testing.T) {
	userCommands := commandSet(UserCommandMenu())
	for _, command := range []string{"ban", "unban", "banned", "info", "close", "open", "clear", "broadcast", "say"} {
		if userCommands[command] {
			t.Fatalf("user command menu exposes admin command /%s", command)
		}
	}

	adminPrivate := commandSet(AdminPrivateCommandMenu())
	for _, command := range []string{"banned", "info", "unban"} {
		if !adminPrivate[command] {
			t.Fatalf("admin private menu missing /%s", command)
		}
	}
	for _, command := range []string{"ban", "close", "open", "clear", "broadcast", "say"} {
		if adminPrivate[command] {
			t.Fatalf("admin private menu exposes group-only command /%s", command)
		}
	}

	adminGroup := commandSet(AdminGroupCommandMenu())
	for _, command := range []string{"info", "banned", "close", "open", "ban", "unban", "clear", "broadcast", "say"} {
		if !adminGroup[command] {
			t.Fatalf("admin group menu missing /%s", command)
		}
	}
}

func TestUserAndAdminKeyboardsAreDifferent(t *testing.T) {
	userKeyboard := UserKeyboard()
	adminKeyboard := AdminKeyboard()
	if userKeyboard.InputFieldPlaceholder == adminKeyboard.InputFieldPlaceholder {
		t.Fatal("user and admin keyboards should have different placeholders")
	}
	if userKeyboard.Keyboard[0][0].Text == adminKeyboard.Keyboard[0][0].Text {
		t.Fatal("user and admin keyboards should expose different shortcuts")
	}
}

func commandSet(commands []models.BotCommand) map[string]bool {
	set := make(map[string]bool, len(commands))
	for _, command := range commands {
		set[command.Command] = true
	}
	return set
}

type menuRegistrationHTTPClient struct {
	paths    []string
	scopes   []string
	commands []string
}

func (client *menuRegistrationHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		return nil, err
	}
	client.paths = append(client.paths, request.URL.Path)
	client.scopes = append(client.scopes, request.FormValue("scope"))
	client.commands = append(client.commands, request.FormValue("commands"))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{\"ok\":true,\"result\":true}")),
	}, nil
}

func TestRegisterCommandMenusUsesSeparateTelegramScopes(t *testing.T) {
	client := &menuRegistrationHTTPClient{}
	telegramBot, err := bot.New("123456:test-token",
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Second, client),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := &Services{
		Cfg: &config.Config{
			AdminGroupID: -100,
			AdminUserIDs: map[int64]struct{}{999: {}},
		},
		Logger: logger,
	}
	services.RegisterCommandMenus(context.Background(), telegramBot)

	if len(client.paths) != 4 || len(client.scopes) != 4 || len(client.commands) != 4 {
		t.Fatalf("command menu requests paths=%q scopes=%q commands=%q", client.paths, client.scopes, client.commands)
	}
	if !strings.HasSuffix(client.paths[0], "/deleteMyCommands") || !strings.Contains(client.scopes[0], "default") {
		t.Fatalf("legacy default menu was not cleared first: paths=%q scopes=%q", client.paths, client.scopes)
	}
	if !strings.Contains(client.scopes[1], "all_private_chats") {
		t.Fatalf("user private scope = %q", client.scopes[1])
	}
	if !strings.Contains(client.scopes[2], "999") {
		t.Fatalf("admin private scope = %q", client.scopes[2])
	}
	if !strings.Contains(client.scopes[3], "-100") {
		t.Fatalf("admin group scope = %q", client.scopes[3])
	}
	if strings.Contains(client.commands[1], "\"ban\"") || strings.Contains(client.commands[1], "\"banned\"") {
		t.Fatalf("user private commands expose moderation: %s", client.commands[1])
	}
	if !strings.Contains(client.commands[2], "\"banned\"") || strings.Contains(client.commands[2], "\"close\"") {
		t.Fatalf("admin private commands are incorrect: %s", client.commands[2])
	}
	if !strings.Contains(client.commands[3], "\"close\"") || !strings.Contains(client.commands[3], "\"broadcast\"") {
		t.Fatalf("admin group commands are incomplete: %s", client.commands[3])
	}
}
