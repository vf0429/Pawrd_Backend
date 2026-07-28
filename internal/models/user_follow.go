package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserFollow represents a follow relationship between users.
type UserFollow struct {
	ID         string    `gorm:"type:text;primary_key" json:"id"`
	FollowerID string    `gorm:"type:text;not null;index:idx_user_follow_pair,unique" json:"followerId"`
	FolloweeID string    `gorm:"type:text;not null;index:idx_user_follow_pair,unique" json:"followeeId"`
	CreatedAt  time.Time `json:"createdAt"`
}

// BeforeCreate generates UUID and sets timestamp before inserting.
func (uf *UserFollow) BeforeCreate(tx *gorm.DB) error {
	if uf.ID == "" {
		uf.ID = uuid.New().String()
	}
	if uf.CreatedAt.IsZero() {
		uf.CreatedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for UserFollow.
func (UserFollow) TableName() string {
	return "user_follows"
}
