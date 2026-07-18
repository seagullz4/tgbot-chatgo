package service

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/model"
)

const (
	maxPendingMessages       = 20
	arithmeticChoiceCount    = 4
	wrongAnswerCooldown      = 30 * time.Second
	verificationMessageTTL   = 2 * time.Minute
	arithmeticOperationCount = 4
)

type arithmeticChallenge struct {
	Question string
	Answer   int
	Choices  []int
}

func (s *Services) lockVerification(userID int64) func() {
	return s.verificationLocks.Lock(userID)
}

func appendPendingMessage(raw string, messageID int) string {
	ids := pendingMessageIDs(raw)
	for _, existing := range ids {
		if existing == messageID {
			return raw
		}
	}
	ids = append(ids, messageID)
	if len(ids) > maxPendingMessages {
		ids = ids[len(ids)-maxPendingMessages:]
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ",")
}

func pendingMessageIDs(raw string) []int {
	var ids []int
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func newArithmeticChallenge(operation int) arithmeticChallenge {
	var left, right, answer int
	var symbol string
	switch operation {
	case 0:
		left = randomInt(2, 40)
		right = randomInt(2, 40)
		answer = left + right
		symbol = "+"
	case 1:
		left = randomInt(10, 60)
		right = randomInt(1, left)
		answer = left - right
		symbol = "−"
	case 2:
		left = randomInt(2, 12)
		right = randomInt(2, 12)
		answer = left * right
		symbol = "×"
	case 3:
		right = randomInt(2, 12)
		answer = randomInt(2, 12)
		left = right * answer
		symbol = "÷"
	default:
		return newArithmeticChallenge(rand.IntN(arithmeticOperationCount))
	}
	return arithmeticChallenge{
		Question: fmt.Sprintf("%d %s %d = ?", left, symbol, right),
		Answer:   answer,
		Choices:  arithmeticChoices(answer),
	}
}

func arithmeticChoices(answer int) []int {
	choices := make([]int, 0, arithmeticChoiceCount)
	seen := map[int]struct{}{answer: {}}
	choices = append(choices, answer)
	spread := 6
	if answer > 30 {
		spread = 12
	}
	for len(choices) < arithmeticChoiceCount {
		candidate := answer + rand.IntN(spread*2+1) - spread
		if candidate < 0 || candidate == answer {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		choices = append(choices, candidate)
	}
	rand.Shuffle(len(choices), func(i, j int) { choices[i], choices[j] = choices[j], choices[i] })
	return choices
}

func randomInt(minimum, maximum int) int {
	return minimum + rand.IntN(maximum-minimum+1)
}

// CheckHuman enforces arithmetic verification before private messages are forwarded.
func (s *Services) CheckHuman(ctx context.Context, b *bot.Bot, msg *models.Message) (bool, error) {
	if s.Cfg.Snapshot().DisableVerification {
		return true, nil
	}
	user := msg.From
	if user == nil {
		return false, nil
	}
	unlock := s.lockVerification(user.ID)
	defer unlock()
	state, err := s.Store.GetVerificationState(user.ID)
	if err != nil {
		return false, err
	}
	if state == nil {
		state = &model.VerificationState{UserID: user.ID}
	}
	if state.IsHuman {
		return true, nil
	}
	if !state.ErrorUntil.IsZero() && time.Now().Before(state.ErrorUntil) {
		remaining := time.Until(state.ErrorUntil).Round(time.Second)
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: fmt.Sprintf("答案错误后需要短暂等待，请在 %s 后重试。", remaining)})
		return false, nil
	}

	state.PendingMessageIDs = appendPendingMessage(state.PendingMessageIDs, msg.ID)
	if state.Answer != "" {
		if _, parseErr := strconv.Atoi(state.Answer); parseErr == nil {
			return false, s.Store.UpsertVerificationState(state)
		}
		state.Answer = ""
	}

	challenge := newArithmeticChallenge(rand.IntN(arithmeticOperationCount))
	keyboard := make([][]models.InlineKeyboardButton, 0, 2)
	for index := 0; index < len(challenge.Choices); index += 2 {
		row := make([]models.InlineKeyboardButton, 0, 2)
		for _, choice := range challenge.Choices[index : index+2] {
			row = append(row, models.InlineKeyboardButton{
				Text:         strconv.Itoa(choice),
				CallbackData: fmt.Sprintf("math_%d_%d", choice, user.ID),
			})
		}
		keyboard = append(keyboard, row)
	}

	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: msg.Chat.ID,
		Text: fmt.Sprintf(
			"%s，请完成安全验证：\n\n<b>%s</b>\n\n选择正确答案后，刚才的消息会自动转达客服。",
			mentionHTML(user.ID, displayName(user)), challenge.Question,
		),
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
	})
	if err != nil {
		return false, err
	}
	state.Answer = strconv.Itoa(challenge.Answer)
	state.IsHuman = false
	state.ErrorUntil = time.Time{}
	if err := s.Store.UpsertVerificationState(state); err != nil {
		return false, err
	}

	chatID, messageID, expectedAnswer := sent.Chat.ID, sent.ID, state.Answer
	s.Jobs.Once(fmt.Sprintf("delete_math_verification_%d_%d", chatID, messageID), verificationMessageTTL, func() {
		_, _ = b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{ChatID: chatID, MessageID: messageID})
		latest, stateErr := s.Store.GetVerificationState(user.ID)
		if stateErr == nil && latest != nil && !latest.IsHuman && latest.Answer == expectedAnswer {
			latest.Answer = ""
			_ = s.Store.UpsertVerificationState(latest)
		}
	})
	return false, nil
}

