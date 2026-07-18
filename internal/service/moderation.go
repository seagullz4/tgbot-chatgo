package service

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot"

	"telegram-interactive-bot/go-bot/internal/model"
)

func (s *Services) UserStatus(userID int64) (string, error) {
	if s.Cfg.IsAdmin(userID) {
		return "当前身份：管理员（不受封禁与普通用户限制）", nil
	}
	user, err := s.Store.GetUserByTelegramID(userID)
	if err != nil {
		return "", err
	}
	if user == nil {
		return "尚未建立客服会话，可以直接发送消息联系客服。", nil
	}
	if user.IsBanned {
		reason := strings.TrimSpace(user.BanReason)
		if reason == "" {
			reason = "管理员限制"
		}
		return "当前状态：已被限制联系\n原因：" + reason, nil
	}
	verification, err := s.Store.GetVerificationState(userID)
	if err != nil {
		return "", err
	}
	if !s.Cfg.Snapshot().DisableVerification && (verification == nil || !verification.IsHuman) {
		return "当前状态：等待完成安全验证。发送消息后按提示完成验证即可。", nil
	}
	if user.MessageThreadID > 0 {
		status, err := s.Store.GetForumStatus(user.MessageThreadID)
		if err != nil {
			return "", err
		}
		if status != nil && status.Status == "closed" {
			return "当前状态：客服已关闭对话，暂时无法继续留言。", nil
		}
		return "当前状态：会话正常，可以直接发送消息联系客服。", nil
	}
	return "当前状态：可以联系。发送第一条消息后将自动建立客服会话。", nil
}

func displayUserLabel(user *model.User) string {
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name == "" {
		name = user.Username
	}
	if name == "" {
		name = fmt.Sprintf("%d", user.UserID)
	}
	return name
}

func (s *Services) UserInfo(userID int64) (string, error) {
	user, err := s.Store.GetUserByTelegramID(userID)
	if err != nil {
		return "", err
	}
	if user == nil {
		if s.Cfg.IsAdmin(userID) {
			return fmt.Sprintf("👤 <b>用户信息</b>\nID：<code>%d</code>\n角色：管理员\n说明：该账号在管理员列表中，系统不会封禁管理员。", userID), nil
		}
		return "", fmt.Errorf("未找到用户 %d", userID)
	}
	status := "未建立"
	if user.MessageThreadID > 0 {
		status = "已打开"
		forumStatus, statusErr := s.Store.GetForumStatus(user.MessageThreadID)
		if statusErr != nil {
			return "", statusErr
		}
		if forumStatus != nil {
			switch forumStatus.Status {
			case "closed":
				status = "已关闭"
			case "opened":
				status = "已打开"
			default:
				status = "状态未知"
			}
		}
	}
	verificationStatus := "已关闭"
	if !s.Cfg.Snapshot().DisableVerification {
		verificationStatus = "未验证"
		verification, verificationErr := s.Store.GetVerificationState(userID)
		if verificationErr != nil {
			return "", verificationErr
		}
		if verification != nil && verification.IsHuman {
			verificationStatus = "已验证"
		}
	}
	count, err := s.Store.CountMessageMapsByUser(userID)
	if err != nil {
		return "", err
	}
	username := "未设置"
	if user.Username != "" {
		username = "@" + html.EscapeString(user.Username)
	}
	role := "普通用户"
	banned := "否"
	if s.Cfg.IsAdmin(userID) {
		role = "管理员（不可封禁）"
		banned = "否（管理员不受封禁限制）"
	} else if user.IsBanned {
		banned = "是"
		if user.BanReason != "" {
			banned += "（" + html.EscapeString(user.BanReason) + "）"
		}
	}
	updated := "未知"
	if !user.UpdatedAt.IsZero() {
		updated = user.UpdatedAt.Local().Format("2006-01-02 15:04:05")
	}
	return fmt.Sprintf("👤 <b>用户信息</b>\n名称：%s\nID：<code>%d</code>\n用户名：%s\n角色：%s\n话题 ID：<code>%d</code>\n会话状态：%s\n验证状态：%s\n已封禁：%s\n消息映射：%d\n更新时间：%s",
		html.EscapeString(displayUserLabel(user)), user.UserID, username, role, user.MessageThreadID, status, verificationStatus, banned, count, updated), nil
}

func (s *Services) ListBannedUsersText() (string, error) {
	users, err := s.Store.ListBannedUsers()
	if err != nil {
		return "", err
	}
	bannedUsers := make([]*model.User, 0, len(users))
	for _, user := range users {
		if user != nil && !s.Cfg.IsAdmin(user.UserID) {
			bannedUsers = append(bannedUsers, user)
		}
	}
	if len(bannedUsers) == 0 {
		return "当前没有被封禁的用户。", nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("<b>被封禁用户（%d）</b>\n", len(bannedUsers)))
	limit := len(bannedUsers)
	if limit > 30 {
		limit = 30
	}
	for index := 0; index < limit; index++ {
		user := bannedUsers[index]
		username := "无用户名"
		if user.Username != "" {
			username = "@" + html.EscapeString(user.Username)
		}
		reason := strings.TrimSpace(user.BanReason)
		if reason == "" {
			reason = "未填写"
		}
		builder.WriteString(fmt.Sprintf("\n%d. %s（%s）\nID：<code>%d</code>\n原因：%s\n解封：/unban %d\n",
			index+1,
			html.EscapeString(displayUserLabel(user)),
			username,
			user.UserID,
			html.EscapeString(reason),
			user.UserID,
		))
	}
	if len(bannedUsers) > limit {
		builder.WriteString(fmt.Sprintf("\n…其余 %d 人请继续使用 /unban &lt;用户ID&gt; 解封。", len(bannedUsers)-limit))
	}
	return builder.String(), nil
}

