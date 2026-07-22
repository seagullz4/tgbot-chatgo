package config

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Provider supplies immutable runtime configuration snapshots.
type Provider interface {
	Current() Config
	Snapshot() *Config
	IsAdmin(userID int64) bool
	IsOwner(userID int64) bool
}

// Config contains runtime settings.
type Config struct {
	BotToken                    string
	AppName                     string
	WelcomeMessage              string
	AdminGroupID                int64
	AdminUserIDs                map[int64]struct{}
	OwnerUserIDs                map[int64]struct{}
	DeleteTopicAsForeverBan     bool
	DeleteUserMessageOnClear    bool
	DisableVerification         bool
	UserForwardAck              bool
	MessageInterval             int
	StatusNotifyIntervalMinutes int
	DatabasePath                string
	Workers                     int
	PollTimeoutSeconds          int
	HTTPMaxIdlePerHost          int
	LogPath                     string
	LogMaxSizeMB                int
	LogMaxBackups               int
}

func (c *Config) Current() Config { return cloneConfig(*c) }

// Snapshot returns the current immutable configuration without allocations.
// Callers must never mutate the returned Config or its maps.
func (c *Config) Snapshot() *Config { return c }
func (c *Config) IsAdmin(userID int64) bool {
	_, admin := c.AdminUserIDs[userID]
	_, owner := c.OwnerUserIDs[userID]
	return admin || owner
}
func (c *Config) IsOwner(userID int64) bool { _, ok := c.OwnerUserIDs[userID]; return ok }

// Manager owns the active snapshot and the writable dotenv file.
type Manager struct {
	path     string
	current  atomic.Pointer[Config]
	mu       sync.Mutex
	lastHash [32]byte
}

func OpenManager() (*Manager, error) {
	path := resolveEnvPath()
	cfg, hash, err := loadFromPath(path)
	if err != nil {
		return nil, err
	}
	manager := &Manager{path: path, lastHash: hash}
	manager.current.Store(&cfg)
	return manager, nil
}

func (m *Manager) Path() string              { return m.path }
func (m *Manager) Current() Config           { return cloneConfig(*m.current.Load()) }
func (m *Manager) Snapshot() *Config         { return m.current.Load() }
func (m *Manager) IsAdmin(userID int64) bool { cfg := m.current.Load(); return cfg.IsAdmin(userID) }
func (m *Manager) IsOwner(userID int64) bool { cfg := m.current.Load(); return cfg.IsOwner(userID) }

// ReadCandidate parses the current file without changing the active snapshot.
func (m *Manager) ReadCandidate() (Config, [32]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return loadFromPath(m.path)
}
func (m *Manager) Changed(hash [32]byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return hash != m.lastHash
}
func (m *Manager) MarkObserved(hash [32]byte) { m.mu.Lock(); m.lastHash = hash; m.mu.Unlock() }
func (m *Manager) Apply(cfg Config, hash [32]byte) {
	copy := cloneConfig(cfg)
	m.current.Store(&copy)
	m.MarkObserved(hash)
}

// Preview builds and validates a candidate by overlaying dotenv values.
func (m *Manager) Preview(updates map[string]string) (Config, error) {
	values, _, err := readDotEnv(m.path)
	if err != nil && !os.IsNotExist(err) {
		return Config{}, err
	}
	for key, value := range updates {
		values[key] = value
	}
	return configFromValues(values)
}

