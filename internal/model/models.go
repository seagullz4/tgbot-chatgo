package model

import "time"

// User maps a private Telegram user to a forum topic in the admin group.
type User struct {
	ID              int64
	UserID          int64
	FirstName       string
	LastName        string
	Username        string
	IsPremium       bool
	MessageThreadID int
	IsBanned        bool
	BanReason       string
	UpdatedAt       time.Time
}

// MessageMap links a user private-chat message id with the mirrored group message id.
type MessageMap struct {
	ID                 int64
	UserChatMessageID  int
	GroupChatMessageID int
	UserID             int64
	MessageText        string
}

// ForumStatus tracks whether a topic conversation is opened or closed.
type ForumStatus struct {
	ID              int64
	ChatID          int64
	MessageThreadID int
	Status          string // opened | closed
}

// MediaGroupMessage buffers parts of a media group before delayed flush.
type MediaGroupMessage struct {
	ID           int64
	ChatID       int64
	MessageID    int
	MediaGroupID string
	IsHeader     bool
	CaptionHTML  string
}

// VerificationState persists arithmetic verification and rate-limit state.
type VerificationState struct {
	UserID            int64
	Answer            string
	IsHuman           bool
	ErrorUntil        time.Time
	LastMsgAt         time.Time
	PendingMessageIDs string
}
