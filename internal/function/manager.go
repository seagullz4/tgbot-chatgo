package function

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/command"
	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/logging"
)

type BotInfo struct {
	ID         int64
	Username   string
	GroupTitle string
}
type Controller interface {
	Info() BotInfo
	Update(context.Context, map[string]string, bool) error
	Reload(context.Context) error
}

type pending struct {
	action  string
	expires time.Time
}
type confirmation struct {
	updates   map[string]string
	reset     bool
	clearLogs bool
	expires   time.Time
}

type Manager struct {
	cfg           *config.Manager
	logs          *logging.RotatingWriter
	logger        *slog.Logger
	mu            sync.Mutex
	pending       map[int64]pending
	confirmations map[int64]confirmation
	controller    Controller
}

func New(cfg *config.Manager, logs *logging.RotatingWriter, logger *slog.Logger) *Manager {
	return &Manager{cfg: cfg, logs: logs, logger: logger, pending: map[int64]pending{}, confirmations: map[int64]confirmation{}}
}
func (m *Manager) SetController(controller Controller) {
	m.mu.Lock()
	m.controller = controller
	m.mu.Unlock()
}

func (m *Manager) Register(b *bot.Bot, username string) {
	commands := map[string]bot.HandlerFunc{"function": m.Function, "reload": m.Reload, "logs": m.Logs, "clearlogs": m.ClearLogs, "cancel": m.Cancel}
	for commandName, handler := range commands {
		registered := commandName
		b.RegisterHandlerMatchFunc(func(update *models.Update) bool { return command.Matches(update, registered, username) }, handler)
	}
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "fn:", bot.MatchTypePrefix, m.Callback)
	b.RegisterHandlerMatchFunc(m.functionShortcut, m.Function)
	b.RegisterHandlerMatchFunc(m.hasPending, m.PendingInput)
}

func (m *Manager) functionShortcut(update *models.Update) bool {
	return update != nil && m.ownerPrivate(update.Message) && strings.TrimSpace(update.Message.Text) == command.ShortcutFunctionManagement
}

func (m *Manager) ownerPrivate(message *models.Message) bool {
	return message != nil && message.From != nil && message.Chat.Type == models.ChatTypePrivate && m.cfg.IsOwner(message.From.ID)
}
func (m *Manager) ownerPrivateCallback(query *models.CallbackQuery) bool {
	return query != nil && m.cfg.IsOwner(query.From.ID) && query.Message.Message != nil && query.Message.Message.Chat.Type == models.ChatTypePrivate
}
func (m *Manager) hasPending(update *models.Update) bool {
	if update == nil || !m.ownerPrivate(update.Message) || strings.HasPrefix(update.Message.Text, "/") {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.pending[update.Message.From.ID]
	if ok && time.Now().After(state.expires) {
		delete(m.pending, update.Message.From.ID)
		return false
	}
	return ok
}

func (m *Manager) Function(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !m.ownerPrivate(update.Message) {
		return
	}
	m.clearOperation(update.Message.From.ID)
	m.sendPanel(ctx, b, update.Message.Chat.ID)
}
func (m *Manager) sendPanel(ctx context.Context, b *bot.Bot, chatID int64) {
	cfg := m.cfg.Current()
	m.mu.Lock()
	controller := m.controller
	m.mu.Unlock()
	info := BotInfo{}
	if controller != nil {
		info = controller.Info()
	}
	welcome := strings.ReplaceAll(cfg.WelcomeMessage, "\n", " ")
	runes := []rune(welcome)
	if len(runes) > 60 {
		welcome = string(runes[:60]) + "…"
	}
	text := fmt.Sprintf("<b>机器人运维设置</b>\n\nBot：@%s (<code>%d</code>)\n管理群：%s (<code>%d</code>)\n普通管理员：%d\n超级管理员：1\nToken：***%s\n欢迎语：%s\n状态通知：%s", escape(info.Username), info.ID, escape(info.GroupTitle), cfg.AdminGroupID, len(cfg.AdminUserIDs), tokenTail(cfg.BotToken), escape(welcome), escape(config.FormatStatusNotifyInterval(cfg.StatusNotifyIntervalMinutes)))
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "切换管理群", CallbackData: "fn:setgroup"}, {Text: "修改欢迎语", CallbackData: "fn:setwelcome"}},
		{{Text: "添加管理员", CallbackData: "fn:addadmin"}, {Text: "查询管理员", CallbackData: "fn:admins:0"}},
		{{Text: "删除管理员", CallbackData: "fn:deladmins:0"}, {Text: "切换 Token", CallbackData: "fn:settoken"}},
		{{Text: "定时状态通知", CallbackData: "fn:setstatusnotify"}, {Text: "重新加载配置", CallbackData: "fn:reload"}},
		{{Text: "下载日志", CallbackData: "fn:logs"}, {Text: "清除日志", CallbackData: "fn:clearlogs"}},
	}}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: keyboard})
}