// Write atomically persists selected values while preserving comments and unknown keys.
func (m *Manager) Write(updates map[string]string) ([32]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.path)
	if err != nil && !os.IsNotExist(err) {
		return [32]byte{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	remaining := make(map[string]string, len(updates))
	for key, value := range updates {
		remaining[key] = value
	}
	for index, line := range lines {
		key, _, ok, _ := parseAssignment(line)
		if !ok {
			continue
		}
		if value, exists := remaining[key]; exists {
			lines[index] = key + "=" + encodeValue(value)
			delete(remaining, key)
		}
	}
	keys := make([]string, 0, len(remaining))
	for key := range remaining {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	for _, key := range keys {
		lines = append(lines, key+"="+encodeValue(remaining[key]))
	}
	output := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return [32]byte{}, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(m.path), ".env-*.tmp")
	if err != nil {
		return [32]byte{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return [32]byte{}, err
	}
	if _, err := temporary.WriteString(output); err != nil {
		temporary.Close()
		return [32]byte{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return [32]byte{}, err
	}
	if err := temporary.Close(); err != nil {
		return [32]byte{}, err
	}
	backupPath := m.path + ".bak"
	if len(data) > 0 {
		if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
			return [32]byte{}, err
		}
		if err := os.Rename(m.path, backupPath); err != nil {
			return [32]byte{}, err
		}
		if err := os.Chmod(backupPath, 0o600); err != nil {
			if restoreErr := os.Rename(backupPath, m.path); restoreErr != nil {
				return [32]byte{}, fmt.Errorf("secure backup: %v; restore env: %w", err, restoreErr)
			}
			return [32]byte{}, err
		}
	}
	if err := os.Rename(temporaryName, m.path); err != nil {
		if len(data) > 0 {
			if restoreErr := os.Rename(backupPath, m.path); restoreErr != nil {
				return [32]byte{}, fmt.Errorf("replace env: %v; restore env: %w", err, restoreErr)
			}
		}
		return [32]byte{}, err
	}
	hash := sha256.Sum256([]byte(output))
	m.lastHash = hash
	return hash, nil
}

// Load remains available for tests and static callers.
func Load() (*Config, error) {
	cfg, _, err := loadFromPath(resolveEnvPath())
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func resolveEnvPath() string {
	if explicit := strings.TrimSpace(os.Getenv("ENV_FILE")); explicit != "" {
		absolute, _ := filepath.Abs(explicit)
		return absolute
	}
	for _, candidate := range []string{".env", filepath.Join("..", ".env")} {
		if _, err := os.Stat(candidate); err == nil {
			absolute, _ := filepath.Abs(candidate)
			return absolute
		}
	}
	absolute, _ := filepath.Abs(".env")
	return absolute
}

func loadFromPath(path string) (Config, [32]byte, error) {
	values, data, err := readDotEnv(path)
	hash := sha256.Sum256(data)
	if err != nil && !os.IsNotExist(err) {
		return Config{}, hash, err
	}
	cfg, err := configFromValues(values)
	return cfg, hash, err
}

func configFromValues(values map[string]string) (Config, error) {
	value := func(key, fallback string) string {
		if v, ok := values[key]; ok && strings.TrimSpace(v) != "" {
			return v
		}
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
		return fallback
	}
	cfg := Config{
		BotToken:       value("BOT_TOKEN", ""),
		AppName:        value("APP_NAME", "interactive-bot"),
		WelcomeMessage: value("WELCOME_MESSAGE", "欢迎使用本机器人"),
		DatabasePath:   value("DATABASE_PATH", "data/db.sqlite3"),
		LogPath:        value("LOG_PATH", "logs/bot.log"),
	}
	var err error
	if cfg.DeleteTopicAsForeverBan, err = parseBoolSetting("DELETE_TOPIC_AS_FOREVER_BAN", value("DELETE_TOPIC_AS_FOREVER_BAN", "FALSE")); err != nil {
		return Config{}, err
	}
	if cfg.DeleteUserMessageOnClear, err = parseBoolSetting("DELETE_USER_MESSAGE_ON_CLEAR_CMD", value("DELETE_USER_MESSAGE_ON_CLEAR_CMD", "FALSE")); err != nil {
		return Config{}, err
	}
	if cfg.DisableVerification, err = parseBoolSetting("DISABLE_VERIFICATION/DISABLE_CAPTCHA", value("DISABLE_VERIFICATION", value("DISABLE_CAPTCHA", "FALSE"))); err != nil {
		return Config{}, err
	}
	if cfg.UserForwardAck, err = parseBoolSetting("USER_FORWARD_ACK", value("USER_FORWARD_ACK", "TRUE")); err != nil {
		return Config{}, err
	}
	if cfg.MessageInterval, err = parseIntSetting("MESSAGE_INTERVAL", value("MESSAGE_INTERVAL", "5")); err != nil {
		return Config{}, err
	}
	if cfg.MessageInterval < 0 {
		return Config{}, fmt.Errorf("MESSAGE_INTERVAL must be zero or greater, got %d", cfg.MessageInterval)
	}
	if cfg.StatusNotifyIntervalMinutes, err = parseIntSetting("STATUS_NOTIFY_INTERVAL_MINUTES", value("STATUS_NOTIFY_INTERVAL_MINUTES", "30")); err != nil {
		return Config{}, err
	}
	if cfg.StatusNotifyIntervalMinutes < 0 {
		return Config{}, fmt.Errorf("STATUS_NOTIFY_INTERVAL_MINUTES must be zero or greater, got %d", cfg.StatusNotifyIntervalMinutes)
	}
	if cfg.StatusNotifyIntervalMinutes > 10080 {
		return Config{}, fmt.Errorf("STATUS_NOTIFY_INTERVAL_MINUTES must be at most 10080 (7 days), got %d", cfg.StatusNotifyIntervalMinutes)
	}
	if cfg.Workers, err = parseIntSetting("BOT_WORKERS", value("BOT_WORKERS", "4")); err != nil {
		return Config{}, err
	}
	cfg.Workers = clamp(cfg.Workers, 1, 32)
	if cfg.PollTimeoutSeconds, err = parseIntSetting("POLL_TIMEOUT_SECONDS", value("POLL_TIMEOUT_SECONDS", "50")); err != nil {
		return Config{}, err
	}
	cfg.PollTimeoutSeconds = clamp(cfg.PollTimeoutSeconds, 10, 60)
	if cfg.HTTPMaxIdlePerHost, err = parseIntSetting("HTTP_MAX_IDLE_PER_HOST", value("HTTP_MAX_IDLE_PER_HOST", "16")); err != nil {
		return Config{}, err
	}
	cfg.HTTPMaxIdlePerHost = clamp(cfg.HTTPMaxIdlePerHost, 4, 128)
	if cfg.LogMaxSizeMB, err = parseIntSetting("LOG_MAX_SIZE_MB", value("LOG_MAX_SIZE_MB", "10")); err != nil {
		return Config{}, err
	}
	cfg.LogMaxSizeMB = clamp(cfg.LogMaxSizeMB, 1, 100)
	if cfg.LogMaxBackups, err = parseIntSetting("LOG_MAX_BACKUPS", value("LOG_MAX_BACKUPS", "5")); err != nil {
		return Config{}, err
	}
	cfg.LogMaxBackups = clamp(cfg.LogMaxBackups, 1, 20)
	if cfg.BotToken == "" {
		return Config{}, fmt.Errorf("BOT_TOKEN is required")
	}
	groupID, err := strconv.ParseInt(strings.TrimSpace(value("ADMIN_GROUP_ID", "")), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("ADMIN_GROUP_ID must be int64: %w", err)
	}
	cfg.AdminGroupID = groupID
	cfg.AdminUserIDs, err = parseIDs(value("ADMIN_USER_IDS", ""))
	if err != nil {
		return Config{}, fmt.Errorf("ADMIN_USER_IDS: %w", err)
	}
	ownerRaw := strings.TrimSpace(value("OWNER_USER_IDS", ""))
	if ownerRaw == "" {
		return Config{}, fmt.Errorf("OWNER_USER_IDS must contain exactly one ID")
	}
	cfg.OwnerUserIDs, err = parseIDs(ownerRaw)
	if err != nil {
		return Config{}, fmt.Errorf("OWNER_USER_IDS: %w", err)
	}
	if len(cfg.OwnerUserIDs) != 1 {
		return Config{}, fmt.Errorf("OWNER_USER_IDS must contain exactly one ID")
	}
	for ownerID := range cfg.OwnerUserIDs {
		delete(cfg.AdminUserIDs, ownerID)
	}
	if err := cfg.ValidateRuntime(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) ValidateRuntime() error {
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

func parseIDs(raw string) (map[int64]struct{}, error) {
	ids := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid user ID %q", part)
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}
func FormatIDs(ids map[int64]struct{}) string {
	values := make([]int64, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	parts := make([]string, len(values))
	for i, id := range values {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}
func Values(cfg Config) map[string]string {
	return map[string]string{
		"BOT_TOKEN":                        cfg.BotToken,
		"APP_NAME":                         cfg.AppName,
		"WELCOME_MESSAGE":                  cfg.WelcomeMessage,
		"ADMIN_GROUP_ID":                   strconv.FormatInt(cfg.AdminGroupID, 10),
		"ADMIN_USER_IDS":                   FormatIDs(cfg.AdminUserIDs),
		"OWNER_USER_IDS":                   FormatIDs(cfg.OwnerUserIDs),
		"DELETE_TOPIC_AS_FOREVER_BAN":      strconv.FormatBool(cfg.DeleteTopicAsForeverBan),
		"DELETE_USER_MESSAGE_ON_CLEAR_CMD": strconv.FormatBool(cfg.DeleteUserMessageOnClear),
		"DISABLE_VERIFICATION":             strconv.FormatBool(cfg.DisableVerification),
		"USER_FORWARD_ACK":                 strconv.FormatBool(cfg.UserForwardAck),
		"MESSAGE_INTERVAL":                 strconv.Itoa(cfg.MessageInterval),
		"STATUS_NOTIFY_INTERVAL_MINUTES":   strconv.Itoa(cfg.StatusNotifyIntervalMinutes),
		"DATABASE_PATH":                    cfg.DatabasePath,
		"BOT_WORKERS":                      strconv.Itoa(cfg.Workers),
		"POLL_TIMEOUT_SECONDS":             strconv.Itoa(cfg.PollTimeoutSeconds),
		"HTTP_MAX_IDLE_PER_HOST":           strconv.Itoa(cfg.HTTPMaxIdlePerHost),
		"LOG_PATH":                         cfg.LogPath,
		"LOG_MAX_SIZE_MB":                  strconv.Itoa(cfg.LogMaxSizeMB),
		"LOG_MAX_BACKUPS":                  strconv.Itoa(cfg.LogMaxBackups),
	}
}

func cloneConfig(cfg Config) Config {
	cfg.AdminUserIDs = cloneIDs(cfg.AdminUserIDs)
	cfg.OwnerUserIDs = cloneIDs(cfg.OwnerUserIDs)
	return cfg
}
func cloneIDs(input map[int64]struct{}) map[int64]struct{} {
	output := make(map[int64]struct{}, len(input))
	for id := range input {
		output[id] = struct{}{}
	}
	return output
}
func parseBoolSetting(key, value string) (bool, error) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be TRUE or FALSE, got %q", key, value)
	}
	return parsed, nil
}
func parseIntSetting(key, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, value)
	}
	return parsed, nil
}
func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// FormatStatusNotifyInterval renders a human-readable status-notify interval.
func FormatStatusNotifyInterval(minutes int) string {
	if minutes <= 0 {
		return "已关闭"
	}
	if minutes%60 == 0 {
		hours := minutes / 60
		if hours == 1 {
			return "每 1 小时"
		}
		return fmt.Sprintf("每 %d 小时", hours)
	}
	if minutes < 60 {
		return fmt.Sprintf("每 %d 分钟", minutes)
	}
	hours := float64(minutes) / 60
	return fmt.Sprintf("每 %.1f 小时（%d 分钟）", hours, minutes)
}

// StatusNotifyHoursToMinutes converts hour input from the ops panel into minutes.
func StatusNotifyHoursToMinutes(raw string) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("STATUS_NOTIFY_INTERVAL_MINUTES interval is required")
	}
	hours, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("STATUS_NOTIFY_INTERVAL_MINUTES must be a number of hours, got %q", raw)
	}
	if hours < 0 {
		return 0, fmt.Errorf("STATUS_NOTIFY_INTERVAL_MINUTES must be zero or greater, got %v", hours)
	}
	minutes := int(math.Round(hours * 60))
	if minutes > 10080 {
		return 0, fmt.Errorf("STATUS_NOTIFY_INTERVAL_MINUTES must be at most 10080 (7 days), got %d", minutes)
	}
	return minutes, nil
}

