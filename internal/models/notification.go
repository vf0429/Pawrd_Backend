package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Notification struct {
	ID        string    `gorm:"type:text;primary_key" json:"id"`
	UserID    string    `gorm:"type:text;not null;index" json:"userId"`
	Type      string    `gorm:"type:text;not null" json:"type"` // "like", "collect", "comment"
	ActorID   string    `gorm:"type:text;not null" json:"actorId"`
	ActorName string    `gorm:"type:text;not null" json:"actorName"`
	ActorAvatar string  `gorm:"type:text" json:"actorAvatar"`
	PostID    string    `gorm:"type:text;not null;index" json:"postId"`
	PostTitle string    `gorm:"type:text" json:"postTitle"`
	Content   string    `gorm:"type:text" json:"content"`
	IsRead    bool      `gorm:"default:false;index" json:"isRead"`
	CreatedAt time.Time `json:"createdAt"`
}

func (n *Notification) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	return nil
}

type NotificationResponse struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	ActorID     string `json:"actorId"`
	ActorName   string `json:"actorName"`
	ActorAvatar string `json:"actorAvatar"`
	PostID      string `json:"postId"`
	PostTitle   string `json:"postTitle"`
	Content     string `json:"content"`
	IsRead      bool   `json:"isRead"`
	CreatedAt   string `json:"createdAt"`
}

func (n *Notification) ToResponse() NotificationResponse {
	return NotificationResponse{
		ID:          n.ID,
		Type:        n.Type,
		ActorID:     n.ActorID,
		ActorName:   n.ActorName,
		ActorAvatar: n.ActorAvatar,
		PostID:      n.PostID,
		PostTitle:   n.PostTitle,
		Content:     n.Content,
		IsRead:      n.IsRead,
		CreatedAt:   n.CreatedAt.Format(time.RFC3339),
	}
}
