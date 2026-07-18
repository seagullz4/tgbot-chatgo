package service

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/model"
	storesqlite "telegram-interactive-bot/go-bot/internal/store/sqlite"
)

type experienceHTTPClient struct {
	paths        []string
	texts        []string
	chatIDs      []string
	replyMarkups []string
}

func (client *experienceHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		return nil, err
	}
	client.paths = append(client.paths, request.URL.Path)
	client.texts = append(client.texts, request.FormValue("text"))
	client.chatIDs = append(client.chatIDs, request.FormValue("chat_id"))
	client.replyMarkups = append(client.replyMarkups, request.FormValue("reply_markup"))
	result := "true"
	if strings.HasSuffix(request.URL.Path, "/sendMessage") || strings.HasSuffix(request.URL.Path, "/copyMessage") {
		result = "{\"message_id\":20,\"date\":0,\"chat\":{\"id\":42,\"type\":\"private\"}}"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{\"ok\":true,\"result\":" + result + "}")),
	}, nil
}

func newExperienceBot(t *testing.T, client *experienceHTTPClient) *bot.Bot {
	t.Helper()
	telegramBot, err := bot.New("123456:test-token",
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(time.Second, client),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	return telegramBot
}

func newClosedConversationService(t *testing.T, databaseName string) (*Services, *storesqlite.SQLite) {
	t.Helper()
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), databaseName), 4)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := messageStore.EnsureUser(&model.User{UserID: 42, FirstName: "Test"}); err != nil {
		messageStore.Close()
		t.Fatalf("ensure user: %v", err)
	}
	if err := messageStore.UpdateUserThreadID(42, 100); err != nil {
		messageStore.Close()
		t.Fatalf("set thread: %v", err)
	}
	if err := messageStore.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 100, Status: "closed"}); err != nil {
		messageStore.Close()
		t.Fatalf("close forum status: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := New(&config.Config{
		AdminGroupID:        -100,
		AdminUserIDs:        map[int64]struct{}{1: {}},
		DisableVerification: true,
		UserForwardAck:      true,
	}, messageStore, job.New(), logger)
	return services, messageStore
}

func TestPendingMessageQueueDeduplicatesAndCaps(t *testing.T) {
	raw := ""
	for id := 1; id <= 25; id++ {
		raw = appendPendingMessage(raw, id)
	}
	raw = appendPendingMessage(raw, 25)
	ids := pendingMessageIDs(raw)
	if len(ids) != maxPendingMessages {
		t.Fatalf("pending count = %d, want %d", len(ids), maxPendingMessages)
	}
	if ids[0] != 6 || ids[len(ids)-1] != 25 {
		t.Fatalf("pending ids = %v", ids)
	}
}

func TestParseMathCallback(t *testing.T) {
	answer, userID, ok := parseMathCallback("math_42_12345")
	if !ok || answer != "42" || userID != 12345 {
		t.Fatalf("parsed answer=%q user=%d ok=%v", answer, userID, ok)
	}
	if _, _, ok := parseMathCallback("math_invalid_12345"); ok {
		t.Fatal("non-numeric answer should be rejected")
	}
}

func TestUserKeyboardHasNoConversationEndAction(t *testing.T) {
	keyboard := UserKeyboard()
	var labels []string
	for _, row := range keyboard.Keyboard {
		for _, button := range row {
			labels = append(labels, button.Text)
		}
	}
	joined := strings.Join(labels, ",")
	if strings.Contains(joined, "结束") || strings.Contains(joined, "取消") {
		t.Fatalf("keyboard contains forbidden end action: %s", joined)
	}
	if !strings.Contains(joined, ShortcutStatus) || !strings.Contains(joined, ShortcutID) || !strings.Contains(joined, ShortcutHelp) {
		t.Fatalf("keyboard missing shortcuts: %s", joined)
	}
}

