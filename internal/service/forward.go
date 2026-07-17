package service

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/model"
	"telegram-interactive-bot/go-bot/internal/store"
)

// Services groups business logic used by handlers.
type Services struct {
	Cfg               *config.Config
	Store             store.Store
	Jobs              *job.Scheduler
	Logger            *slog.Logger
	topicLocks        sync.Map
	verificationLocks sync.Map
}

func New(cfg *config.Config, st store.Store, jobs *job.Scheduler, logger *slog.Logger) *Services {
	if logger == nil {
		logger = slog.Default()
	}
	return &Services{Cfg: cfg, Store: st, Jobs: jobs, Logger: logger}
}

func (s *Services) UpsertTelegramUser(u *models.User) (*model.User, error) {
	return s.Store.EnsureUser(&model.User{
		UserID:    u.ID,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		Username:  u.Username,
		IsPremium: u.IsPremium,
	})
}

// EnsureTopic creates a forum topic for the user when missing.
func (s *Services) EnsureTopic(ctx context.Context, b *bot.Bot, user *models.User, dbUser *model.User) (int, error) {
	if dbUser.MessageThreadID > 0 {
		return dbUser.MessageThreadID, nil
	}

	lockValue, _ := s.topicLocks.LoadOrStore(user.ID, &sync.Mutex{})
	topicLock := lockValue.(*sync.Mutex)
	topicLock.Lock()
	defer topicLock.Unlock()

	latestUser, err := s.Store.GetUserByTelegramID(user.ID)
	if err != nil {
		return 0, fmt.Errorf("reload user before topic creation: %w", err)
	}
	if latestUser != nil && latestUser.MessageThreadID > 0 {
		dbUser.MessageThreadID = latestUser.MessageThreadID
		return latestUser.MessageThreadID, nil
	}

	name := strings.TrimSpace(fmt.Sprintf("%s %s", user.FirstName, user.LastName))
	if name == "" {
		name = user.Username
	}
	if name == "" {
		name = fmt.Sprintf("user_%d", user.ID)
	}
	// Topic name length limit is 128.
	topicName := fmt.Sprintf("%s|%d", name, user.ID)
	if len([]rune(topicName)) > 128 {
		topicName = fmt.Sprintf("%d", user.ID)
	}

	topic, err := b.CreateForumTopic(ctx, &bot.CreateForumTopicParams{
		ChatID: s.Cfg.AdminGroupID,
		Name:   topicName,
	})
	if err != nil {
		return 0, fmt.Errorf("create forum topic: %w", err)
	}
	if err := s.Store.UpdateUserThreadID(user.ID, topic.MessageThreadID); err != nil {
		return 0, err
	}
	dbUser.MessageThreadID = topic.MessageThreadID

	mention := mentionHTML(user.ID, displayName(user))
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          s.Cfg.AdminGroupID,
		MessageThreadID: topic.MessageThreadID,
		Text:            fmt.Sprintf("新的用户 %s 开始了一个新的会话。", mention),
		ParseMode:       models.ParseModeHTML,
	})
	_ = s.SendContactCard(ctx, b, topic.MessageThreadID, user)
	_ = s.Store.UpsertForumStatus(&model.ForumStatus{
		ChatID:          s.Cfg.AdminGroupID,
		MessageThreadID: topic.MessageThreadID,
		Status:          "opened",
	})
	return topic.MessageThreadID, nil
}

func (s *Services) SendContactCard(ctx context.Context, b *bot.Bot, threadID int, user *models.User) error {
	contactURL := directContactURL(user)
	keyboard := AdminContactKeyboard(user.ID)
	keyboard.InlineKeyboard[0][0].URL = contactURL

	username := "未设置（使用用户 ID 联络）"
	if user.Username != "" {
		username = "@" + user.Username
	}
	caption := fmt.Sprintf("👤 %s\n\n📱 用户 ID：<code>%d</code>\n\n🔗 用户名：%s",
		mentionHTML(user.ID, displayName(user)),
		user.ID,
		html.EscapeString(username),
	)

	photos, err := b.GetUserProfilePhotos(ctx, &bot.GetUserProfilePhotosParams{
		UserID: user.ID,
		Limit:  1,
	})
	if err == nil && photos != nil && photos.TotalCount > 0 && len(photos.Photos) > 0 {
		sizes := photos.Photos[0]
		fileID := sizes[len(sizes)-1].FileID
		_, err = b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:          s.Cfg.AdminGroupID,
			MessageThreadID: threadID,
			Photo:           &models.InputFileString{Data: fileID},
			Caption:         caption,
			ParseMode:       models.ParseModeHTML,
			ReplyMarkup:     keyboard,
		})
		return err
	}

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          s.Cfg.AdminGroupID,
		MessageThreadID: threadID,
		Text:            caption,
		ParseMode:       models.ParseModeHTML,
		ReplyMarkup:     keyboard,
	})
	return err
}

