package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PostComment represents a comment on a blog post.
// Author identity is captured at write time (denormalized) so that
// comments survive even if the user record changes later.
type PostComment struct {
	ID           string    `gorm:"type:text;primary_key" json:"id"`
	PostID       string    `gorm:"type:text;not null;index" json:"postId"`
	AuthorID     string    `gorm:"type:text;not null;index" json:"authorId"`
	AuthorName   string    `gorm:"type:text;default:''" json:"authorName"`
	AuthorAvatar string    `gorm:"type:text;default:'person.circle.fill'" json:"authorAvatar"`
	Content      string    `gorm:"type:text;not null" json:"content"`
	CreatedAt    time.Time `json:"createdAt"`
}

// BeforeCreate generates UUID before inserting a new record
func (c *PostComment) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	return nil
}

// TableName specifies the table name for PostComment
func (PostComment) TableName() string {
	return "post_comments"
}

// CommentResponse is the API response format for a comment
type CommentResponse struct {
	ID           string    `json:"id"`
	PostID       string    `json:"postId"`
	AuthorID     string    `json:"authorId"`
	AuthorName   string    `json:"authorName"`
	AuthorAvatar string    `json:"authorAvatar"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ToResponse converts a PostComment to its API response form
func (c *PostComment) ToResponse() CommentResponse {
	return CommentResponse{
		ID:           c.ID,
		PostID:       c.PostID,
		AuthorID:     c.AuthorID,
		AuthorName:   c.AuthorName,
		AuthorAvatar: c.AuthorAvatar,
		Content:      c.Content,
		CreatedAt:    c.CreatedAt,
	}
}

// CreateCommentRequest is the request body for creating a comment
type CreateCommentRequest struct {
	Content string `json:"content" binding:"required"`
}