func (m *Manager) Callback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil {
		return
	}
	q := update.CallbackQuery
	if !m.ownerPrivateCallback(q) {
		return
	}
	data := strings.TrimPrefix(q.Data, "fn:")
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	if m.handleAdminCallback(ctx, b, q.From.ID, data) {
		return
	}
	switch data {
	case "setgroup", "setwelcome", "addadmin", "settoken", "setstatusnotify":
		m.begin(ctx, b, q.From.ID, data)
	case "reload":
		m.reload(ctx, b, q.From.ID)
	case "logs":
		m.sendLogs(ctx, b, q.From.ID)
	case "clearlogs":
		m.mu.Lock()
		m.confirmations[q.From.ID] = confirmation{clearLogs: true, expires: time.Now().Add(time.Minute)}
		m.mu.Unlock()
		m.sendConfirm(ctx, b, q.From.ID, "确认清除当前及所有归档日志？")
	case "confirm":
		m.confirm(ctx, b, q.From.ID)
	case "cancel":
		m.cancel(ctx, b, q.From.ID)
	case "home":
		m.clearOperation(q.From.ID)
		m.sendPanel(ctx, b, q.From.ID)
	}
}
func (m *Manager) begin(ctx context.Context, b *bot.Bot, userID int64, action string) {
	prompts := map[string]string{"setgroup": "请输入新的管理超级群 ID（-100...）", "setwelcome": "请输入新的欢迎语，可包含换行", "addadmin": "请输入要添加的管理员用户 ID", "settoken": "请输入新的 Bot Token；消息处理后会尝试删除", "setstatusnotify": "请输入状态通知间隔（小时，支持小数，如 0.5=30 分钟，1=1 小时；0 表示关闭）"}
	m.mu.Lock()
	m.pending[userID] = pending{action: action, expires: time.Now().Add(5 * time.Minute)}
	m.mu.Unlock()
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: userID, Text: prompts[action] + "\n发送 /cancel 可取消。"})
}