func readDotEnv(path string) (map[string]string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, nil, err
	}
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		key, value, ok, err := parseAssignment(scanner.Text())
		if err != nil {
			return nil, data, fmt.Errorf("parse %s line %d: %w", path, lineNumber, err)
		}
		if ok {
			values[key] = value
		}
	}
	return values, data, scanner.Err()
}
func parseAssignment(line string) (string, string, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false, nil
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")
	index := strings.IndexByte(trimmed, '=')
	if index <= 0 {
		return "", "", false, nil
	}
	key := strings.TrimSpace(trimmed[:index])
	raw := strings.TrimSpace(trimmed[index+1:])
	value, err := decodeValue(raw)
	if err != nil {
		return "", "", false, fmt.Errorf("invalid value for %s: %w", key, err)
	}
	return key, value, true, nil
}
func decodeValue(raw string) (string, error) {
	if strings.HasPrefix(raw, "\"") {
		if len(raw) < 2 || raw[len(raw)-1] != '"' {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		return strconv.Unquote(raw)
	}
	if strings.HasPrefix(raw, "'") {
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return raw[1 : len(raw)-1], nil
	}
	return raw, nil
}
func encodeValue(value string) string {
	if value == "" {
		return "\"\""
	}
	if strings.ContainsAny(value, " #\\\"'\n\r\t") {
		return strconv.Quote(value)
	}
	return value
}