func parseMathCallback(data string) (string, int64, bool) {
	value := strings.TrimPrefix(data, "math_")
	separator := strings.LastIndex(value, "_")
	if separator <= 0 {
		return "", 0, false
	}
	userID, err := strconv.ParseInt(value[separator+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	if _, err := strconv.Atoi(value[:separator]); err != nil {
		return "", 0, false
	}
	return value[:separator], userID, true
}

// HandleVerificationCallback processes math_* callback queries.
func (s *Services) HandleVerificationCallback(ctx context.Context, b *bot.Bot, query *models.CallbackQuery) error {
	answer, expectedUserID, ok := parseMathCallback(query.Data)
	if !ok {
		return nil
	}
	user := query.From
	if user.ID != expectedUserID {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: "这不是你的验证题", ShowAlert: true})
		return nil
	}
	unlock := s.lockVerification(user.ID)
	defer unlock()
	state, err := s.Store.GetVerificationState(user.ID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &model.VerificationState{UserID: user.ID}
	}

	if state.Answer == "" {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "验证题已失效，请重新发送消息获取新题",
			ShowAlert:       true,
		})
		deleteCallbackMessage(ctx, b, query)
		return nil
	}
	if answer != state.Answer {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "答案错误，请在 30 秒后重新发送消息获取新题",
			ShowAlert:       true,
		})
		state.Answer = ""
		state.ErrorUntil = time.Now().Add(wrongAnswerCooldown)
		if err := s.Store.UpsertVerificationState(state); err != nil {
			return err
		}
		deleteCallbackMessage(ctx, b, query)
		return nil
	}

	pending := pendingMessageIDs(state.PendingMessageIDs)
	state.IsHuman = true
	state.Answer = ""
	state.ErrorUntil = time.Time{}
	state.PendingMessageIDs = ""
	if err := s.Store.UpsertVerificationState(state); err != nil {
		return err
	}
	_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: "验证通过"})
	deleteCallbackMessage(ctx, b, query)

	deliveredCount := 0
	failed := 0
	blocked := false
	for _, messageID := range pending {
		message := &models.Message{ID: messageID, Chat: models.Chat{ID: user.ID, Type: models.ChatTypePrivate}, From: &user}
		delivered, forwardErr := s.ForwardUserToAdmin(ctx, b, message)
		if forwardErr != nil {
			failed++
			s.Logger.Error("forward pending verification message", "err", forwardErr, "user_id", user.ID, "message_id", messageID)
			continue
		}
		if !delivered {
			blocked = true
			break
		}
		deliveredCount++
	}
	text := "验证通过，现在可以继续会话。"
	switch {
	case failed > 0:
		text = "验证通过，但部分消息处理失败，请重新发送。"
	case blocked:
		text = "验证通过，但当前会话不可转发，请查看上方提示。"
	case deliveredCount > 0:
		text = "验证通过，刚才的消息已转达客服。"
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: user.ID, Text: text, ReplyMarkup: UserKeyboard()})
	return nil
}

func deleteCallbackMessage(ctx context.Context, b *bot.Bot, query *models.CallbackQuery) {
	if query.Message.Message != nil {
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: query.Message.Message.Chat.ID, MessageID: query.Message.Message.ID})
	}
}

// CheckRateLimit returns false if the user is messaging too fast.
func (s *Services) CheckRateLimit(ctx context.Context, b *bot.Bot, msg *models.Message) (bool, error) {
	if s.Cfg.Snapshot().MessageInterval <= 0 {
		return true, nil
	}
	user := msg.From
	if user == nil {
		return true, nil
	}
	state, err := s.Store.GetVerificationState(user.ID)
	if err != nil {
		return true, err
	}
	if state == nil {
		state = &model.VerificationState{UserID: user.ID}
	}
	interval := time.Duration(s.Cfg.Snapshot().MessageInterval) * time.Second
	elapsed := time.Since(state.LastMsgAt)
	if !state.LastMsgAt.IsZero() && elapsed < interval {
		remaining := interval - elapsed
		seconds := int(remaining.Round(time.Second).Seconds())
		if seconds < 1 {
			seconds = 1
		}
		_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: msg.Chat.ID, Text: fmt.Sprintf("发送过快，请 %d 秒后再试。", seconds)})
		return false, nil
	}
	state.LastMsgAt = time.Now()
	if err := s.Store.UpsertVerificationState(state); err != nil {
		return true, err
	}
	return true, nil
}
