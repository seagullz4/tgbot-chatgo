package service

import (
	"context"
	"fmt"

	"telegram-interactive-bot/go-bot/internal/command"
	"telegram-interactive-bot/go-bot/internal/config"

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

func OwnerKeyboard() *models.ReplyKeyboardMarkup {
	keyboard := AdminKeyboard()
	keyboard.Keyboard = append(keyboard.Keyboard, []models.KeyboardButton{{Text: command.ShortcutFunctionManagement}})
	keyboard.InputFieldPlaceholder = "超级管理员快捷操作"
	return keyboard
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

func OwnerPrivateCommandMenu() []models.BotCommand {
	commands := append([]models.BotCommand{}, AdminPrivateCommandMenu()...)
	return append(commands,
		models.BotCommand{Command: "function", Description: "功能管理"},
		models.BotCommand{Command: "reload", Description: "重新加载 .env 配置"},
		models.BotCommand{Command: "logs", Description: "下载运行日志"},
		models.BotCommand{Command: "clearlogs", Description: "清理运行日志"},
		models.BotCommand{Command: "cancel", Description: "取消当前设置操作"},
	)
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
		{Command: "say", Description: "快速发送文本给用户"},
	}
}

func (s *Services) RegisterCommandMenus(ctx context.Context, b *bot.Bot) {
	s.RefreshCommandMenus(ctx, b, config.Config{}, s.Cfg.Current())
}

func (s *Services) RefreshCommandMenus(ctx context.Context, b *bot.Bot, previous, current config.Config) {
	if _, err := b.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: &models.BotCommandScopeDefault{}}); err != nil {
		s.Logger.Warn("clear legacy default command menu failed", "err", err)
	}
	if previous.AdminGroupID != 0 && previous.AdminGroupID != current.AdminGroupID {
		_, _ = b.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: &models.BotCommandScopeChat{ChatID: previous.AdminGroupID}})
	}
	oldUsers := make(map[int64]struct{})
	for id := range previous.AdminUserIDs {
		oldUsers[id] = struct{}{}
	}
	for id := range previous.OwnerUserIDs {
		oldUsers[id] = struct{}{}
	}
	for id := range oldUsers {
		if !current.IsAdmin(id) {
			_, _ = b.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: &models.BotCommandScopeChat{ChatID: id}})
		}
	}
	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: UserCommandMenu(), Scope: &models.BotCommandScopeAllPrivateChats{}}); err != nil {
		s.Logger.Warn("set user private command menu failed", "err", err)
	}
	for adminID := range current.AdminUserIDs {
		commands := AdminPrivateCommandMenu()
		if current.IsOwner(adminID) {
			commands = OwnerPrivateCommandMenu()
		}
		if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: commands, Scope: &models.BotCommandScopeChat{ChatID: adminID}}); err != nil {
			s.Logger.Warn("set admin private command menu failed", "err", err, "admin_id", adminID)
		}
	}
	for ownerID := range current.OwnerUserIDs {
		if _, duplicate := current.AdminUserIDs[ownerID]; duplicate {
			continue
		}
		if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: OwnerPrivateCommandMenu(), Scope: &models.BotCommandScopeChat{ChatID: ownerID}}); err != nil {
			s.Logger.Warn("set owner private command menu failed", "err", err, "owner_id", ownerID)
		}
	}
	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: AdminGroupCommandMenu(), Scope: &models.BotCommandScopeChat{ChatID: current.AdminGroupID}}); err != nil {
		s.Logger.Warn("set admin group command menu failed", "err", err)
	}
}
