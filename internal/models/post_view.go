package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PostView records that a user opened a post's detail page. Used to bias the
// Discover feed toward posts the viewer hasn't seen yet. The unique (user, post)
// index keeps it to one row per viewer per post.
type PostView struct {
	ID        string    `gorm:"type:text;primary_key" json:"id"`
	PostID    string    `gorm:"type:text;not null;index:idx_post_view_user,unique" json:"postId"`
	UserID    string    `gorm:"type:text;not null;index:idx_post_view_user,unique" json:"userId"`
	CreatedAt time.Time `json:"createdAt"`
}

// BeforeCreate generates UUID and sets timestamp before inserting.
func (pv *PostView) BeforeCreate(tx *gorm.DB) error {
	if pv.ID == "" {
		pv.ID = uuid.New().String()
	}
	if pv.CreatedAt.IsZero() {
		pv.CreatedAt = time.Now()
	}
	return nil
}

// TableName specifies the table name for PostView.
func (PostView) TableName() string { return "post_views" }
