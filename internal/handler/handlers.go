package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/command"
	"telegram-interactive-bot/go-bot/internal/service"
)

// Handlers binds Telegram updates to services.
const (
	commandStart = "start"
	commandClear = "clear"
)

type Handlers struct {
	Svc    *service.Services
	Logger *slog.Logger
}

func New(svc *service.Services, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{Svc: svc, Logger: logger}
}

// Register wires all go-telegram/bot handlers.
func (h *Handlers) Register(b *bot.Bot, botUsername string) {
	commands := map[string]bot.HandlerFunc{
		commandStart:  h.Start,
		commandHelp:   h.Help,
		commandStatus: h.Status,
		commandID:     h.ID,
		commandInfo:   h.Info,
		commandClose:  h.Close,
		commandOpen:   h.Open,
		commandBan:    h.Ban,
		commandUnban:  h.Unban,
		commandBanned: h.Banned,
		commandClear:  h.Clear,
		commandSay:    h.Say,
	}
	for commandName, handler := range commands {
		registeredCommand := commandName
		b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
			return command.Matches(update, registeredCommand, botUsername)
		}, handler)
	}
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "math_", bot.MatchTypePrefix, h.VerificationCallback)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "adm:", bot.MatchTypePrefix, h.AdminCallback)
}

// Default is used via bot.WithDefaultHandler for non-command messages (incl. media).
func (h *Handlers) Default(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.EditedMessage != nil {
		h.logMessage("edited", update.ID, update.EditedMessage)
		if err := h.Svc.EditMirroredMessage(ctx, b, update.EditedMessage); err != nil {
			h.Logger.Error("edit mirrored message", "err", err, "chat_id", update.EditedMessage.Chat.ID, "message_id", update.EditedMessage.ID)
		}
		return
	}
	if update.Message == nil {
		h.Logger.Debug("ignored update without message", "update_id", update.ID)
		return
	}
	msg := update.Message
	h.logMessage("default", update.ID, msg)

	if msg.Chat.Type == models.ChatTypePrivate && h.handleUserShortcut(ctx, b, msg) {
		return
	}
	if msg.Text != "" && strings.HasPrefix(msg.Text, "/") {
		if msg.Chat.Type == models.ChatTypePrivate && msg.From != nil {
			h.sendMessage(ctx, b, &bot.SendMessageParams{
				ChatID:      msg.Chat.ID,
				Text:        h.unknownPrivateCommandText(msg.From.ID),
				ReplyMarkup: h.privateKeyboard(msg.From.ID),
			}, "reply unknown command")
		}
		return
	}

	switch msg.Chat.Type {
	case models.ChatTypePrivate:
		h.userToAdmin(ctx, b, msg)
	case models.ChatTypeSupergroup, models.ChatTypeGroup:
		if msg.Chat.ID == h.Svc.Cfg.Snapshot().AdminGroupID && (hasForwardableContent(msg) || isForumLifecycleEvent(msg)) {
			h.adminToUser(ctx, b, msg)
		}
	default:
		h.Logger.Info("ignored unsupported chat type", "chat_id", msg.Chat.ID, "chat_type", msg.Chat.Type)
	}
}

func (h *Handlers) Start(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	msg := update.Message
	h.logMessage("start", update.ID, msg)
	if msg.Chat.Type != models.ChatTypePrivate {
		return
	}
	user := msg.From

	if h.Svc.Cfg.IsAdmin(user.ID) {
		chat, err := b.GetChat(ctx, &bot.GetChatParams{ChatID: h.Svc.Cfg.Snapshot().AdminGroupID})
		if err != nil {
			h.Logger.Error("admin group check failed", "err", err)
			h.sendMessage(ctx, b, &bot.SendMessageParams{
				ChatID: msg.Chat.ID,
				Text: fmt.Sprintf(
					"⚠️ 管理群组配置检查失败。请确认机器人已加入管理群并拥有管理话题权限。\n错误细节：%v",
					err,
				),
				ReplyMarkup: h.privateKeyboard(msg.From.ID),
			}, "send admin configuration error")
			return
		}
		h.sendMessage(ctx, b, &bot.SendMessageParams{
			ChatID: msg.Chat.ID,
			Text: fmt.Sprintf(
				"你好，管理员 %s（<code>%d</code>）。\n\n<b>%s 管理面板</b>\n私聊可用：/help、/status、/id、/banned、/info &lt;用户ID&gt;、/unban &lt;用户ID&gt;。\n关闭、重开、封禁、清理和回复等操作请在管理群 <b>%s</b> 的对应用户话题内执行。\n\n管理员账号不会进入普通用户会话，也不能被封禁。",
				escape(display(user)), user.ID, escape(h.Svc.Cfg.Snapshot().AppName), escape(chat.Title),
			),
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: h.privateKeyboard(msg.From.ID),
		}, "send admin welcome")
		return
	}

	if _, err := h.Svc.UpsertTelegramUser(user); err != nil {
		h.Logger.Error("ensure user", "err", err)
	}
	h.sendMessage(ctx, b, &bot.SendMessageParams{
		ChatID: msg.Chat.ID,
		Text: fmt.Sprintf(
			"<a href=\"tg://user?id=%d\">%s</a> 同学：\n\n%s",
			user.ID, escape(display(user)), h.Svc.Cfg.Snapshot().WelcomeMessage,
		),
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: service.UserKeyboard(),
	}, "send user welcome")
}

