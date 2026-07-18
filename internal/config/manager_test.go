package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerWritePreservesDotEnvAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	initial := "# keep this comment\nUNKNOWN_KEY=keep-me\nBOT_TOKEN=123456:test\nADMIN_GROUP_ID=-100123\nADMIN_USER_IDS=20,10\nOWNER_USER_IDS=10\nWELCOME_MESSAGE=hello\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", path)
	manager, err := OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.Preview(map[string]string{"WELCOME_MESSAGE": "第一行\n第二行", "ADMIN_USER_IDS": "30,20"})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := manager.Write(map[string]string{"WELCOME_MESSAGE": candidate.WelcomeMessage, "ADMIN_USER_IDS": FormatIDs(candidate.AdminUserIDs)})
	if err != nil {
		t.Fatal(err)
	}
	manager.Apply(candidate, hash)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "# keep this comment") || !strings.Contains(text, "UNKNOWN_KEY=keep-me") {
		t.Fatalf("dotenv content not preserved: %s", text)
	}
	if !strings.Contains(text, "WELCOME_MESSAGE=\"第一行\\n第二行\"") {
		t.Fatalf("welcome was not escaped: %s", text)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	reloaded, _, err := manager.ReadCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WelcomeMessage != "第一行\n第二行" || FormatIDs(reloaded.AdminUserIDs) != "20,30" {
		t.Fatalf("reloaded config = %+v", reloaded)
	}
}

func TestManagerRequiresExplicitSingleOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	data := "BOT_TOKEN=123456:test\nADMIN_GROUP_ID=-100123\nADMIN_USER_IDS=99,42\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", path)
	if _, err := OpenManager(); err == nil || !strings.Contains(err.Error(), "exactly one ID") {
		t.Fatalf("expected explicit owner error, got %v", err)
	}
}
func TestMalformedDotEnvChangesKeepDistinctHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	initial := "BOT_TOKEN=123456:test\nADMIN_GROUP_ID=-100123\nOWNER_USER_IDS=99\nWELCOME_MESSAGE=hello\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", path)
	manager, err := OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Replace(initial, "WELCOME_MESSAGE=hello", "WELCOME_MESSAGE=\"first", 1)
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	_, firstHash, err := manager.ReadCandidate()
	if err == nil {
		t.Fatal("first malformed file was accepted")
	}
	manager.MarkObserved(firstHash)
	second := strings.Replace(initial, "WELCOME_MESSAGE=hello", "WELCOME_MESSAGE=\"second", 1)
	if err := os.WriteFile(path, []byte(second), 0o600); err != nil {
		t.Fatal(err)
	}
	_, secondHash, err := manager.ReadCandidate()
	if err == nil {
		t.Fatal("second malformed file was accepted")
	}
	if firstHash == secondHash || !manager.Changed(secondHash) {
		t.Fatal("distinct malformed file contents were not detected as a new change")
	}
}

func TestMalformedDotEnvDoesNotReplaceActiveSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	initial := "BOT_TOKEN=123456:test\nADMIN_GROUP_ID=-100123\nADMIN_USER_IDS=99\nOWNER_USER_IDS=99\nWELCOME_MESSAGE=hello\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", path)
	manager, err := OpenManager()
	if err != nil {
		t.Fatal(err)
	}
	before := manager.Current()
	malformed := strings.Replace(initial, "WELCOME_MESSAGE=hello", "WELCOME_MESSAGE=\"unterminated", 1)
	if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ReadCandidate(); err == nil || !strings.Contains(err.Error(), "line 5") {
		t.Fatalf("expected line-numbered parse error, got %v", err)
	}
	after := manager.Current()
	if after.WelcomeMessage != before.WelcomeMessage || after.BotToken != before.BotToken {
		t.Fatalf("active snapshot changed: before=%+v after=%+v", before, after)
	}
}

var benchmarkConfig Config
var benchmarkSnapshot *Config

func BenchmarkManagerConfigReads(b *testing.B) {
	path := filepath.Join(b.TempDir(), ".env")
	data := "BOT_TOKEN=123456:test\nADMIN_GROUP_ID=-100123\nADMIN_USER_IDS=20,10\nOWNER_USER_IDS=10\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		b.Fatal(err)
	}
	b.Setenv("ENV_FILE", path)
	manager, err := OpenManager()
	if err != nil {
		b.Fatal(err)
	}
	b.Run("CurrentClone", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			benchmarkConfig = manager.Current()
		}
	})
	b.Run("ImmutableSnapshot", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			benchmarkSnapshot = manager.Snapshot()
		}
	})
}
