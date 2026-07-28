package models

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ChatMessage is a single direct message between two users.
//
// Conversations are not stored as their own table: a conversation is uniquely
// identified by the sorted pair of participant ids (see ConversationID), so any
// two users always map to one stable conversation key regardless of who sends
// first.
type ChatMessage struct {
	ID             string    `gorm:"type:text;primary_key" json:"id"`
	ConversationID string    `gorm:"type:text;not null;index" json:"conversationId"`
	SenderID       string    `gorm:"type:text;not null;index" json:"senderId"`
	RecipientID    string    `gorm:"type:text;not null;index" json:"recipientId"`
	Content        string    `gorm:"type:text;not null" json:"content"`
	// PostID is set when the message is a shared blog post; empty for plain text.
	// The client renders such messages as a tappable post card.
	PostID    string    `gorm:"type:text" json:"postId,omitempty"`
	IsRead    bool      `gorm:"default:false" json:"isRead"`
	CreatedAt time.Time `json:"createdAt"`
}

// BeforeCreate assigns a UUID and timestamp before insertion.
func (m *ChatMessage) BeforeCreate(tx *gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	return nil
}

// TableName pins the table name.
func (ChatMessage) TableName() string {
	return "chat_messages"
}

// ConversationID returns the stable conversation key for two participants.
// The two ids are sorted so the result is independent of argument order.
func ConversationID(a, b string) string {
	pair := []string{strings.TrimSpace(a), strings.TrimSpace(b)}
	sort.Strings(pair)
	return pair[0] + ":" + pair[1]
}