// ForwardUserToAdmin copies a private user message into the admin topic.
// The boolean result is true only when the message was accepted for delivery.
func (s *Services) ForwardUserToAdmin(ctx context.Context, b *bot.Bot, msg *models.Message) (bool, error) {
	user := msg.From
	if user == nil || s.Cfg.IsAdmin(user.ID) {
		return false, nil
	}
	dbUser, err := s.UpsertTelegramUser(user)
	if err != nil {
		return false, err
	}
	blockedMessage, err := s.UserConversationBlockMessage(dbUser)
	if err != nil {
		return false, err
	}
	if blockedMessage != "" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: blockedMessage})
		return false, nil
	}

	threadID, err := s.EnsureTopic(ctx, b, user, dbUser)
	if err != nil {
		return false, err
	}
	if msg.MediaGroupID != "" {
		if err := s.bufferMediaGroup(ctx, b, msg, "u2a", threadID, user.ID); err != nil {
			return false, err
		}
		return true, nil
	}

	params := &bot.CopyMessageParams{
		ChatID:          s.Cfg.AdminGroupID,
		FromChatID:      msg.Chat.ID,
		MessageID:       msg.ID,
		MessageThreadID: threadID,
	}
	if msg.ReplyToMessage != nil {
		if mapping, _ := s.Store.GetByUserMessageID(msg.ReplyToMessage.ID); mapping != nil {
			params.ReplyParameters = &models.ReplyParameters{MessageID: mapping.GroupChatMessageID}
		}
	}

	sent, err := b.CopyMessage(ctx, params)
	if err != nil {
		s.Logger.Warn("copy user message failed", "err", err, "user_id", user.ID, "thread_id", threadID)
		if s.Cfg.DeleteTopicAsForeverBan {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: "消息未能转达：客服话题已被删除，当前不能自动重新建立会话。"})
			return false, nil
		}
		_ = s.Store.UpdateUserThreadID(user.ID, 0)
		_ = s.Store.DeleteForumStatus(threadID)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: "消息未能转达：原会话已失效。请重新发送一条消息建立新会话。"})
		return false, nil
	}

	if err := s.Store.SaveMessageMap(&model.MessageMap{
		UserChatMessageID:  msg.ID,
		GroupChatMessageID: sent.ID,
		UserID:             user.ID,
		MessageText:        messageContent(msg),
	}); err != nil {
		s.Logger.Warn("save user message mapping failed", "err", err, "user_id", user.ID, "message_id", msg.ID)
	}
	return true, nil
}

// UserConversationBlockMessage returns the user-facing reason a message cannot be forwarded.
func (s *Services) UserConversationBlockMessage(user *model.User) (string, error) {
	if user == nil {
		return "", nil
	}
	if s.Cfg.IsAdmin(user.UserID) {
		return "你是管理员，请在管理群中处理用户会话。", nil
	}
	if user.IsBanned {
		reason := strings.TrimSpace(user.BanReason)
		if reason == "" {
			reason = "管理员限制"
		}
		return "你已被限制联系本客服。\n原因：" + reason, nil
	}
	if user.MessageThreadID <= 0 {
		return "", nil
	}
	status, err := s.Store.GetForumStatus(user.MessageThreadID)
	if err != nil {
		return "", err
	}
	if status != nil && status.Status == "closed" {
		return "客服已关闭本次对话，消息未转达。请等待管理员重新打开会话。", nil
	}
	return "", nil
}

