package function

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
)

const adminPageSize = 8

func (m *Manager) handleAdminCallback(ctx context.Context, b *bot.Bot, userID int64, data string) bool {
	parts := strings.Split(data, ":")
	switch parts[0] {
	case "admins":
		m.sendAdminPage(ctx, b, userID, callbackPage(parts), false)
		return true
	case "deladmins":
		m.sendAdminPage(ctx, b, userID, callbackPage(parts), true)
		return true
	case "deladmin":
		if len(parts) != 3 {
			m.reply(ctx, b, userID, "删除管理员操作无效")
			m.sendAdminPage(ctx, b, userID, 0, true)
			return true
		}
		adminID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			m.reply(ctx, b, userID, "管理员 ID 格式错误")
			m.sendAdminPage(ctx, b, userID, 0, true)
			return true
		}
		page, _ := strconv.Atoi(parts[2])
		m.deleteAdmin(ctx, b, userID, adminID, page)
		return true
	default:
		return false
	}
}

func callbackPage(parts []string) int {
	if len(parts) < 2 {
		return 0
	}
	page, err := strconv.Atoi(parts[1])
	if err != nil || page < 0 {
		return 0
	}
	return page
}

func (m *Manager) sendAdminPage(ctx context.Context, b *bot.Bot, userID int64, page int, deleteMode bool) {
	cfg := m.cfg.Current()
	admins := sortedAdminIDs(cfg.AdminUserIDs)
	ownerID := firstID(cfg.OwnerUserIDs)
	page, start, end, pageCount := adminPageBounds(page, len(admins))

	var text string
	if deleteMode {
		if len(admins) == 0 {
			text = "<b>删除普通管理员</b>\n\n当前没有普通管理员。"
		} else {
			text = fmt.Sprintf("<b>删除普通管理员</b>\n\n点击需要删除的管理员。\n第 %d/%d 页", page+1, pageCount)
		}
	} else {
		text = fmt.Sprintf("<b>管理员列表</b>\n\n超级管理员：<code>%d</code>\n", ownerID)
		if len(admins) == 0 {
			text += "\n当前没有普通管理员。"
		} else {
			text += fmt.Sprintf("\n普通管理员（%d）：\n", len(admins))
			for _, adminID := range admins[start:end] {
				text += fmt.Sprintf("• <code>%d</code>\n", adminID)
			}
			text += fmt.Sprintf("\n第 %d/%d 页", page+1, pageCount)
		}
	}

	rows := make([][]models.InlineKeyboardButton, 0, adminPageSize+3)
	if deleteMode {
		for _, adminID := range admins[start:end] {
			rows = append(rows, []models.InlineKeyboardButton{{
				Text:         fmt.Sprintf("删除 %d", adminID),
				CallbackData: fmt.Sprintf("fn:deladmin:%d:%d", adminID, page),
			}})
		}
	}
	if len(admins) > 0 {
		prefix := "admins"
		if deleteMode {
			prefix = "deladmins"
		}
		if navigation := adminNavigation(prefix, page, pageCount); len(navigation) > 0 {
			rows = append(rows, navigation)
		}
	}
	if len(admins) == 0 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: "返回主页", CallbackData: "fn:home"}})
	} else if deleteMode {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "查询管理员", CallbackData: fmt.Sprintf("fn:admins:%d", page)},
			{Text: "返回主页", CallbackData: "fn:home"},
		})
	} else {
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "删除管理员", CallbackData: fmt.Sprintf("fn:deladmins:%d", page)},
			{Text: "返回主页", CallbackData: "fn:home"},
		})
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      userID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: rows},
	})
}

func (m *Manager) deleteAdmin(ctx context.Context, b *bot.Bot, userID, adminID int64, page int) {
	cfg := m.cfg.Current()
	if _, exists := cfg.AdminUserIDs[adminID]; !exists {
		m.reply(ctx, b, userID, "该普通管理员不存在或已被删除")
		m.sendAdminPage(ctx, b, userID, page, true)
		return
	}
	delete(cfg.AdminUserIDs, adminID)
	if err := m.apply(ctx, map[string]string{"ADMIN_USER_IDS": config.FormatIDs(cfg.AdminUserIDs)}, false); err != nil {
		m.reply(ctx, b, userID, "删除管理员失败："+err.Error())
		return
	}
	m.reply(ctx, b, userID, fmt.Sprintf("已删除普通管理员 %d", adminID))
	m.sendAdminPage(ctx, b, userID, page, true)
}

func sortedAdminIDs(ids map[int64]struct{}) []int64 {
	values := make([]int64, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return values
}

func firstID(ids map[int64]struct{}) int64 {
	for id := range ids {
		return id
	}
	return 0
}

func adminPageBounds(page, total int) (current, start, end, pageCount int) {
	if total == 0 {
		return 0, 0, 0, 0
	}
	pageCount = (total + adminPageSize - 1) / adminPageSize
	if page < 0 {
		page = 0
	}
	if page >= pageCount {
		page = pageCount - 1
	}
	start = page * adminPageSize
	end = start + adminPageSize
	if end > total {
		end = total
	}
	return page, start, end, pageCount
}

func adminNavigation(prefix string, page, pageCount int) []models.InlineKeyboardButton {
	buttons := make([]models.InlineKeyboardButton, 0, 2)
	if page > 0 {
		buttons = append(buttons, models.InlineKeyboardButton{Text: "上一页", CallbackData: fmt.Sprintf("fn:%s:%d", prefix, page-1)})
	}
	if page+1 < pageCount {
		buttons = append(buttons, models.InlineKeyboardButton{Text: "下一页", CallbackData: fmt.Sprintf("fn:%s:%d", prefix, page+1)})
	}
	return buttons
}
