package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PostCollection represents a bookmark/collection on a blog post.
type PostCollection struct {
	ID        string    `gorm:"type:text;primary_key" json:"id"`
	PostID    string    `gorm:"type:text;not null;index:idx_post_collection_user,unique" json:"postId"`
	UserID    string    `gorm:"type:text;not null;index:idx_post_collection_user,unique" json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
}

// BeforeCreate generates UUID and sets timestamp before inserting.
func (pc *PostCollection) BeforeCreate(tx *gorm.DB) error {
	if pc.ID == "" {
		pc.ID = uuid.New().String()
	}
	if pc.CreatedAt.IsZero() {
		pc.CreatedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for PostCollection.
func (PostCollection) TableName() string {
	return "post_collections"
}
