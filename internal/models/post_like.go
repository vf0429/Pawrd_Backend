package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PostLike represents a like on a blog post
type PostLike struct {
	ID        string    `gorm:"type:text;primary_key" json:"id"`
	PostID    string    `gorm:"type:text;not null;index:idx_post_user,unique" json:"postId"`
	UserID    string    `gorm:"type:text;not null;index:idx_post_user,unique" json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
}

// BeforeCreate generates UUID and sets timestamp before inserting
func (pl *PostLike) BeforeCreate(tx *gorm.DB) error {
	if pl.ID == "" {
		pl.ID = uuid.New().String()
	}
	if pl.CreatedAt.IsZero() {
		pl.CreatedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for PostLike
func (PostLike) TableName() string {
	return "post_likes"
}

// ToggleLikeRequest is the request body for toggling a like
type ToggleLikeRequest struct {
	PostID string `json:"postId" binding:"required"`
}

// LikeResponse is the response for like operations
type LikeResponse struct {
	PostID    string `json:"postId"`
	LikeCount int    `json:"likeCount"`
	IsLiked   bool   `json:"isLiked"`
}
