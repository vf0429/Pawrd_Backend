package models

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PostImage represents an image attached to a blog post
type PostImage struct {
	ID       string `gorm:"type:text;primary_key" json:"id"`
	PostID   string `gorm:"type:text;not null;index:idx_post_sort" json:"postId"`
	ImageURL string `gorm:"type:text;not null" json:"imageUrl"`
	SortOrder int   `gorm:"not null;index:idx_post_sort" json:"sortOrder"`
}

// BeforeCreate generates UUID before inserting a new record
func (pi *PostImage) BeforeCreate(tx *gorm.DB) error {
	if pi.ID == "" {
		pi.ID = uuid.New().String()
	}
	return nil
}

// TableName specifies the table name for PostImage
func (PostImage) TableName() string {
	return "post_images"
}