// ForwardAdminToUser copies staff replies from a forum topic back to the user.
func (s *Services) ForwardAdminToUser(ctx context.Context, b *bot.Bot, msg *models.Message) error {
	if msg.MessageThreadID == 0 {
		return nil // general chat messages ignored
	}
	// forum topic lifecycle events
	if msg.ForumTopicCreated != nil {
		_ = s.Store.UpsertForumStatus(&model.ForumStatus{
			ChatID:          msg.Chat.ID,
			MessageThreadID: msg.MessageThreadID,
			Status:          "opened",
		})
		return nil
	}
	if msg.ForumTopicClosed != nil {
		previous, _ := s.Store.GetForumStatus(msg.MessageThreadID)
		if u, _ := s.Store.GetUserByThreadID(msg.MessageThreadID); u != nil && (previous == nil || previous.Status != "closed") {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: u.UserID,
				Text:   "对话已经结束。对方已经关闭了对话。你的留言将被忽略。",
			})
		}
		_ = s.Store.UpsertForumStatus(&model.ForumStatus{
			ChatID:          msg.Chat.ID,
			MessageThreadID: msg.MessageThreadID,
			Status:          "closed",
		})
		return nil
	}
	if msg.ForumTopicReopened != nil {
		previous, _ := s.Store.GetForumStatus(msg.MessageThreadID)
		if u, _ := s.Store.GetUserByThreadID(msg.MessageThreadID); u != nil && (previous == nil || previous.Status != "opened") {
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: u.UserID,
				Text:   "对方重新打开了对话。可以继续对话了。",
			})
		}
		_ = s.Store.UpsertForumStatus(&model.ForumStatus{
			ChatID:          msg.Chat.ID,
			MessageThreadID: msg.MessageThreadID,
			Status:          "opened",
		})
		return nil
	}

	dbUser, err := s.Store.GetUserByThreadID(msg.MessageThreadID)
	if err != nil || dbUser == nil {
		return nil
	}
	if st, _ := s.Store.GetForumStatus(msg.MessageThreadID); st != nil && st.Status == "closed" {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: msg.MessageThreadID,
			Text:            "对话已经结束。希望和对方联系，需要打开对话。",
			ReplyParameters: &models.ReplyParameters{MessageID: msg.ID},
		})
		return nil
	}

	if msg.MediaGroupID != "" {
		return s.bufferMediaGroup(ctx, b, msg, "a2u", 0, dbUser.UserID)
	}

	params := &bot.CopyMessageParams{
		ChatID:     dbUser.UserID,
		FromChatID: msg.Chat.ID,
		MessageID:  msg.ID,
	}
	if msg.ReplyToMessage != nil {
		if m, _ := s.Store.GetByGroupMessageID(msg.ReplyToMessage.ID); m != nil {
			params.ReplyParameters = &models.ReplyParameters{MessageID: m.UserChatMessageID}
		}
	}

	sent, err := b.CopyMessage(ctx, params)
	if err != nil {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:          msg.Chat.ID,
			MessageThreadID: msg.MessageThreadID,
			Text:            fmt.Sprintf("发送失败: %v", err),
			ReplyParameters: &models.ReplyParameters{MessageID: msg.ID},
		})
		return err
	}
	return s.Store.SaveMessageMap(&model.MessageMap{
		UserChatMessageID:  sent.ID,
		GroupChatMessageID: msg.ID,
		UserID:             dbUser.UserID,
		MessageText:        messageContent(msg),
	})
}

func (s *Services) bufferMediaGroup(ctx context.Context, b *bot.Bot, msg *models.Message, dir string, threadID int, userID int64) error {
	if err := s.Store.SaveMediaGroupMessage(&model.MediaGroupMessage{
		ChatID:       msg.Chat.ID,
		MessageID:    msg.ID,
		MediaGroupID: msg.MediaGroupID,
		CaptionHTML:  msg.Caption,
	}); err != nil {
		return fmt.Errorf("buffer media group: %w", err)
	}

	fromChatID := msg.Chat.ID
	mediaGroupID := msg.MediaGroupID
	targetID := s.Cfg.AdminGroupID
	if dir == "a2u" {
		targetID = userID
	}
	name := fmt.Sprintf("sendmediagroup_%d_%d_%s_%s", fromChatID, targetID, dir, mediaGroupID)
	s.Jobs.Once(name, 2*time.Second, func() {
		s.flushMediaGroup(context.Background(), b, fromChatID, targetID, mediaGroupID, dir, threadID, userID)
	})
	return nil
}

