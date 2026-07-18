package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"telegram-interactive-bot/go-bot/internal/model"
)

func TestEnsureUserConcurrent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "bot.sqlite3"), 8)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	const goroutines = 24
	var waitGroup sync.WaitGroup
	errors := make(chan error, goroutines)
	for index := 0; index < goroutines; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, ensureErr := store.EnsureUser(&model.User{
				UserID:    123456789,
				FirstName: "Concurrent",
				Username:  "same_user",
			})
			errors <- ensureErr
		}()
	}
	waitGroup.Wait()
	close(errors)
	for ensureErr := range errors {
		if ensureErr != nil {
			t.Fatalf("concurrent EnsureUser failed: %v", ensureErr)
		}
	}

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("got %d users, want 1", len(users))
	}
}

func TestMessageTextMigrationAndUpdates(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy.sqlite3")
	legacyDB, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	_, err = legacyDB.Exec(`CREATE TABLE message_map (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_chat_message_id INTEGER NOT NULL,
		group_chat_message_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatalf("create legacy message_map: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	store, err := Open(databasePath, 4)
	if err != nil {
		t.Fatalf("open migrated sqlite: %v", err)
	}
	defer store.Close()

	mapping := &model.MessageMap{
		UserChatMessageID:  10,
		GroupChatMessageID: 20,
		UserID:             30,
		MessageText:        "你好",
	}
	if err := store.SaveMessageMap(mapping); err != nil {
		t.Fatalf("save mapping: %v", err)
	}
	if err := store.UpdateMessageTextByUserMessageID(30, 10, "我好"); err != nil {
		t.Fatalf("update by user message id: %v", err)
	}
	got, err := store.GetByUserMessageID(30, 10)
	if err != nil {
		t.Fatalf("get by user message id: %v", err)
	}
	if got == nil || got.MessageText != "我好" {
		t.Fatalf("message text after user edit = %#v", got)
	}

	if err := store.UpdateMessageTextByGroupMessageID(20, "大家好"); err != nil {
		t.Fatalf("update by group message id: %v", err)
	}
	got, err = store.GetByGroupMessageID(20)
	if err != nil {
		t.Fatalf("get by group message id: %v", err)
	}
	if got == nil || got.MessageText != "大家好" {
		t.Fatalf("message text after group edit = %#v", got)
	}
}

func TestBanAndPendingStatePersistence(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.EnsureUser(&model.User{UserID: 99, FirstName: "Banned"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserBanned(99, true, "abuse"); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByTelegramID(99)
	if err != nil {
		t.Fatal(err)
	}
	if user == nil || !user.IsBanned || user.BanReason != "abuse" {
		t.Fatalf("user = %#v", user)
	}
	state := &model.VerificationState{UserID: 99, Answer: "42", PendingMessageIDs: "1,2,3"}
	if err := store.UpsertVerificationState(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetVerificationState(99)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.PendingMessageIDs != "1,2,3" {
		t.Fatalf("captcha state = %#v", loaded)
	}
}

func TestLegacyCaptchaStateRemainsCompatibleWithVerificationState(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy-verification.sqlite3")
	legacyDB, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open legacy sqlite: %v", err)
	}
	_, err = legacyDB.Exec(`CREATE TABLE captcha_state (
		user_id INTEGER PRIMARY KEY,
		code TEXT,
		is_human INTEGER DEFAULT 0,
		error_until TEXT,
		last_msg_at TEXT,
		media_group_id TEXT,
		pending_message_ids TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("create legacy verification table: %v", err)
	}
	if _, err := legacyDB.Exec(`INSERT INTO captcha_state(user_id, code, media_group_id, pending_message_ids) VALUES(99, '8', 'legacy', '1,2')`); err != nil {
		t.Fatalf("insert legacy verification state: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	store, err := Open(databasePath, 4)
	if err != nil {
		t.Fatalf("open migrated sqlite: %v", err)
	}
	defer store.Close()
	state, err := store.GetVerificationState(99)
	if err != nil {
		t.Fatalf("load migrated verification state: %v", err)
	}
	if state == nil || state.Answer != "8" || state.PendingMessageIDs != "1,2" {
		t.Fatalf("migrated verification state = %#v", state)
	}
	state.Answer = "9"
	if err := store.UpsertVerificationState(state); err != nil {
		t.Fatalf("update migrated verification state: %v", err)
	}
	state, err = store.GetVerificationState(99)
	if err != nil || state == nil || state.Answer != "9" {
		t.Fatalf("updated verification state = %#v err=%v", state, err)
	}
}

func TestOpenCreatesMissingParentDirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "nested", "data", "bot.sqlite3")
	store, err := Open(databasePath, 4)
	if err != nil {
		t.Fatalf("open sqlite in missing directory: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(databasePath)); err != nil {
		t.Fatalf("database parent directory was not created: %v", err)
	}
}

func TestUserMessageMappingsAreScopedByUser(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "mapping-scope.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := &model.MessageMap{UserID: 101, UserChatMessageID: 7, GroupChatMessageID: 1001, MessageText: "first"}
	second := &model.MessageMap{UserID: 202, UserChatMessageID: 7, GroupChatMessageID: 2002, MessageText: "second"}
	if err := store.SaveMessageMap(first); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageMap(second); err != nil {
		t.Fatal(err)
	}
	gotFirst, err := store.GetByUserMessageID(101, 7)
	if err != nil || gotFirst == nil || gotFirst.GroupChatMessageID != 1001 {
		t.Fatalf("first mapping = %#v, err = %v", gotFirst, err)
	}
	gotSecond, err := store.GetByUserMessageID(202, 7)
	if err != nil || gotSecond == nil || gotSecond.GroupChatMessageID != 2002 {
		t.Fatalf("second mapping = %#v, err = %v", gotSecond, err)
	}
	if err := store.UpdateMessageTextByUserMessageID(101, 7, "updated"); err != nil {
		t.Fatal(err)
	}
	gotSecond, err = store.GetByUserMessageID(202, 7)
	if err != nil || gotSecond == nil || gotSecond.MessageText != "second" {
		t.Fatalf("second mapping was modified: %#v, err = %v", gotSecond, err)
	}
}

func TestResetConversationRouting(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "reset-routing.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.EnsureUser(&model.User{UserID: 42, MessageThreadID: 77}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateUserThreadID(42, 77); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertForumStatus(&model.ForumStatus{ChatID: -100, MessageThreadID: 77, Status: "opened"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageMap(&model.MessageMap{UserChatMessageID: 1, GroupChatMessageID: 2, UserID: 42}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMediaGroupMessage(&model.MediaGroupMessage{ChatID: 42, MessageID: 3, MediaGroupID: "album"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetConversationRouting(); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByTelegramID(42)
	if err != nil {
		t.Fatal(err)
	}
	if user.MessageThreadID != 0 {
		t.Fatalf("thread id = %d", user.MessageThreadID)
	}
	status, err := store.GetForumStatus(77)
	if err != nil {
		t.Fatal(err)
	}
	if status != nil {
		t.Fatalf("status remains: %+v", status)
	}
	mapping, err := store.GetByUserMessageID(42, 1)
	if err != nil {
		t.Fatal(err)
	}
	if mapping != nil {
		t.Fatalf("mapping remains: %+v", mapping)
	}
	media, err := store.ListMediaGroupMessages(42, "album")
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 0 {
		t.Fatalf("media remains: %+v", media)
	}
}