func (s *Services) CloseTopic(ctx context.Context, b *bot.Bot, threadID int, operatorID int64) error {
	if !s.Cfg.IsAdmin(operatorID) {
		return fmt.Errorf("没有权限执行此操作")
	}
	user, err := s.Store.GetUserByThreadID(threadID)
	if err != nil || user == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("未找到话题对应用户")
	}
	if s.Cfg.IsAdmin(user.UserID) {
		return fmt.Errorf("不能对管理员账号执行会话关闭操作")
	}
	if _, err := b.CloseForumTopic(ctx, &bot.CloseForumTopicParams{ChatID: s.Cfg.Snapshot().AdminGroupID, MessageThreadID: threadID}); err != nil {
		return err
	}
	if err := s.Store.UpsertForumStatus(&model.ForumStatus{ChatID: s.Cfg.Snapshot().AdminGroupID, MessageThreadID: threadID, Status: "closed"}); err != nil {
		return err
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: user.UserID, Text: "客服已关闭本次对话。如需继续联系，请等待管理员重新打开会话。"})
	return nil
}

func (s *Services) OpenTopic(ctx context.Context, b *bot.Bot, threadID int, operatorID int64) error {
	if !s.Cfg.IsAdmin(operatorID) {
		return fmt.Errorf("没有权限执行此操作")
	}
	user, err := s.Store.GetUserByThreadID(threadID)
	if err != nil || user == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("未找到话题对应用户")
	}
	if user.IsBanned {
		return fmt.Errorf("用户仍处于封禁状态，请先 /unban %d", user.UserID)
	}
	if _, err := b.ReopenForumTopic(ctx, &bot.ReopenForumTopicParams{ChatID: s.Cfg.Snapshot().AdminGroupID, MessageThreadID: threadID}); err != nil {
		return err
	}
	if err := s.Store.UpsertForumStatus(&model.ForumStatus{ChatID: s.Cfg.Snapshot().AdminGroupID, MessageThreadID: threadID, Status: "opened"}); err != nil {
		return err
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: user.UserID, Text: "客服已重新打开对话，你现在可以继续发送消息。"})
	return nil
}

func (s *Services) BanUser(ctx context.Context, b *bot.Bot, userID int64, reason string, operatorID int64) error {
	if !s.Cfg.IsAdmin(operatorID) {
		return fmt.Errorf("没有权限执行此操作")
	}
	if userID == operatorID {
		return fmt.Errorf("不能封禁自己")
	}
	if s.Cfg.IsAdmin(userID) {
		return fmt.Errorf("不能封禁管理员账号")
	}
	user, err := s.Store.GetUserByTelegramID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return fmt.Errorf("未找到用户 %d", userID)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "管理员操作"
	}
	if user.IsBanned && strings.TrimSpace(user.BanReason) == reason {
		return fmt.Errorf("用户 %d 已处于封禁状态", userID)
	}
	if err := s.Store.SetUserBanned(userID, true, reason); err != nil {
		return err
	}
	if user.MessageThreadID > 0 {
		if _, closeErr := b.CloseForumTopic(ctx, &bot.CloseForumTopicParams{ChatID: s.Cfg.Snapshot().AdminGroupID, MessageThreadID: user.MessageThreadID}); closeErr != nil {
			s.Logger.Warn("ban closed topic failed", "err", closeErr, "user_id", userID, "thread_id", user.MessageThreadID)
		}
		if statusErr := s.Store.UpsertForumStatus(&model.ForumStatus{ChatID: s.Cfg.Snapshot().AdminGroupID, MessageThreadID: user.MessageThreadID, Status: "closed"}); statusErr != nil {
			return statusErr
		}
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: userID, Text: "你已被限制联系本客服。\n原因：" + reason})
	return nil
}

func (s *Services) UnbanUser(ctx context.Context, b *bot.Bot, userID int64, operatorID int64) error {
	if !s.Cfg.IsAdmin(operatorID) {
		return fmt.Errorf("没有权限执行此操作")
	}
	if s.Cfg.IsAdmin(userID) {
		return fmt.Errorf("管理员账号无需解封")
	}
	user, err := s.Store.GetUserByTelegramID(userID)
	if err != nil {
		return err
	}
	if user == nil {
		return fmt.Errorf("未找到用户 %d", userID)
	}
	if !user.IsBanned {
		return fmt.Errorf("用户 %d 当前未被封禁", userID)
	}
	if err := s.Store.SetUserBanned(userID, false, ""); err != nil {
		return err
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: userID, Text: "联系限制已解除。若会话仍处于关闭状态，请等待管理员重新打开。"})
	return nil
}

func (s *Services) SayToUser(ctx context.Context, b *bot.Bot, threadID int, groupMessageID int, text string, operatorID int64) error {
	if !s.Cfg.IsAdmin(operatorID) {
		return fmt.Errorf("没有权限执行此操作")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("用法：/say 要发送的内容")
	}
	user, err := s.Store.GetUserByThreadID(threadID)
	if err != nil || user == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("未找到话题对应用户")
	}
	if s.Cfg.IsAdmin(user.UserID) {
		return fmt.Errorf("不能通过 /say 向管理员账号发送客服消息")
	}
	if user.IsBanned {
		return fmt.Errorf("用户已被封禁，请先 /unban %d", user.UserID)
	}
	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: user.UserID, Text: text})
	if err != nil {
		return err
	}
	return s.Store.SaveMessageMap(&model.MessageMap{UserChatMessageID: sent.ID, GroupChatMessageID: groupMessageID, UserID: user.UserID, MessageText: text})
}
