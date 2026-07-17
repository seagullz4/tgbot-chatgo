package service

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"telegram-interactive-bot/go-bot/internal/config"
	"telegram-interactive-bot/go-bot/internal/job"
	"telegram-interactive-bot/go-bot/internal/model"
	storesqlite "telegram-interactive-bot/go-bot/internal/store/sqlite"
)

func TestBanUserRejectsSelfAndAdmins(t *testing.T) {
	store, err := storesqlite.Open(filepath.Join(t.TempDir(), "ban.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := New(&config.Config{
		AdminGroupID: -100,
		AdminUserIDs: map[int64]struct{}{1001: {}, 1002: {}},
	}, store, job.New(), logger)

	if _, err := store.EnsureUser(&model.User{UserID: 1001, FirstName: "AdminA"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUser(&model.User{UserID: 1002, FirstName: "AdminB"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUser(&model.User{UserID: 2001, FirstName: "User"}); err != nil {
		t.Fatal(err)
	}

	if err := services.BanUser(context.Background(), nil, 1001, "test", 1001); err == nil || !strings.Contains(err.Error(), "自己") {
		t.Fatalf("self ban error = %v", err)
	}
	if err := services.BanUser(context.Background(), nil, 1002, "test", 1001); err == nil || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("admin ban error = %v", err)
	}
}

func TestListBannedUsersText(t *testing.T) {
	store, err := storesqlite.Open(filepath.Join(t.TempDir(), "banned-list.sqlite3"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services := New(&config.Config{AdminUserIDs: map[int64]struct{}{1: {}}}, store, job.New(), logger)
	if _, err := store.EnsureUser(&model.User{UserID: 9, FirstName: "Blocked", Username: "blocked_user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureUser(&model.User{UserID: 1, FirstName: "LegacyAdmin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserBanned(9, true, "spam"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUserBanned(1, true, "legacy data"); err != nil {
		t.Fatal(err)
	}
	text, err := services.ListBannedUsersText()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "9") || !strings.Contains(text, "spam") || !strings.Contains(text, "/unban 9") {
		t.Fatalf("banned list text = %q", text)
	}
	if strings.Contains(text, "LegacyAdmin") || strings.Contains(text, "/unban 1") {
		t.Fatalf("admin must not appear in banned list: %q", text)
	}
}