func TestUserStatusReflectsBanAndClosedConversation(t *testing.T) {
	store, err := storesqlite.Open(filepath.Join(t.TempDir(), "status.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.EnsureUser(&model.User{UserID: 42, FirstName: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := New(&config.Config{DisableVerification: true, AdminUserIDs: map[int64]struct{}{1: {}}}, store, job.New(), logger)
	if err := store.SetUserBanned(42, true, "spam"); err != nil {
		t.Fatal(err)
	}
	status, err := services.UserStatus(42)
	if err != nil || !strings.Contains(status, "spam") {
		t.Fatalf("banned status=%q err=%v", status, err)
	}
	if err := store.SetUserBanned(42, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserThreadID(42, 100); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 100, Status: "closed"}); err != nil {
		t.Fatal(err)
	}
	status, err = services.UserStatus(42)
	if err != nil || !strings.Contains(status, "关闭") {
		t.Fatalf("closed status=%q err=%v", status, err)
	}
}

func TestVerificationReplayDoesNotAcknowledgeClosedConversation(t *testing.T) {
	services, messageStore := newClosedConversationService(t, "verification-closed.sqlite3")
	defer messageStore.Close()
	services.Cfg.(*config.Config).DisableVerification = false
	if err := messageStore.UpsertVerificationState(&model.VerificationState{
		UserID:            42,
		Answer:            "6",
		PendingMessageIDs: "10",
	}); err != nil {
		t.Fatalf("save verification state: %v", err)
	}
	client := &experienceHTTPClient{}
	telegramBot := newExperienceBot(t, client)
	query := &models.CallbackQuery{
		ID:   "callback-1",
		From: models.User{ID: 42, FirstName: "Test"},
		Data: "math_6_42",
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 99, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}},
		},
	}
	if err := services.HandleVerificationCallback(context.Background(), telegramBot, query); err != nil {
		t.Fatalf("handle verification callback: %v", err)
	}
	joinedTexts := strings.Join(client.texts, "\n")
	if !strings.Contains(joinedTexts, "客服已关闭") || !strings.Contains(joinedTexts, "当前会话不可转发") {
		t.Fatalf("closed verification replay notices = %q", client.texts)
	}
	if strings.Contains(joinedTexts, "已转达客服") {
		t.Fatalf("verification replay acknowledged closed conversation: %q", client.texts)
	}
	for _, requestPath := range client.paths {
		if strings.HasSuffix(requestPath, "/copyMessage") || strings.HasSuffix(requestPath, "/copyMessages") {
			t.Fatalf("verification replay copied a closed message: %q", client.paths)
		}
	}
}

func TestMediaGroupFlushDoesNotAcknowledgeAfterConversationCloses(t *testing.T) {
	services, messageStore := newClosedConversationService(t, "media-closed.sqlite3")
	defer messageStore.Close()
	if err := messageStore.SaveMediaGroupMessage(&model.MediaGroupMessage{
		ChatID:       42,
		MessageID:    11,
		MediaGroupID: "album-1",
	}); err != nil {
		t.Fatalf("buffer media group: %v", err)
	}
	client := &experienceHTTPClient{}
	telegramBot := newExperienceBot(t, client)
	services.flushMediaGroup(context.Background(), telegramBot, 42, -100, "album-1", "u2a", 100, 42)

	joinedTexts := strings.Join(client.texts, "\n")
	if !strings.Contains(joinedTexts, "客服已关闭") {
		t.Fatalf("closed media group notice = %q", client.texts)
	}
	if strings.Contains(joinedTexts, "已转达客服") {
		t.Fatalf("media group acknowledged after close: %q", client.texts)
	}
	for _, requestPath := range client.paths {
		if strings.HasSuffix(requestPath, "/copyMessages") {
			t.Fatalf("closed media group was copied: %q", client.paths)
		}
	}
	buffered, err := messageStore.ListMediaGroupMessages(42, "album-1")
	if err != nil {
		t.Fatalf("reload media group: %v", err)
	}
	if len(buffered) != 0 {
		t.Fatalf("closed media group buffer was not cleared: %v", buffered)
	}
}

