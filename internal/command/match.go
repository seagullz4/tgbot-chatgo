package command

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

const ShortcutFunctionManagement = "功能管理"

// Matches reports whether an update starts with a bare command or a command addressed to this bot.
func Matches(update *models.Update, name, botUsername string) bool {
	if update == nil || update.Message == nil {
		return false
	}
	fields := strings.Fields(update.Message.Text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	commandName, target, targeted := strings.Cut(strings.TrimPrefix(fields[0], "/"), "@")
	if !strings.EqualFold(commandName, name) {
		return false
	}
	if !targeted {
		return true
	}
	botUsername = strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	return botUsername != "" && strings.EqualFold(target, botUsername)
}
