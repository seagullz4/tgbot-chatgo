package function

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"telegram-interactive-bot/go-bot/internal/command"
	"telegram-interactive-bot/go-bot/internal/config"
)

func newTestConfigManager(t *testing.T) *config.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	data := "BOT_TOKEN=123456:test\nADMIN_GROUP_ID=-100123\nADMIN_USER_IDS=20\nOWNER_USER_IDS=10\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", path)
	manager, err := config.OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestOwnerPrivateAccessAndPendingIsolation(t *testing.T) {
	manager := &Manager{cfg: newTestConfigManager(t), pending: map[int64]pending{}, confirmations: map[int64]confirmation{}}
	owner := &models.Message{From: &models.User{ID: 10}, Chat: models.Chat{ID: 10, Type: models.ChatTypePrivate}, Text: "value"}
	admin := &models.Message{From: &models.User{ID: 20}, Chat: models.Chat{ID: 20, Type: models.ChatTypePrivate}, Text: "value"}
	if !manager.ownerPrivate(owner) || manager.ownerPrivate(admin) {
		t.Fatal("owner permission check failed")
	}
	manager.pending[10] = pending{action: "setwelcome", expires: time.Now().Add(time.Minute)}
	if !manager.hasPending(&models.Update{Message: owner}) {
		t.Fatal("owner pending input was not matched")
	}
	if manager.hasPending(&models.Update{Message: admin}) {
		t.Fatal("admin consumed owner pending input")
	}
}

func TestCommandMatchesAddressedOwnerCommand(t *testing.T) {
	update := &models.Update{Message: &models.Message{Text: "/function@SupportBot"}}
	if !command.Matches(update, "function", "supportbot") {
		t.Fatal("addressed command did not match")
	}
	if command.Matches(update, "function", "otherbot") {
		t.Fatal("command for another bot matched")
	}
}

type functionHTTPClient struct {
	texts   []string
	markups []string
}

