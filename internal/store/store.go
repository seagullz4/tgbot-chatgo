package store

import "telegram-interactive-bot/go-bot/internal/model"

// Store is the persistence boundary used by services.
type Store interface {
	Close() error
	Migrate() error

	// users
	EnsureUser(u *model.User) (*model.User, error)
	GetUserByTelegramID(userID int64) (*model.User, error)
	GetUserByThreadID(threadID int) (*model.User, error)
	UpdateUserThreadID(userID int64, threadID int) error
	SetUserBanned(userID int64, banned bool, reason string) error
	ListUsers() ([]*model.User, error)
	ListBannedUsers() ([]*model.User, error)

	// message map
	SaveMessageMap(m *model.MessageMap) error
	GetByUserMessageID(userMessageID int) (*model.MessageMap, error)
	GetByGroupMessageID(groupMessageID int) (*model.MessageMap, error)
	UpdateMessageTextByUserMessageID(userMessageID int, text string) error
	UpdateMessageTextByGroupMessageID(groupMessageID int, text string) error
	ListMessageMapsByUser(userID int64) ([]*model.MessageMap, error)
	DeleteMessageMapsByUser(userID int64) error
	CountMessageMapsByUser(userID int64) (int, error)

	// forum status
	UpsertForumStatus(s *model.ForumStatus) error
	GetForumStatus(threadID int) (*model.ForumStatus, error)
	DeleteForumStatus(threadID int) error

	// media group buffer
	SaveMediaGroupMessage(m *model.MediaGroupMessage) error
	ListMediaGroupMessages(chatID int64, mediaGroupID string) ([]*model.MediaGroupMessage, error)
	DeleteMediaGroupMessages(chatID int64, mediaGroupID string) error

	// verification / rate-limit state
	GetVerificationState(userID int64) (*model.VerificationState, error)
	UpsertVerificationState(s *model.VerificationState) error
}