func (m *Manager) PendingInput(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	userID := message.From.ID
	m.mu.Lock()
	state := m.pending[userID]
	delete(m.pending, userID)
	m.mu.Unlock()
	value := strings.TrimSpace(message.Text)
	if state.action == "settoken" {
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: message.Chat.ID, MessageID: message.ID})
	}
	cfg := m.cfg.Current()
	updates := map[string]string{}
	switch state.action {
	case "setgroup":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			m.reply(ctx, b, userID, "群组 ID 格式错误")
			return
		}
		updates["ADMIN_GROUP_ID"] = value
		m.requestConfigConfirm(ctx, b, userID, updates, true, "切换管理群会重置全部用户话题映射，旧群历史不会删除。是否继续？")
		return
	case "setwelcome":
		if value == "" {
			m.reply(ctx, b, userID, "欢迎语不能为空")
			return
		}
		updates["WELCOME_MESSAGE"] = message.Text
	case "settoken":
		if !strings.Contains(value, ":") {
			m.reply(ctx, b, userID, "Token 格式错误")
			return
		}
		updates["BOT_TOKEN"] = value
		m.requestConfigConfirm(ctx, b, userID, updates, false, "确认切换 Bot Token？旧机器人会停止轮询。")
		return
	case "addadmin":
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			m.reply(ctx, b, userID, "用户 ID 格式错误")
			return
		}
		if cfg.IsOwner(id) {
			m.reply(ctx, b, userID, "该用户是超级管理员，无需重复添加")
			return
		}
		admins := cfg.AdminUserIDs
		admins[id] = struct{}{}
		updates["ADMIN_USER_IDS"] = config.FormatIDs(admins)
	case "setstatusnotify":
		minutes, err := config.StatusNotifyHoursToMinutes(value)
		if err != nil {
			m.reply(ctx, b, userID, "间隔格式错误：请输入小时数，例如 0.5、1、2；0 表示关闭")
			return
		}
		updates["STATUS_NOTIFY_INTERVAL_MINUTES"] = strconv.Itoa(minutes)
	}
	if err := m.apply(ctx, updates, false); err != nil {
		m.reply(ctx, b, userID, "修改失败："+err.Error())
		return
	}
	success := "配置已更新并写入 .env"
	if state.action == "setstatusnotify" {
		success = "状态通知已更新为：" + config.FormatStatusNotifyInterval(m.cfg.Current().StatusNotifyIntervalMinutes)
	}
	m.reply(ctx, b, userID, success)
	m.sendPanel(ctx, b, userID)
}
func (m *Manager) requestConfigConfirm(ctx context.Context, b *bot.Bot, userID int64, updates map[string]string, reset bool, text string) {
	m.mu.Lock()
	m.confirmations[userID] = confirmation{updates: updates, reset: reset, expires: time.Now().Add(time.Minute)}
	m.mu.Unlock()
	m.sendConfirm(ctx, b, userID, text)
}
func (m *Manager) sendConfirm(ctx context.Context, b *bot.Bot, userID int64, text string) {
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "确认", CallbackData: "fn:confirm"}, {Text: "取消", CallbackData: "fn:cancel"}}}}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: userID, Text: text, ReplyMarkup: keyboard})
}
func (m *Manager) confirm(ctx context.Context, b *bot.Bot, userID int64) {
	m.mu.Lock()
	confirmation, ok := m.confirmations[userID]
	delete(m.confirmations, userID)
	m.mu.Unlock()
	if !ok || time.Now().After(confirmation.expires) {
		m.reply(ctx, b, userID, "确认操作已过期")
		return
	}
	if confirmation.clearLogs {
		if err := m.logs.Clear(); err != nil {
			m.reply(ctx, b, userID, "清理失败："+err.Error())
			return
		}
		m.logger.Info("logs cleared", "operator_id", userID)
		m.reply(ctx, b, userID, "日志已清理")
		return
	}
	if err := m.apply(ctx, confirmation.updates, confirmation.reset); err != nil {
		m.reply(ctx, b, userID, "修改失败："+err.Error())
		return
	}
	m.reply(ctx, b, userID, "配置已更新并生效")
}
func (m *Manager) apply(ctx context.Context, updates map[string]string, reset bool) error {
	m.mu.Lock()
	controller := m.controller
	m.mu.Unlock()
	if controller == nil {
		return fmt.Errorf("运行控制器尚未就绪")
	}
	return controller.Update(ctx, updates, reset)
}
func (m *Manager) Reload(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !m.ownerPrivate(update.Message) {
		return
	}
	m.reload(ctx, b, update.Message.From.ID)
}
func (m *Manager) reload(ctx context.Context, b *bot.Bot, userID int64) {
	m.mu.Lock()
	controller := m.controller
	m.mu.Unlock()
	if controller == nil {
		m.reply(ctx, b, userID, "运行控制器尚未就绪")
		return
	}
	if err := controller.Reload(ctx); err != nil {
		m.reply(ctx, b, userID, "重载失败："+err.Error())
		return
	}
	m.reply(ctx, b, userID, "配置已重新加载")
}
func (m *Manager) Cancel(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !m.ownerPrivate(update.Message) {
		return
	}
	m.cancel(ctx, b, update.Message.From.ID)
}

func (m *Manager) cancel(ctx context.Context, b *bot.Bot, userID int64) {
	m.clearOperation(userID)
	m.reply(ctx, b, userID, "操作已取消")
	m.sendPanel(ctx, b, userID)
}

func (m *Manager) clearOperation(userID int64) {
	m.mu.Lock()
	delete(m.pending, userID)
	delete(m.confirmations, userID)
	m.mu.Unlock()
}
func (m *Manager) reply(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text})
}
func tokenTail(token string) string {
	if len(token) <= 4 {
		return "****"
	}
	return token[len(token)-4:]
}
func escape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}
