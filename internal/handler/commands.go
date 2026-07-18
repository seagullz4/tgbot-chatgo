package handler

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/service"
)

const (
	commandHelp   = "help"
	commandStatus = "status"
	commandID     = "id"
	commandInfo   = "info"
	commandClose  = "close"
	commandOpen   = "open"
	commandBan    = "ban"
	commandUnban  = "unban"
	commandBanned = "banned"
	commandSay    = "say"
)

func (h *Handlers) Help(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil {
		return
	}
	if message.Chat.ID == h.Svc.Cfg.Snapshot().AdminGroupID {
		if !h.requireAdmin(ctx, b, message) {
			return
		}
		text := "<b>管理员命令</b>\n/info [用户ID] — 查看用户信息\n/banned — 查看封禁列表\n/close — 关闭当前会话\n/open — 重新打开当前会话\n/ban [原因] — 封禁当前用户（管理员不可封禁）\n/unban [用户ID] — 解除封禁\n/clear — 删除话题并清理会话\n/say 文本 — 快速回复用户\n\n关闭、重开、封禁、清理和回复必须在对应用户话题内执行。"
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, MessageThreadID: message.MessageThreadID, Text: text, ParseMode: models.ParseModeHTML}, "send admin help")
		return
	}
	if message.Chat.Type != models.ChatTypePrivate {
		return
	}
	if h.Svc.Cfg.IsAdmin(message.From.ID) {
		text := "<b>管理员私聊命令</b>\n/banned — 查看封禁用户列表\n/info &lt;用户ID&gt; — 查询用户详情\n/unban &lt;用户ID&gt; — 解除用户封禁\n/status — 检查管理员身份\n/id — 查看你的 Telegram ID"
		if h.Svc.Cfg.IsOwner(message.From.ID) {
			text += "\n/function — 打开运维设置\n/reload — 重载 .env\n/logs — 下载日志\n/clearlogs — 清理日志"
		}
		text += "\n\n关闭、重开、封禁、清理和回复等会话操作仅可在管理群执行。管理员账号不会作为普通用户建立会话，也不能被封禁。"
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: h.privateKeyboard(message.From.ID)}, "send admin private help")
		return
	}
	text := "<b>使用帮助</b>\n直接发送文字、图片、语音或文件即可联系客服。\n/status — 查看会话状态\n/id — 查看你的 Telegram ID\n\n会话只能由管理员关闭或重新打开，用户不能主动结束或取消对话。"
	h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: service.UserKeyboard()}, "send user help")
}

func (h *Handlers) Status(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	status, err := h.Svc.UserStatus(message.From.ID)
	if err != nil {
		status = "状态查询失败，请稍后重试。"
	}
	h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, Text: status, ReplyMarkup: h.privateKeyboard(message.From.ID)}, "send status")
}

func (h *Handlers) ID(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, Text: fmt.Sprintf("你的 Telegram ID：<code>%d</code>", message.From.ID), ParseMode: models.ParseModeHTML, ReplyMarkup: h.privateKeyboard(message.From.ID)}, "send id")
}

func (h *Handlers) Info(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminAccess(ctx, b, message) {
		return
	}
	userID, err := h.resolveTargetUserID(message, true)
	if err != nil {
		h.adminReply(ctx, b, message, err.Error())
		return
	}
	info, err := h.Svc.UserInfo(userID)
	if err != nil {
		h.adminReply(ctx, b, message, "查询失败："+err.Error())
		return
	}
	h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, MessageThreadID: message.MessageThreadID, Text: info, ParseMode: models.ParseModeHTML}, "send info")
}

func (h *Handlers) Close(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminTopic(ctx, b, message) {
		return
	}
	h.adminReply(ctx, b, message, "正在关闭会话…")
	if err := h.Svc.CloseTopic(ctx, b, message.MessageThreadID, message.From.ID); err != nil {
		h.adminReply(ctx, b, message, "关闭失败："+err.Error())
	}
}

func (h *Handlers) Open(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminTopic(ctx, b, message) {
		return
	}
	if err := h.Svc.OpenTopic(ctx, b, message.MessageThreadID, message.From.ID); err != nil {
		h.adminReply(ctx, b, message, "重开失败："+err.Error())
		return
	}
	h.adminReply(ctx, b, message, "✅ 会话已重新打开")
}

func (h *Handlers) Ban(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminTopic(ctx, b, message) {
		return
	}
	user, err := h.Svc.Store.GetUserByThreadID(message.MessageThreadID)
	if err != nil || user == nil {
		h.adminReply(ctx, b, message, "未找到话题对应用户")
		return
	}
	if h.Svc.Cfg.IsAdmin(user.UserID) {
		h.adminReply(ctx, b, message, "不能封禁管理员账号")
		return
	}
	h.adminReply(ctx, b, message, "正在封禁用户…")
	if err := h.Svc.BanUser(ctx, b, user.UserID, commandArguments(message.Text), message.From.ID); err != nil {
		h.adminReply(ctx, b, message, "封禁失败："+err.Error())
		return
	}
	h.adminReply(ctx, b, message, fmt.Sprintf("✅ 已封禁用户 %d。可用 /banned 查看列表，/unban %d 解封。", user.UserID, user.UserID))
}

