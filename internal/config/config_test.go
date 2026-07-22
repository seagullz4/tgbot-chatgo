package config

import (
	"strings"
	"testing"
)

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("BOT_TOKEN", "123456:test-token")
	t.Setenv("ADMIN_GROUP_ID", "-1001234567890")
	t.Setenv("ADMIN_USER_IDS", "123456")
	t.Setenv("OWNER_USER_IDS", "654321")
}

func TestLoadRuntimeDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("BOT_WORKERS", "")
	t.Setenv("POLL_TIMEOUT_SECONDS", "")
	t.Setenv("HTTP_MAX_IDLE_PER_HOST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Workers != 4 {
		t.Fatalf("Workers = %d, want 4", cfg.Workers)
	}
	if cfg.PollTimeoutSeconds != 50 {
		t.Fatalf("PollTimeoutSeconds = %d, want 50", cfg.PollTimeoutSeconds)
	}
	if cfg.HTTPMaxIdlePerHost != 16 {
		t.Fatalf("HTTPMaxIdlePerHost = %d, want 16", cfg.HTTPMaxIdlePerHost)
	}
	if !cfg.UserForwardAck {
		t.Fatal("UserForwardAck default = false, want true")
	}
}

func TestLoadDisablesForwardAck(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("USER_FORWARD_ACK", "FALSE")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UserForwardAck {
		t.Fatal("UserForwardAck = true, want false")
	}
}

func TestLoadClampsUnsafeRuntimeValues(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("BOT_WORKERS", "0")
	t.Setenv("POLL_TIMEOUT_SECONDS", "0")
	t.Setenv("HTTP_MAX_IDLE_PER_HOST", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Workers != 1 || cfg.PollTimeoutSeconds != 10 || cfg.HTTPMaxIdlePerHost != 4 {
		t.Fatalf("clamped values = workers:%d poll:%d idle:%d", cfg.Workers, cfg.PollTimeoutSeconds, cfg.HTTPMaxIdlePerHost)
	}
}

func TestValidateRuntimeRejectsZeroWorkers(t *testing.T) {
	cfg := &Config{Workers: 0, PollTimeoutSeconds: 50, HTTPMaxIdlePerHost: 16}
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatal("ValidateRuntime() accepted zero workers")
	}
}

func TestLoadDisablesVerificationWithNewVariable(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DISABLE_VERIFICATION", "TRUE")
	t.Setenv("DISABLE_CAPTCHA", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DisableVerification {
		t.Fatal("DISABLE_VERIFICATION was not applied")
	}
}

func TestLoadSupportsLegacyDisableCaptchaVariable(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("DISABLE_VERIFICATION", "")
	t.Setenv("DISABLE_CAPTCHA", "TRUE")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DisableVerification {
		t.Fatal("legacy DISABLE_CAPTCHA was not applied")
	}
}
func TestConfigRejectsInvalidScalarValues(t *testing.T) {
	base := map[string]string{
		"BOT_TOKEN":      "123456:test-token",
		"ADMIN_GROUP_ID": "-1001234567890",
		"OWNER_USER_IDS": "654321",
		"ADMIN_USER_IDS": "123456",
	}
	tests := []struct {
		key   string
		value string
	}{
		{key: "BOT_WORKERS", value: "many"},
		{key: "USER_FORWARD_ACK", value: "sometimes"},
		{key: "MESSAGE_INTERVAL", value: "-1"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			values := make(map[string]string, len(base)+1)
			for key, value := range base {
				values[key] = value
			}
			values[test.key] = test.value
			if _, err := configFromValues(values); err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("expected %s validation error, got %v", test.key, err)
			}
		})
	}
}

func TestLoadRejectsMultipleOwners(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("OWNER_USER_IDS", "1,2")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted multiple owners")
	}
}

func TestLoadSeparatesOwnerFromOrdinaryAdmins(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("ADMIN_USER_IDS", "123456,654321")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsOwner(654321) || !cfg.IsAdmin(654321) {
		t.Fatal("owner permissions were not preserved")
	}
	if _, duplicate := cfg.AdminUserIDs[654321]; duplicate {
		t.Fatal("owner remained duplicated in ordinary admins")
	}
}

func TestLoadStatusNotifyDefault(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("STATUS_NOTIFY_INTERVAL_MINUTES", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatusNotifyIntervalMinutes != 30 {
		t.Fatalf("StatusNotifyIntervalMinutes = %d, want 30", cfg.StatusNotifyIntervalMinutes)
	}
}

func TestLoadStatusNotifyDisableAndReject(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("STATUS_NOTIFY_INTERVAL_MINUTES", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatusNotifyIntervalMinutes != 0 {
		t.Fatalf("StatusNotifyIntervalMinutes = %d, want 0", cfg.StatusNotifyIntervalMinutes)
	}

	t.Setenv("STATUS_NOTIFY_INTERVAL_MINUTES", "-1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "STATUS_NOTIFY_INTERVAL_MINUTES") {
		t.Fatalf("expected negative interval error, got %v", err)
	}
	t.Setenv("STATUS_NOTIFY_INTERVAL_MINUTES", "10081")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "STATUS_NOTIFY_INTERVAL_MINUTES") {
		t.Fatalf("expected upper bound error, got %v", err)
	}
}

func TestStatusNotifyHoursToMinutes(t *testing.T) {
	minutes, err := StatusNotifyHoursToMinutes("0.5")
	if err != nil || minutes != 30 {
		t.Fatalf("0.5 hours -> %d, err=%v", minutes, err)
	}
	minutes, err = StatusNotifyHoursToMinutes("1")
	if err != nil || minutes != 60 {
		t.Fatalf("1 hour -> %d, err=%v", minutes, err)
	}
	minutes, err = StatusNotifyHoursToMinutes("0")
	if err != nil || minutes != 0 {
		t.Fatalf("0 hours -> %d, err=%v", minutes, err)
	}
	if _, err := StatusNotifyHoursToMinutes("-1"); err == nil {
		t.Fatal("expected negative hours error")
	}
	if FormatStatusNotifyInterval(30) != "每 30 分钟" {
		t.Fatalf("format 30 = %q", FormatStatusNotifyInterval(30))
	}
	if FormatStatusNotifyInterval(60) != "每 1 小时" {
		t.Fatalf("format 60 = %q", FormatStatusNotifyInterval(60))
	}
	if FormatStatusNotifyInterval(0) != "已关闭" {
		t.Fatalf("format 0 = %q", FormatStatusNotifyInterval(0))
	}
}
