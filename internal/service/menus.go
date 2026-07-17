package service

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const (
	ShortcutStatus      = "状态"
	ShortcutID          = "我的ID"
	ShortcutHelp        = "帮助"
	ShortcutBanned      = "封禁列表"
	ShortcutAdminHelp   = "管理帮助"
	ShortcutAdminStatus = "管理员状态"
)

func UserKeyboard() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: ShortcutStatus}, {Text: ShortcutID}},
			{{Text: ShortcutHelp}},
		},
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: "发送消息联系客服",
	}
}

func AdminKeyboard() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: ShortcutBanned}, {Text: ShortcutAdminStatus}},
			{{Text: ShortcutAdminHelp}, {Text: ShortcutID}},
		},
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: "管理员快捷操作",
	}
}

func AdminContactKeyboard(userID int64) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "👤 直接联络", URL: fmt.Sprintf("tg://user?id=%d", userID)}},
		{
			{Text: "ℹ️ 用户信息", CallbackData: fmt.Sprintf("adm:info:%d", userID)},
			{Text: "⏸ 关闭会话", CallbackData: fmt.Sprintf("adm:close:%d", userID)},
		},
		{{Text: "🚫 封禁用户", CallbackData: fmt.Sprintf("adm:ban:%d", userID)}},
	}}
}

func UserCommandMenu() []models.BotCommand {
	return []models.BotCommand{
		{Command: "start", Description: "开始使用客服机器人"},
		{Command: "help", Description: "查看使用帮助"},
		{Command: "status", Description: "查看当前会话状态"},
		{Command: "id", Description: "查看我的 Telegram ID"},
	}
}

func AdminPrivateCommandMenu() []models.BotCommand {
	return []models.BotCommand{
		{Command: "start", Description: "打开管理员面板"},
		{Command: "help", Description: "查看管理员命令说明"},
		{Command: "status", Description: "检查管理员身份"},
		{Command: "id", Description: "查看我的 Telegram ID"},
		{Command: "banned", Description: "查看封禁用户列表"},
		{Command: "info", Description: "按用户 ID 查询信息"},
		{Command: "unban", Description: "按用户 ID 解除封禁"},
	}
}

func AdminGroupCommandMenu() []models.BotCommand {
	return []models.BotCommand{
		{Command: "help", Description: "查看管理员命令"},
		{Command: "info", Description: "查看当前用户信息"},
		{Command: "banned", Description: "查看封禁用户列表"},
		{Command: "close", Description: "关闭当前会话"},
		{Command: "open", Description: "重新打开当前会话"},
		{Command: "ban", Description: "封禁当前用户"},
		{Command: "unban", Description: "解除用户封禁"},
		{Command: "clear", Description: "删除话题并清理会话"},
		{Command: "broadcast", Description: "回复消息后广播"},
		{Command: "say", Description: "快速发送文本给用户"},
	}
}

func (s *Services) RegisterCommandMenus(ctx context.Context, b *bot.Bot) {
	if _, err := b.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{
		Scope: &models.BotCommandScopeDefault{},
	}); err != nil {
		s.Logger.Warn("clear legacy default command menu failed", "err", err)
	}

	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: UserCommandMenu(),
		Scope:    &models.BotCommandScopeAllPrivateChats{},
	}); err != nil {
		s.Logger.Warn("set user private command menu failed", "err", err)
	}

	for adminID := range s.Cfg.AdminUserIDs {
		if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
			Commands: AdminPrivateCommandMenu(),
			Scope:    &models.BotCommandScopeChat{ChatID: adminID},
		}); err != nil {
			s.Logger.Warn("set admin private command menu failed", "err", err, "admin_id", adminID)
		}
	}

	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: AdminGroupCommandMenu(),
		Scope:    &models.BotCommandScopeChat{ChatID: s.Cfg.AdminGroupID},
	}); err != nil {
		s.Logger.Warn("set admin group command menu failed", "err", err)
	}
}