func (h *Handlers) Unban(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminAccess(ctx, b, message) {
		return
	}
	args := strings.Fields(commandArguments(message.Text))
	if len(args) == 0 && message.MessageThreadID == 0 {
		text, err := h.Svc.ListBannedUsersText()
		if err != nil {
			h.adminReply(ctx, b, message, "读取封禁列表失败："+err.Error())
			return
		}
		h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, MessageThreadID: message.MessageThreadID, Text: "未指定用户。\n用法：/unban &lt;用户ID&gt;\n或在用户话题内直接 /unban\n\n" + text, ParseMode: models.ParseModeHTML}, "send unban usage")
		return
	}
	userID, err := h.resolveTargetUserID(message, true)
	if err != nil {
		h.adminReply(ctx, b, message, err.Error()+"。也可先使用 /banned 查看封禁列表。")
		return
	}
	if err := h.Svc.UnbanUser(ctx, b, userID, message.From.ID); err != nil {
		h.adminReply(ctx, b, message, "解封失败："+err.Error())
		return
	}
	h.adminReply(ctx, b, message, fmt.Sprintf("✅ 用户 %d 已解除封禁。如需继续对话，请在对应话题执行 /open。", userID))
}

func (h *Handlers) Banned(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminAccess(ctx, b, message) {
		return
	}
	text, err := h.Svc.ListBannedUsersText()
	if err != nil {
		h.adminReply(ctx, b, message, "读取封禁列表失败："+err.Error())
		return
	}
	h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, MessageThreadID: message.MessageThreadID, Text: text, ParseMode: models.ParseModeHTML}, "send banned list")
}

func (h *Handlers) Say(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if !h.validAdminTopic(ctx, b, message) {
		return
	}
	if err := h.Svc.SayToUser(ctx, b, message.MessageThreadID, message.ID, commandArguments(message.Text), message.From.ID); err != nil {
		h.adminReply(ctx, b, message, "发送失败："+err.Error())
		return
	}
	h.adminReply(ctx, b, message, "✅ 已发送给用户")
}

func (h *Handlers) AdminCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	if !h.Svc.Cfg.IsAdmin(query.From.ID) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: "你没有权限执行此操作", ShowAlert: true})
		return
	}
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 {
		return
	}
	userID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	user, err := h.Svc.Store.GetUserByTelegramID(userID)
	if err != nil || user == nil {
		h.answerAdminAction(ctx, b, query.ID, fmt.Errorf("用户不存在"), "")
		return
	}
	switch parts[1] {
	case "info":
		info, infoErr := h.Svc.UserInfo(userID)
		if infoErr != nil {
			h.answerAdminAction(ctx, b, query.ID, infoErr, "")
			return
		}
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: h.Svc.Cfg.Snapshot().AdminGroupID, MessageThreadID: user.MessageThreadID, Text: info, ParseMode: models.ParseModeHTML})
		h.answerAdminAction(ctx, b, query.ID, nil, "已发送用户信息")
	case "close":
		if user.MessageThreadID == 0 {
			h.answerAdminAction(ctx, b, query.ID, fmt.Errorf("用户没有活跃话题"), "")
			return
		}
		h.answerAdminAction(ctx, b, query.ID, h.Svc.CloseTopic(ctx, b, user.MessageThreadID, query.From.ID), "会话已关闭")
	case "ban":
		h.answerAdminAction(ctx, b, query.ID, h.Svc.BanUser(ctx, b, userID, "管理员操作", query.From.ID), "用户已封禁")
	}
}

func (h *Handlers) handleUserShortcut(ctx context.Context, b *bot.Bot, message *models.Message) bool {
	if message == nil || message.From == nil {
		return false
	}
	text := strings.TrimSpace(message.Text)
	if h.Svc.Cfg.IsAdmin(message.From.ID) {
		switch text {
		case service.ShortcutBanned:
			h.Banned(ctx, b, &models.Update{Message: message})
		case service.ShortcutAdminStatus:
			h.Status(ctx, b, &models.Update{Message: message})
		case service.ShortcutAdminHelp:
			h.Help(ctx, b, &models.Update{Message: message})
		case service.ShortcutID:
			h.ID(ctx, b, &models.Update{Message: message})
		default:
			return false
		}
		return true
	}
	switch text {
	case service.ShortcutStatus:
		h.Status(ctx, b, &models.Update{Message: message})
	case service.ShortcutID:
		h.ID(ctx, b, &models.Update{Message: message})
	case service.ShortcutHelp:
		h.Help(ctx, b, &models.Update{Message: message})
	default:
		return false
	}
	return true
}