func TestUserInfoUsesLocalizedConversationStatus(t *testing.T) {
	services, messageStore := newClosedConversationService(t, "localized-info.sqlite3")
	defer messageStore.Close()
	info, err := services.UserInfo(42)
	if err != nil {
		t.Fatalf("user info: %v", err)
	}
	if !strings.Contains(info, "会话状态：已关闭") {
		t.Fatalf("localized user info = %q", info)
	}
	if strings.Contains(info, "opened") || strings.Contains(info, "closed") {
		t.Fatalf("user info exposes raw status: %q", info)
	}
}

func TestArithmeticChallengeSupportsAllFourOperations(t *testing.T) {
	symbols := []string{"+", "−", "×", "÷"}
	for operation, symbol := range symbols {
		for attempt := 0; attempt < 20; attempt++ {
			challenge := newArithmeticChallenge(operation)
			if !strings.Contains(challenge.Question, symbol) {
				t.Fatalf("operation %d question = %q, want symbol %q", operation, challenge.Question, symbol)
			}
			parts := strings.Fields(challenge.Question)
			if len(parts) != 5 {
				t.Fatalf("invalid question format: %q", challenge.Question)
			}
			left, leftErr := strconv.Atoi(parts[0])
			right, rightErr := strconv.Atoi(parts[2])
			if leftErr != nil || rightErr != nil {
				t.Fatalf("invalid operands in %q", challenge.Question)
			}
			expected := 0
			switch symbol {
			case "+":
				expected = left + right
			case "−":
				expected = left - right
			case "×":
				expected = left * right
			case "÷":
				if right == 0 || left%right != 0 {
					t.Fatalf("division is not exact: %q", challenge.Question)
				}
				expected = left / right
			}
			if challenge.Answer != expected {
				t.Fatalf("question %q answer=%d want=%d", challenge.Question, challenge.Answer, expected)
			}
			if len(challenge.Choices) != arithmeticChoiceCount {
				t.Fatalf("operation %d choices = %v", operation, challenge.Choices)
			}
			seen := make(map[int]bool, len(challenge.Choices))
			containsAnswer := false
			for _, choice := range challenge.Choices {
				if seen[choice] {
					t.Fatalf("duplicate choice in %v", challenge.Choices)
				}
				seen[choice] = true
				containsAnswer = containsAnswer || choice == challenge.Answer
			}
			if !containsAnswer {
				t.Fatalf("answer %d missing from %v", challenge.Answer, challenge.Choices)
			}
		}
	}
}

func TestCheckHumanSendsArithmeticQuestionWithoutImageDependency(t *testing.T) {
	messageStore, err := storesqlite.Open(filepath.Join(t.TempDir(), "math-verification.sqlite3"), 4)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer messageStore.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := New(&config.Config{AdminUserIDs: map[int64]struct{}{1: {}}}, messageStore, job.New(), logger)
	client := &experienceHTTPClient{}
	telegramBot := newExperienceBot(t, client)

	verified, err := services.CheckHuman(context.Background(), telegramBot, &models.Message{
		ID:   10,
		Text: "你好",
		From: &models.User{ID: 42, FirstName: "Test"},
		Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate},
	})
	if err != nil {
		t.Fatalf("check human: %v", err)
	}
	if verified {
		t.Fatal("unverified user should not pass before answering")
	}
	joinedText := strings.Join(client.texts, "\n")
	joinedMarkup := strings.Join(client.replyMarkups, "\n")
	if !strings.Contains(joinedText, "安全验证") || !strings.Contains(joinedMarkup, "math_") {
		t.Fatalf("math verification text=%q markup=%q", client.texts, client.replyMarkups)
	}
	for _, requestPath := range client.paths {
		if strings.HasSuffix(requestPath, "/sendPhoto") {
			t.Fatalf("math verification unexpectedly sent an image: %q", client.paths)
		}
	}
	state, err := messageStore.GetVerificationState(42)
	if err != nil {
		t.Fatalf("load verification state: %v", err)
	}
	if state == nil || state.Answer == "" || state.PendingMessageIDs != "10" {
		t.Fatalf("verification state = %#v", state)
	}
	if _, err := strconv.Atoi(state.Answer); err != nil {
		t.Fatalf("stored answer is not numeric: %q", state.Answer)
	}
}

