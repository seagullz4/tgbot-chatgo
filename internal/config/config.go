package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config contains runtime settings loaded from environment variables.
type Config struct {
	BotToken                 string
	AppName                  string
	WelcomeMessage           string
	AdminGroupID             int64
	AdminUserIDs             map[int64]struct{}
	DeleteTopicAsForeverBan  bool
	DeleteUserMessageOnClear bool
	DisableVerification      bool
	UserForwardAck           bool
	MessageInterval          int
	DatabasePath             string
	Workers                  int
	PollTimeoutSeconds       int
	HTTPMaxIdlePerHost       int
}

func Load() (*Config, error) {
	_ = loadDotEnv(".env")
	_ = loadDotEnv("../.env")

	cfg := &Config{
		BotToken:                 os.Getenv("BOT_TOKEN"),
		AppName:                  envOr("APP_NAME", "interactive-bot"),
		WelcomeMessage:           envOr("WELCOME_MESSAGE", "欢迎使用本机器人"),
		DeleteTopicAsForeverBan:  envBool("DELETE_TOPIC_AS_FOREVER_BAN"),
		DeleteUserMessageOnClear: envBool("DELETE_USER_MESSAGE_ON_CLEAR_CMD"),
		DisableVerification:      envBool("DISABLE_VERIFICATION") || envBool("DISABLE_CAPTCHA"),
		UserForwardAck:           envBoolDefault("USER_FORWARD_ACK", true),
		MessageInterval:          envInt("MESSAGE_INTERVAL", 5),
		DatabasePath:             envOr("DATABASE_PATH", "data/db.sqlite3"),
		Workers:                  envIntRange("BOT_WORKERS", 4, 1, 32),
		PollTimeoutSeconds:       envIntRange("POLL_TIMEOUT_SECONDS", 50, 10, 60),
		HTTPMaxIdlePerHost:       envIntRange("HTTP_MAX_IDLE_PER_HOST", 16, 4, 128),
	}

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}

	adminGroup := strings.TrimSpace(os.Getenv("ADMIN_GROUP_ID"))
	if adminGroup == "" {
		return nil, fmt.Errorf("ADMIN_GROUP_ID is required")
	}
	groupID, err := strconv.ParseInt(adminGroup, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ADMIN_GROUP_ID must be int64: %w", err)
	}
	cfg.AdminGroupID = groupID

	adminUsersRaw := strings.TrimSpace(os.Getenv("ADMIN_USER_IDS"))
	if adminUsersRaw == "" {
		return nil, fmt.Errorf("ADMIN_USER_IDS is required")
	}
	cfg.AdminUserIDs = make(map[int64]struct{})
	for _, part := range strings.Split(adminUsersRaw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ADMIN_USER_IDS must be comma-separated int64: %w", err)
		}
		cfg.AdminUserIDs[id] = struct{}{}
	}
	if len(cfg.AdminUserIDs) == 0 {
		return nil, fmt.Errorf("ADMIN_USER_IDS is empty")
	}
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) ValidateRuntime() error {
	if c.Workers < 1 || c.Workers > 32 {
		return fmt.Errorf("BOT_WORKERS must be between 1 and 32, got %d", c.Workers)
	}
	if c.PollTimeoutSeconds < 10 || c.PollTimeoutSeconds > 60 {
		return fmt.Errorf("POLL_TIMEOUT_SECONDS must be between 10 and 60, got %d", c.PollTimeoutSeconds)
	}
	if c.HTTPMaxIdlePerHost < 4 || c.HTTPMaxIdlePerHost > 128 {
		return fmt.Errorf("HTTP_MAX_IDLE_PER_HOST must be between 4 and 128, got %d", c.HTTPMaxIdlePerHost)
	}
	return nil
}

func (c *Config) IsAdmin(userID int64) bool {
	_, ok := c.AdminUserIDs[userID]
	return ok
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "TRUE")
}

func envBoolDefault(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return strings.EqualFold(value, "TRUE")
}

func envIntRange(key string, fallback, minimum, maximum int) int {
	value := envInt(key, fallback)
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// loadDotEnv is a minimal KEY=VALUE parser (no external dependency).
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// support export KEY=VAL
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
	return scanner.Err()
}