func (h *Handlers) resolveTargetUserID(message *models.Message, allowArgument bool) (int64, error) {
	if allowArgument {
		fields := strings.Fields(commandArguments(message.Text))
		if len(fields) > 0 {
			userID, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("用户 ID 格式错误")
			}
			return userID, nil
		}
	}
	if message.MessageThreadID == 0 {
		return 0, fmt.Errorf("请在用户话题内使用，或提供用户 ID")
	}
	user, err := h.Svc.Store.GetUserByThreadID(message.MessageThreadID)
	if err != nil {
		return 0, err
	}
	if user == nil {
		return 0, fmt.Errorf("未找到话题对应用户")
	}
	return user.UserID, nil
}

func (h *Handlers) privateKeyboard(userID int64) *models.ReplyKeyboardMarkup {
	if h.Svc.Cfg.IsOwner(userID) {
		return service.OwnerKeyboard()
	}
	if h.Svc.Cfg.IsAdmin(userID) {
		return service.AdminKeyboard()
	}
	return service.UserKeyboard()
}

func (h *Handlers) unknownPrivateCommandText(userID int64) string {
	if h.Svc.Cfg.IsAdmin(userID) {
		return "未知管理员命令。私聊可使用 /help、/banned、/info <用户ID>、/unban <用户ID>、/status、/id。"
	}
	return "未知命令。可使用 /help、/status、/id，或直接发送消息联系客服。"
}

func (h *Handlers) validAdminAccess(ctx context.Context, b *bot.Bot, message *models.Message) bool {
	if message == nil || message.From == nil {
		return false
	}
	if message.Chat.ID != h.Svc.Cfg.Snapshot().AdminGroupID && message.Chat.Type != models.ChatTypePrivate {
		return false
	}
	return h.requireAdmin(ctx, b, message)
}

func (h *Handlers) validAdminMessage(ctx context.Context, b *bot.Bot, message *models.Message) bool {
	if message == nil || message.From == nil {
		return false
	}
	if message.Chat.ID != h.Svc.Cfg.Snapshot().AdminGroupID {
		if message.Chat.Type == models.ChatTypePrivate {
			if h.Svc.Cfg.IsAdmin(message.From.ID) {
				h.sendMessage(ctx, b, &bot.SendMessageParams{
					ChatID:      message.Chat.ID,
					Text:        "此命令只能在管理群中使用；涉及用户会话时，请进入对应用户话题操作。",
					ReplyMarkup: h.privateKeyboard(message.From.ID),
				}, "redirect admin group command")
			} else {
				h.sendMessage(ctx, b, &bot.SendMessageParams{
					ChatID:      message.Chat.ID,
					Text:        "该命令仅供管理员使用。你可以使用 /help、/status、/id，或直接发送消息联系客服。",
					ReplyMarkup: service.UserKeyboard(),
				}, "reject user admin command")
			}
		}
		return false
	}
	return h.requireAdmin(ctx, b, message)
}

func (h *Handlers) validAdminTopic(ctx context.Context, b *bot.Bot, message *models.Message) bool {
	if !h.validAdminMessage(ctx, b, message) {
		return false
	}
	if message.MessageThreadID == 0 {
		h.adminReply(ctx, b, message, "请在对应的用户话题内使用此命令")
		return false
	}
	return true
}

func (h *Handlers) requireAdmin(ctx context.Context, b *bot.Bot, message *models.Message) bool {
	if h.Svc.Cfg.IsAdmin(message.From.ID) {
		return true
	}
	h.adminReply(ctx, b, message, "你没有权限执行此操作。")
	return false
}

func (h *Handlers) adminReply(ctx context.Context, b *bot.Bot, message *models.Message, text string) {
	h.sendMessage(ctx, b, &bot.SendMessageParams{ChatID: message.Chat.ID, MessageThreadID: message.MessageThreadID, Text: text}, "send admin reply")
}

func (h *Handlers) answerAdminAction(ctx context.Context, b *bot.Bot, callbackID string, err error, success string) {
	if err != nil {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: err.Error(), ShowAlert: true})
		return
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: success})
}

func commandArguments(text string) string {
	fields := strings.SplitN(strings.TrimSpace(text), " ", 2)
	if len(fields) < 2 {
		return ""
	}
	return strings.TrimSpace(fields[1])
}

func isForumLifecycleEvent(message *models.Message) bool {
	return message.ForumTopicCreated != nil || message.ForumTopicClosed != nil || message.ForumTopicReopened != nil
}

func hasForwardableContent(message *models.Message) bool {
	return message.Text != "" || message.Caption != "" || message.Animation != nil || message.Audio != nil || message.Document != nil || len(message.Photo) > 0 || message.Sticker != nil || message.Video != nil || message.VideoNote != nil || message.Voice != nil || message.Contact != nil || message.Dice != nil || message.Game != nil || message.Poll != nil || message.Venue != nil || message.Location != nil
}
