package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"telegram-interactive-bot/go-bot/internal/model"
	"telegram-interactive-bot/go-bot/internal/store"

	_ "modernc.org/sqlite"
)

type SQLite struct {
	db *sql.DB
}

func Open(path string, maxOpenConnections int) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dsn := path + separator + "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if maxOpenConnections < 2 {
		maxOpenConnections = 2
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &SQLite{db: db}
	if err := s.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

func (s *SQLite) Migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS user (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL UNIQUE,
	first_name TEXT,
	last_name TEXT,
	username TEXT,
	is_premium INTEGER DEFAULT 0,
	message_thread_id INTEGER DEFAULT 0,
	is_banned INTEGER DEFAULT 0,
	ban_reason TEXT NOT NULL DEFAULT '',
	updated_at TEXT
);
CREATE TABLE IF NOT EXISTS message_map (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_chat_message_id INTEGER NOT NULL,
	group_chat_message_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	message_text TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS formn_status (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id INTEGER NOT NULL,
	message_thread_id INTEGER NOT NULL UNIQUE,
	status TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS media_group_message (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	media_group_id TEXT NOT NULL,
	is_header INTEGER DEFAULT 0,
	caption_html TEXT
);
CREATE TABLE IF NOT EXISTS captcha_state (
	user_id INTEGER PRIMARY KEY,
	code TEXT,
	is_human INTEGER DEFAULT 0,
	error_until TEXT,
	last_msg_at TEXT,
	pending_message_ids TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_message_map_user_msg ON message_map(user_chat_message_id);
CREATE INDEX IF NOT EXISTS idx_message_map_user_message ON message_map(user_id, user_chat_message_id);
CREATE INDEX IF NOT EXISTS idx_message_map_group_msg ON message_map(group_chat_message_id);
CREATE INDEX IF NOT EXISTS idx_message_map_user ON message_map(user_id);
CREATE INDEX IF NOT EXISTS idx_media_group ON media_group_message(chat_id, media_group_id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	columns := []struct {
		table, name, definition string
	}{
		{"message_map", "message_text", "TEXT NOT NULL DEFAULT ''"},
		{"user", "is_banned", "INTEGER DEFAULT 0"},
		{"user", "ban_reason", "TEXT NOT NULL DEFAULT ''"},
		{"captcha_state", "pending_message_ids", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := s.ensureColumn(column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLite) EnsureUser(u *model.User) (*model.User, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO user(user_id, first_name, last_name, username, is_premium, message_thread_id, updated_at)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   first_name = excluded.first_name,
		   last_name = excluded.last_name,
		   username = excluded.username,
		   is_premium = excluded.is_premium,
		   updated_at = excluded.updated_at`,
		u.UserID, u.FirstName, u.LastName, u.Username, boolToInt(u.IsPremium), u.MessageThreadID, now,
	)
	if err != nil {
		return nil, err
	}
	return s.GetUserByTelegramID(u.UserID)
}
func (s *SQLite) GetUserByTelegramID(userID int64) (*model.User, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, first_name, last_name, username, is_premium, message_thread_id, is_banned, ban_reason, updated_at
		 FROM user WHERE user_id = ?`, userID,
	)
	return scanUser(row)
}

func (s *SQLite) GetUserByThreadID(threadID int) (*model.User, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, first_name, last_name, username, is_premium, message_thread_id, is_banned, ban_reason, updated_at
		 FROM user WHERE message_thread_id = ?`, threadID,
	)
	return scanUser(row)
}

func (s *SQLite) UpdateUserThreadID(userID int64, threadID int) error {
	_, err := s.db.Exec(
		`UPDATE user SET message_thread_id = ?, updated_at = ? WHERE user_id = ?`,
		threadID, time.Now().UTC().Format(time.RFC3339), userID,
	)
	return err
}

func (s *SQLite) ResetConversationRouting() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		"UPDATE user SET message_thread_id = 0",
		"DELETE FROM formn_status",
		"DELETE FROM message_map",
		"DELETE FROM media_group_message",
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) SetUserBanned(userID int64, banned bool, reason string) error {
	_, err := s.db.Exec(
		`UPDATE user SET is_banned = ?, ban_reason = ?, updated_at = ? WHERE user_id = ?`,
		boolToInt(banned), reason, time.Now().UTC().Format(time.RFC3339), userID,
	)
	return err
}

func (s *SQLite) ListBannedUsers() ([]*model.User, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, first_name, last_name, username, is_premium, message_thread_id, is_banned, ban_reason, updated_at FROM user WHERE is_banned = 1 ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		if u != nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

func (s *SQLite) ListUsers() ([]*model.User, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, first_name, last_name, username, is_premium, message_thread_id, is_banned, ban_reason, updated_at FROM user`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.User
	for rows.Next() {
		u, err := scanUserRows(rows)
		if err != nil {
			return nil, err
		}
		if u != nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

func (s *SQLite) SaveMessageMap(m *model.MessageMap) error {
	res, err := s.db.Exec(
		`INSERT INTO message_map(user_chat_message_id, group_chat_message_id, user_id, message_text) VALUES(?,?,?,?)`,
		m.UserChatMessageID, m.GroupChatMessageID, m.UserID, m.MessageText,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	m.ID = id
	return nil
}

func (s *SQLite) GetByUserMessageID(userID int64, userMessageID int) (*model.MessageMap, error) {
	row := s.db.QueryRow(
		`SELECT id, user_chat_message_id, group_chat_message_id, user_id, message_text FROM message_map WHERE user_id = ? AND user_chat_message_id = ?`,
		userID, userMessageID,
	)
	return scanMessageMap(row)
}

func (s *SQLite) GetByGroupMessageID(groupMessageID int) (*model.MessageMap, error) {
	row := s.db.QueryRow(
		`SELECT id, user_chat_message_id, group_chat_message_id, user_id, message_text FROM message_map WHERE group_chat_message_id = ?`,
		groupMessageID,
	)
	return scanMessageMap(row)
}

func (s *SQLite) UpdateMessageTextByUserMessageID(userID int64, userMessageID int, text string) error {
	_, err := s.db.Exec(`UPDATE message_map SET message_text = ? WHERE user_id = ? AND user_chat_message_id = ?`, text, userID, userMessageID)
	return err
}

func (s *SQLite) UpdateMessageTextByGroupMessageID(groupMessageID int, text string) error {
	_, err := s.db.Exec(`UPDATE message_map SET message_text = ? WHERE group_chat_message_id = ?`, text, groupMessageID)
	return err
}

func (s *SQLite) ListMessageMapsByUser(userID int64) ([]*model.MessageMap, error) {
	rows, err := s.db.Query(
		`SELECT id, user_chat_message_id, group_chat_message_id, user_id, message_text FROM message_map WHERE user_id = ?`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.MessageMap
	for rows.Next() {
		var m model.MessageMap
		if err := rows.Scan(&m.ID, &m.UserChatMessageID, &m.GroupChatMessageID, &m.UserID, &m.MessageText); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteMessageMapsByUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM message_map WHERE user_id = ?`, userID)
	return err
}

func (s *SQLite) CountMessageMapsByUser(userID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM message_map WHERE user_id = ?`, userID).Scan(&count)
	return count, err
}

func (s *SQLite) UpsertForumStatus(st *model.ForumStatus) error {
	_, err := s.db.Exec(
		`INSERT INTO formn_status(chat_id, message_thread_id, status) VALUES(?,?,?)
		 ON CONFLICT(message_thread_id) DO UPDATE SET status = excluded.status, chat_id = excluded.chat_id`,
		st.ChatID, st.MessageThreadID, st.Status,
	)
	return err
}

func (s *SQLite) GetForumStatus(threadID int) (*model.ForumStatus, error) {
	row := s.db.QueryRow(
		`SELECT id, chat_id, message_thread_id, status FROM formn_status WHERE message_thread_id = ?`,
		threadID,
	)
	var st model.ForumStatus
	err := row.Scan(&st.ID, &st.ChatID, &st.MessageThreadID, &st.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *SQLite) DeleteForumStatus(threadID int) error {
	_, err := s.db.Exec(`DELETE FROM formn_status WHERE message_thread_id = ?`, threadID)
	return err
}

func (s *SQLite) SaveMediaGroupMessage(m *model.MediaGroupMessage) error {
	res, err := s.db.Exec(
		`INSERT INTO media_group_message(chat_id, message_id, media_group_id, is_header, caption_html)
		 VALUES(?,?,?,?,?)`,
		m.ChatID, m.MessageID, m.MediaGroupID, boolToInt(m.IsHeader), m.CaptionHTML,
	)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	m.ID = id
	return nil
}

func (s *SQLite) ListMediaGroupMessages(chatID int64, mediaGroupID string) ([]*model.MediaGroupMessage, error) {
	rows, err := s.db.Query(
		`SELECT id, chat_id, message_id, media_group_id, is_header, caption_html
		 FROM media_group_message WHERE chat_id = ? AND media_group_id = ? ORDER BY id ASC`,
		chatID, mediaGroupID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.MediaGroupMessage
	for rows.Next() {
		var m model.MediaGroupMessage
		var header int
		if err := rows.Scan(&m.ID, &m.ChatID, &m.MessageID, &m.MediaGroupID, &header, &m.CaptionHTML); err != nil {
			return nil, err
		}
		m.IsHeader = header == 1
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteMediaGroupMessages(chatID int64, mediaGroupID string) error {
	_, err := s.db.Exec(
		`DELETE FROM media_group_message WHERE chat_id = ? AND media_group_id = ?`,
		chatID, mediaGroupID,
	)
	return err
}

func (s *SQLite) GetVerificationState(userID int64) (*model.VerificationState, error) {
	row := s.db.QueryRow(
		`SELECT user_id, code, is_human, error_until, last_msg_at, pending_message_ids FROM captcha_state WHERE user_id = ?`,
		userID,
	)
	var st model.VerificationState
	var isHuman int
	var errorUntil, lastMsgAt sql.NullString
	err := row.Scan(&st.UserID, &st.Answer, &isHuman, &errorUntil, &lastMsgAt, &st.PendingMessageIDs)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	st.IsHuman = isHuman == 1
	if errorUntil.Valid && errorUntil.String != "" {
		if t, e := time.Parse(time.RFC3339, errorUntil.String); e == nil {
			st.ErrorUntil = t
		}
	}
	if lastMsgAt.Valid && lastMsgAt.String != "" {
		if t, e := time.Parse(time.RFC3339, lastMsgAt.String); e == nil {
			st.LastMsgAt = t
		}
	}
	return &st, nil
}

func (s *SQLite) UpsertVerificationState(st *model.VerificationState) error {
	_, err := s.db.Exec(
		`INSERT INTO captcha_state(user_id, code, is_human, error_until, last_msg_at, pending_message_ids)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   code = excluded.code,
		   is_human = excluded.is_human,
		   error_until = excluded.error_until,
		   last_msg_at = excluded.last_msg_at,
		   pending_message_ids = excluded.pending_message_ids`,
		st.UserID, st.Answer, boolToInt(st.IsHuman),
		formatTime(st.ErrorUntil), formatTime(st.LastMsgAt), st.PendingMessageIDs,
	)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (*model.User, error) {
	var u model.User
	var premium, banned int
	var updated sql.NullString
	err := row.Scan(&u.ID, &u.UserID, &u.FirstName, &u.LastName, &u.Username, &premium, &u.MessageThreadID, &banned, &u.BanReason, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.IsPremium = premium == 1
	u.IsBanned = banned == 1
	if updated.Valid {
		if t, e := time.Parse(time.RFC3339, updated.String); e == nil {
			u.UpdatedAt = t
		}
	}
	return &u, nil
}

func scanUserRows(rows *sql.Rows) (*model.User, error) {
	return scanUser(rows)
}

func scanMessageMap(row rowScanner) (*model.MessageMap, error) {
	var m model.MessageMap
	err := row.Scan(&m.ID, &m.UserChatMessageID, &m.GroupChatMessageID, &m.UserID, &m.MessageText)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *SQLite) ensureColumn(tableName, columnName, definition string) error {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var columnID int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, definition))
	return err
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Ensure interface compliance.
var _ store.Store = (*SQLite)(nil)