func (s *Services) flushMediaGroup(ctx context.Context, b *bot.Bot, fromChatID, targetID int64, mediaGroupID, dir string, threadID int, userID int64) {
	if dir == "u2a" {
		user, err := s.Store.GetUserByTelegramID(userID)
		if err != nil || user == nil {
			s.Logger.Warn("reload user before media group flush failed", "err", err, "user_id", userID)
			return
		}
		blockedMessage, err := s.UserConversationBlockMessage(user)
		if err != nil {
			s.Logger.Warn("check media group conversation state failed", "err", err, "user_id", userID)
			return
		}
		if blockedMessage != "" {
			_ = s.Store.DeleteMediaGroupMessages(fromChatID, mediaGroupID)
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: userID, Text: blockedMessage})
			return
		}
	}
	msgs, err := s.Store.ListMediaGroupMessages(fromChatID, mediaGroupID)
	if err != nil || len(msgs) == 0 {
		return
	}
	ids := make([]int, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.MessageID)
	}
	params := &bot.CopyMessagesParams{
		ChatID:     targetID,
		FromChatID: fromChatID,
		MessageIDs: ids,
	}
	if dir == "u2a" {
		params.MessageThreadID = threadID
	}
	sent, err := b.CopyMessages(ctx, params)
	if err != nil {
		s.Logger.Error("copy media group failed", "err", err, "dir", dir)
		return
	}
	for i, id := range sent {
		if i >= len(msgs) {
			break
		}
		if dir == "u2a" {
			_ = s.Store.SaveMessageMap(&model.MessageMap{
				UserChatMessageID:  msgs[i].MessageID,
				GroupChatMessageID: id.ID,
				UserID:             userID,
			})
		} else {
			_ = s.Store.SaveMessageMap(&model.MessageMap{
				UserChatMessageID:  id.ID,
				GroupChatMessageID: msgs[i].MessageID,
				UserID:             targetID,
			})
		}
	}
	if dir == "u2a" && s.Cfg.UserForwardAck {
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: userID, Text: "✓ 已转达客服"})
	}
	if err := s.Store.DeleteMediaGroupMessages(fromChatID, mediaGroupID); err != nil {
		s.Logger.Warn("delete media group buffer failed", "err", err, "chat_id", fromChatID, "media_group_id", mediaGroupID)
	}
}

// EditMirroredMessage updates the bot-owned copy when either side edits a message.
func (s *Services) EditMirroredMessage(ctx context.Context, b *bot.Bot, msg *models.Message) error {
	newContent := messageContent(msg)
	if newContent == "" {
		return nil
	}

	var mapping *model.MessageMap
	var err error
	var targetChatID int64
	var targetMessageID int
	var updateStoredText func(string) error

	switch msg.Chat.Type {
	case models.ChatTypePrivate:
		mapping, err = s.Store.GetByUserMessageID(msg.ID)
		if mapping != nil {
			targetChatID = s.Cfg.AdminGroupID
			targetMessageID = mapping.GroupChatMessageID
			updateStoredText = func(text string) error {
				return s.Store.UpdateMessageTextByUserMessageID(msg.ID, text)
			}
		}
	case models.ChatTypeSupergroup, models.ChatTypeGroup:
		if msg.Chat.ID != s.Cfg.AdminGroupID {
			return nil
		}
		mapping, err = s.Store.GetByGroupMessageID(msg.ID)
		if mapping != nil {
			targetChatID = mapping.UserID
			targetMessageID = mapping.UserChatMessageID
			updateStoredText = func(text string) error {
				return s.Store.UpdateMessageTextByGroupMessageID(msg.ID, text)
			}
		}
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("find edited message mapping: %w", err)
	}
	if mapping == nil {
		s.Logger.Info("edited message mapping not found", "chat_id", msg.Chat.ID, "message_id", msg.ID)
		return nil
	}
	if mapping.MessageText == newContent {
		return nil
	}

	if msg.Text != "" {
		_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    targetChatID,
			MessageID: targetMessageID,
			Text:      formatEditedContent(newContent, mapping.MessageText, 4096),
		})
	} else {
		_, err = b.EditMessageCaption(ctx, &bot.EditMessageCaptionParams{
			ChatID:    targetChatID,
			MessageID: targetMessageID,
			Caption:   formatEditedContent(newContent, mapping.MessageText, 1024),
		})
	}
	if err != nil {
		return fmt.Errorf("edit mirrored message: %w", err)
	}
	if err := updateStoredText(newContent); err != nil {
		return fmt.Errorf("save edited message text: %w", err)
	}
	return nil
}