func TestCorrectMathAnswerContinuesPendingConversation(t *testing.T) {
	services, messageStore := newClosedConversationService(t, "verification-open.sqlite3")
	defer messageStore.Close()
	services.Cfg.(*config.Config).DisableVerification = false
	if err := messageStore.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 100, Status: "opened"}); err != nil {
		t.Fatalf("open conversation: %v", err)
	}
	if err := messageStore.UpsertVerificationState(&model.VerificationState{
		UserID:            42,
		Answer:            "6",
		PendingMessageIDs: "10",
	}); err != nil {
		t.Fatalf("save verification state: %v", err)
	}
	client := &experienceHTTPClient{}
	telegramBot := newExperienceBot(t, client)
	query := &models.CallbackQuery{
		ID:   "callback-correct",
		From: models.User{ID: 42, FirstName: "Test"},
		Data: "math_6_42",
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 99, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}},
		},
	}
	if err := services.HandleVerificationCallback(context.Background(), telegramBot, query); err != nil {
		t.Fatalf("handle correct answer: %v", err)
	}
	state, err := messageStore.GetVerificationState(42)
	if err != nil {
		t.Fatalf("load verification state: %v", err)
	}
	if state == nil || !state.IsHuman || state.Answer != "" || state.PendingMessageIDs != "" {
		t.Fatalf("verified state = %#v", state)
	}
	if !strings.Contains(strings.Join(client.texts, "\n"), "已转达客服") {
		t.Fatalf("verification success messages = %q", client.texts)
	}
	copied := false
	for _, requestPath := range client.paths {
		if strings.HasSuffix(requestPath, "/copyMessage") {
			copied = true
		}
	}
	if !copied {
		t.Fatalf("pending message was not continued after verification: %q", client.paths)
	}
}

func TestWrongMathAnswerDoesNotContinueConversation(t *testing.T) {
	services, messageStore := newClosedConversationService(t, "verification-wrong.sqlite3")
	defer messageStore.Close()
	services.Cfg.(*config.Config).DisableVerification = false
	if err := messageStore.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 100, Status: "opened"}); err != nil {
		t.Fatalf("open conversation: %v", err)
	}
	if err := messageStore.UpsertVerificationState(&model.VerificationState{
		UserID:            42,
		Answer:            "6",
		PendingMessageIDs: "10",
	}); err != nil {
		t.Fatalf("save verification state: %v", err)
	}
	client := &experienceHTTPClient{}
	telegramBot := newExperienceBot(t, client)
	query := &models.CallbackQuery{
		ID:   "callback-wrong",
		From: models.User{ID: 42, FirstName: "Test"},
		Data: "math_7_42",
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 99, Chat: models.Chat{ID: 42, Type: models.ChatTypePrivate}},
		},
	}
	if err := services.HandleVerificationCallback(context.Background(), telegramBot, query); err != nil {
		t.Fatalf("handle wrong answer: %v", err)
	}
	state, err := messageStore.GetVerificationState(42)
	if err != nil {
		t.Fatalf("load verification state: %v", err)
	}
	if state == nil || state.IsHuman || state.Answer != "" || state.ErrorUntil.IsZero() || state.PendingMessageIDs != "10" {
		t.Fatalf("wrong-answer state = %#v", state)
	}
	for _, requestPath := range client.paths {
		if strings.HasSuffix(requestPath, "/copyMessage") || strings.HasSuffix(requestPath, "/copyMessages") {
			t.Fatalf("wrong answer continued conversation: %q", client.paths)
		}
	}
}
