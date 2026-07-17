package config

import "testing"

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("BOT_TOKEN", "123456:test-token")
	t.Setenv("ADMIN_GROUP_ID", "-1001234567890")
	t.Setenv("ADMIN_USER_IDS", "123456")
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
