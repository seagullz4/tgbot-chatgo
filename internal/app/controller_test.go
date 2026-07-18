package app

import (
	"errors"
	"strings"
	"testing"

	"telegram-interactive-bot/go-bot/internal/config"
)

func TestRedactTelegramErrorRemovesCandidateToken(t *testing.T) {
	token := "123456:short-test-secret"
	err := redactTelegramError(errors.New("Post https://api.telegram.org/bot"+token+"/getMe: EOF"), token)
	if strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("token was not redacted: %v", err)
	}
}

func TestRollbackSessionConfigRestoresGroupWhenTokenChanges(t *testing.T) {
	old := config.Config{BotToken: "old", AdminGroupID: -1001, Workers: 4, PollTimeoutSeconds: 50, HTTPMaxIdlePerHost: 16}
	candidate := old
	candidate.BotToken = "new"
	candidate.AdminGroupID = -1002
	latest := candidate
	latest.WelcomeMessage = "new welcome"

	rolledBack, updates := rollbackSessionConfig(latest, old, candidate)
	if rolledBack.BotToken != old.BotToken || rolledBack.AdminGroupID != old.AdminGroupID {
		t.Fatalf("rollback left an incoherent bot/group pair: %+v", rolledBack)
	}
	if rolledBack.WelcomeMessage != latest.WelcomeMessage {
		t.Fatal("unrelated hot configuration was not preserved")
	}
	if updates["ADMIN_GROUP_ID"] != "-1001" {
		t.Fatalf("ADMIN_GROUP_ID rollback update = %q", updates["ADMIN_GROUP_ID"])
	}
}

func TestRollbackSessionConfigKeepsValidatedGroupForWorkerRestart(t *testing.T) {
	old := config.Config{BotToken: "same", AdminGroupID: -1001, Workers: 4, PollTimeoutSeconds: 50, HTTPMaxIdlePerHost: 16}
	candidate := old
	candidate.AdminGroupID = -1002
	candidate.Workers = 8

	rolledBack, updates := rollbackSessionConfig(candidate, old, candidate)
	if rolledBack.AdminGroupID != candidate.AdminGroupID {
		t.Fatalf("worker-only rollback changed the validated group: %d", rolledBack.AdminGroupID)
	}
	if _, exists := updates["ADMIN_GROUP_ID"]; exists {
		t.Fatal("worker-only rollback unexpectedly rewrote ADMIN_GROUP_ID")
	}
}