func (h *Handlers) VerificationCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	h.Logger.Info("received callback", "update_id", update.ID, "user_id", update.CallbackQuery.From.ID, "data", update.CallbackQuery.Data)
	if err := h.Svc.HandleVerificationCallback(ctx, b, update.CallbackQuery); err != nil {
		h.Logger.Error("verification callback", "err", err)
	}
}

func (h *Handlers) Clear(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminTopic(ctx, b, message) {
		return
	}
	h.adminReply(ctx, b, message, "正在清理会话…")
	if err := h.Svc.ClearTopic(ctx, b, message.Chat.ID, message.MessageThreadID, message.From.ID); err != nil {
		h.adminReply(ctx, b, message, "清理失败："+err.Error())
	}
}

func (h *Handlers) userToAdmin(ctx context.Context, b *bot.Bot, msg *models.Message) {
	if msg.From == nil {
		return
	}
	if h.Svc.Cfg.IsAdmin(msg.From.ID) {
		h.sendMessage(ctx, b, &bot.SendMessageParams{
			ChatID:      msg.Chat.ID,
			Text:        "这是管理员私聊面板。可使用 /banned 查看封禁列表，使用 /info <用户ID> 查询，使用 /unban <用户ID> 解封；会话管理请前往管理群话题。",
			ReplyMarkup: h.privateKeyboard(msg.From.ID),
		}, "admin private non-command")
		return
	}
	dbUser, err := h.Svc.UpsertTelegramUser(msg.From)
	if err != nil {
		h.Logger.Error("ensure user", "err", err)
		return
	}
	blockedMessage, err := h.Svc.UserConversationBlockMessage(dbUser)
	if err != nil {
		h.Logger.Error("check conversation state", "err", err, "user_id", msg.From.ID)
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: "会话状态检查失败，请稍后重试。"}, "notify conversation state failure")
		return
	}
	if blockedMessage != "" {
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: blockedMessage}, "notify blocked conversation")
		return
	}
	ok, err := h.Svc.CheckHuman(ctx, b, msg)
	if err != nil {
		h.Logger.Error("check human", "err", err)
		return
	}
	if !ok {
		return
	}
	ok, err = h.Svc.CheckRateLimit(ctx, b, msg)
	if err != nil {
		h.Logger.Error("rate limit", "err", err)
		return
	}
	if !ok {
		return
	}
	delivered, err := h.Svc.ForwardUserToAdmin(ctx, b, msg)
	if err != nil {
		h.Logger.Error("forward u2a", "err", err)
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: "消息处理失败，请稍后重试。"}, "notify user forward failure")
		return
	}
	if !delivered {
		return
	}
	if h.Svc.Cfg.Snapshot().UserForwardAck && msg.MediaGroupID == "" {
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: "✓ 已转达客服"}, "ack user forward")
	}
	h.Logger.Info("forward u2a completed", "chat_id", msg.Chat.ID, "message_id", msg.ID)
}

func (h *Handlers) adminToUser(ctx context.Context, b *bot.Bot, msg *models.Message) {
	if err := h.Svc.ForwardAdminToUser(ctx, b, msg); err != nil {
		h.Logger.Error("forward a2u", "err", err)
	}
}

func display(u *models.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = fmt.Sprintf("%d", u.ID)
	}
	return name
}

func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func (h *Handlers) logMessage(route string, updateID int64, msg *models.Message) {
	userID := int64(0)
	if msg.From != nil {
		userID = msg.From.ID
	}
	h.Logger.Info("received message",
		"route", route,
		"update_id", updateID,
		"chat_id", msg.Chat.ID,
		"chat_type", msg.Chat.Type,
		"user_id", userID,
		"message_id", msg.ID,
		"thread_id", msg.MessageThreadID,
		"has_text", msg.Text != "",
		"has_caption", msg.Caption != "",
		"media_group_id", msg.MediaGroupID,
	)
}

func (h *Handlers) sendMessage(ctx context.Context, b *bot.Bot, params *bot.SendMessageParams, operation string) {
	if _, err := b.SendMessage(ctx, params); err != nil {
		h.Logger.Error(operation, "err", err, "chat_id", params.ChatID)
	}
}