func messageContent(msg *models.Message) string {
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

func formatEditedContent(current, previous string, limit int) string {
	if previous == "" {
		previous = "内容未记录"
	}
	formatted := current + "(修改前：" + previous + ")"
	runes := []rune(formatted)
	if len(runes) <= limit {
		return formatted
	}
	return string(runes[:limit])
}

// ClearTopic deletes the forum topic and resets all conversation state.
func (s *Services) ClearTopic(ctx context.Context, b *bot.Bot, adminChatID int64, threadID int, operatorID int64) error {
	if !s.Cfg.IsAdmin(operatorID) {
		return fmt.Errorf("没有权限执行此操作")
	}
	user, err := s.Store.GetUserByThreadID(threadID)
	if err != nil {
		return err
	}
	if user == nil {
		return fmt.Errorf("未找到话题对应用户")
	}
	if s.Cfg.IsAdmin(user.UserID) {
		return fmt.Errorf("不能清理管理员账号会话")
	}

	var messageIDs []int
	if s.Cfg.DeleteUserMessageOnClear {
		mappings, listErr := s.Store.ListMessageMapsByUser(user.UserID)
		if listErr != nil {
			return listErr
		}
		for _, mapping := range mappings {
			if mapping.UserChatMessageID > 0 {
				messageIDs = append(messageIDs, mapping.UserChatMessageID)
			}
		}
	}
	if _, err := b.DeleteForumTopic(ctx, &bot.DeleteForumTopicParams{ChatID: adminChatID, MessageThreadID: threadID}); err != nil {
		return err
	}

	if err := s.Store.UpdateUserThreadID(user.UserID, 0); err != nil {
		return err
	}
	if err := s.Store.DeleteForumStatus(threadID); err != nil {
		return err
	}
	if s.Cfg.DeleteUserMessageOnClear {
		for index := 0; index < len(messageIDs); index += 100 {
			end := index + 100
			if end > len(messageIDs) {
				end = len(messageIDs)
			}
			_, _ = b.DeleteMessages(ctx, &bot.DeleteMessagesParams{ChatID: user.UserID, MessageIDs: messageIDs[index:end]})
		}
		if err := s.Store.DeleteMessageMapsByUser(user.UserID); err != nil {
			return err
		}
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: user.UserID, Text: "客服已结束并清理本次会话。"})
	return nil
}

// Broadcast copies a replied message to all known, unbanned, non-admin users.
func (s *Services) Broadcast(ctx context.Context, b *bot.Bot, fromChatID int64, messageID int, progress func(done, total, success, failed int)) (success, failed int) {
	users, err := s.Store.ListUsers()
	if err != nil {
		return 0, 0
	}
	targets := make([]*model.User, 0, len(users))
	for _, user := range users {
		if user != nil && !user.IsBanned && !s.Cfg.IsAdmin(user.UserID) {
			targets = append(targets, user)
		}
	}
	total := len(targets)
	for index, user := range targets {
		if _, err := b.CopyMessage(ctx, &bot.CopyMessageParams{ChatID: user.UserID, FromChatID: fromChatID, MessageID: messageID}); err != nil {
			failed++
		} else {
			success++
		}
		done := index + 1
		if progress != nil && (done%25 == 0 || done == total) {
			progress(done, total, success, failed)
		}
		if done < total {
			select {
			case <-ctx.Done():
				return success, failed
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return success, failed
}

func mentionHTML(userID int64, name string) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, userID, html.EscapeString(name))
}

func displayName(u *models.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = fmt.Sprintf("%d", u.ID)
	}
	return name
}

func directContactURL(user *models.User) string {
	if user.Username != "" {
		return "https://t.me/" + user.Username
	}
	return fmt.Sprintf("tg://user?id=%d", user.ID)
}