func (client *functionHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		_ = request.ParseForm()
	}
	client.texts = append(client.texts, request.FormValue("text"))
	client.markups = append(client.markups, request.FormValue("reply_markup"))
	body := `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":10,"type":"private"}}}`
	if strings.HasSuffix(request.URL.Path, "/answerCallbackQuery") {
		body = `{"ok":true,"result":true}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func TestCancelReturnsToFunctionPanel(t *testing.T) {
	client := &functionHTTPClient{}
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, client))
	if err != nil {
		t.Fatal(err)
	}
	manager := New(newTestConfigManager(t), nil, nil)
	manager.pending[10] = pending{action: "setwelcome", expires: time.Now().Add(time.Minute)}
	manager.confirmations[10] = confirmation{expires: time.Now().Add(time.Minute)}

	manager.Callback(context.Background(), telegramBot, privateOwnerCallback("callback", "fn:cancel"))

	if _, exists := manager.pending[10]; exists {
		t.Fatal("pending operation was not cleared")
	}
	if _, exists := manager.confirmations[10]; exists {
		t.Fatal("confirmation was not cleared")
	}
	if len(client.texts) < 2 || !strings.Contains(client.texts[len(client.texts)-1], "机器人运维设置") {
		t.Fatalf("cancel did not return to panel: %#v", client.texts)
	}
	if strings.Contains(strings.Join(client.markups, "\n"), "addowner") {
		t.Fatal("panel still exposes addowner action")
	}
}

type applyingController struct {
	cfg *config.Manager
}

func (controller *applyingController) Info() BotInfo                { return BotInfo{} }
func (controller *applyingController) Reload(context.Context) error { return nil }
func (controller *applyingController) Update(_ context.Context, updates map[string]string, _ bool) error {
	candidate, err := controller.cfg.Preview(updates)
	if err != nil {
		return err
	}
	hash, err := controller.cfg.Write(updates)
	if err != nil {
		return err
	}
	controller.cfg.Apply(candidate, hash)
	return nil
}

func newFunctionTestBot(t *testing.T, client *functionHTTPClient) *bot.Bot {
	t.Helper()
	telegramBot, err := bot.New("123456:test-token", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, client))
	if err != nil {
		t.Fatal(err)
	}
	return telegramBot
}

func privateOwnerCallback(id, data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   id,
		From: models.User{ID: 10},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Type:    models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{Chat: models.Chat{ID: 10, Type: models.ChatTypePrivate}},
		},
	}}
}

func TestOwnerPrivateCallback(t *testing.T) {
	manager := New(newTestConfigManager(t), nil, nil)
	private := privateOwnerCallback("private", "fn:home").CallbackQuery
	if !manager.ownerPrivateCallback(private) {
		t.Fatal("private owner callback was rejected")
	}
	group := privateOwnerCallback("group", "fn:home").CallbackQuery
	group.Message.Message.Chat.Type = models.ChatTypeSupergroup
	if manager.ownerPrivateCallback(group) {
		t.Fatal("group callback was accepted")
	}
	inaccessible := privateOwnerCallback("inaccessible", "fn:home").CallbackQuery
	inaccessible.Message.Message = nil
	if manager.ownerPrivateCallback(inaccessible) {
		t.Fatal("inaccessible callback was accepted")
	}
	admin := privateOwnerCallback("admin", "fn:home").CallbackQuery
	admin.From.ID = 20
	if manager.ownerPrivateCallback(admin) {
		t.Fatal("ordinary admin callback was accepted")
	}
}

func TestFunctionManagementShortcut(t *testing.T) {
	manager := New(newTestConfigManager(t), nil, nil)
	if !manager.functionShortcut(&models.Update{Message: &models.Message{Text: " 功能管理 ", From: &models.User{ID: 10}, Chat: models.Chat{ID: 10, Type: models.ChatTypePrivate}}}) {
		t.Fatal("function management shortcut did not match")
	}
	if manager.functionShortcut(&models.Update{Message: &models.Message{Text: "管理帮助", From: &models.User{ID: 10}, Chat: models.Chat{ID: 10, Type: models.ChatTypePrivate}}}) {
		t.Fatal("unrelated shortcut matched")
	}
	if manager.functionShortcut(&models.Update{Message: &models.Message{Text: "功能管理", From: &models.User{ID: 20}, Chat: models.Chat{ID: 20, Type: models.ChatTypePrivate}}}) {
		t.Fatal("ordinary admin shortcut matched owner function")
	}
}

func TestAdminListAndClickDelete(t *testing.T) {
	cfg := newTestConfigManager(t)
	manager := New(cfg, nil, nil)
	manager.SetController(&applyingController{cfg: cfg})
	client := &functionHTTPClient{}
	telegramBot := newFunctionTestBot(t, client)

	manager.Callback(context.Background(), telegramBot, privateOwnerCallback("list", "fn:admins:0"))
	if len(client.texts) == 0 || !strings.Contains(client.texts[len(client.texts)-1], "普通管理员（1）") || !strings.Contains(client.texts[len(client.texts)-1], "20") {
		t.Fatalf("admin list text = %#v", client.texts)
	}
	if !strings.Contains(client.markups[len(client.markups)-1], "fn:deladmins:0") {
		t.Fatalf("admin list keyboard = %q", client.markups[len(client.markups)-1])
	}

	manager.Callback(context.Background(), telegramBot, privateOwnerCallback("delete-list", "fn:deladmins:0"))
	if !strings.Contains(client.markups[len(client.markups)-1], "fn:deladmin:20:0") {
		t.Fatalf("delete keyboard = %q", client.markups[len(client.markups)-1])
	}

	manager.Callback(context.Background(), telegramBot, privateOwnerCallback("delete", "fn:deladmin:20:0"))
	if len(cfg.Current().AdminUserIDs) != 0 {
		t.Fatalf("ordinary admins after delete = %v", cfg.Current().AdminUserIDs)
	}
	if !strings.Contains(client.texts[len(client.texts)-1], "当前没有普通管理员") {
		t.Fatalf("empty admin text = %q", client.texts[len(client.texts)-1])
	}
	lastMarkup := client.markups[len(client.markups)-1]
	if !strings.Contains(lastMarkup, "fn:home") || strings.Contains(lastMarkup, "fn:deladmin:") {
		t.Fatalf("empty admin keyboard = %q", lastMarkup)
	}
}

func TestAdminPageBounds(t *testing.T) {
	page, start, end, pages := adminPageBounds(99, 18)
	if page != 2 || start != 16 || end != 18 || pages != 3 {
		t.Fatalf("bounds = page:%d start:%d end:%d pages:%d", page, start, end, pages)
	}
}
