package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Post represents a blog post in the system
type Post struct {
	ID           string    `gorm:"type:text;primary_key" json:"id"`
	AuthorID     string    `gorm:"type:text;not null;index" json:"authorId"`
	AuthorName   string    `gorm:"type:text;default:''" json:"authorName"`
	AuthorAvatar string    `gorm:"type:text;default:'person.circle.fill'" json:"authorAvatar"`
	Title        string    `gorm:"type:text;default:''" json:"title"`
	Content      string    `gorm:"type:text;not null" json:"content"`
	ImageColor   string    `gorm:"type:text;default:'blue'" json:"imageColor"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`

	// Relationships
	Images []PostImage `gorm:"foreignKey:PostID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"images,omitempty"`
	Likes  []PostLike  `gorm:"foreignKey:PostID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"likes,omitempty"`
}

// BeforeCreate generates UUID before inserting a new record
func (p *Post) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return nil
}

// PostResponse is the API response format for posts
type PostResponse struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"authorId"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Images    []string  `json:"images"`
	LikeCount int       `json:"likeCount"`
	IsLiked   bool      `json:"isLiked"`
}

// ToResponse converts a Post to PostResponse
func (p *Post) ToResponse(currentUserID string) PostResponse {
	imageURLs := make([]string, 0, len(p.Images))
	for _, img := range p.Images {
		imageURLs = append(imageURLs, img.ImageURL)
	}

	likeCount := len(p.Likes)
	isLiked := false
	for _, like := range p.Likes {
		if like.UserID == currentUserID {
			isLiked = true
			break
		}
	}

	return PostResponse{
		ID:        p.ID,
		AuthorID:  p.AuthorID,
		Content:   p.Content,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
		Images:    imageURLs,
		LikeCount: likeCount,
		IsLiked:   isLiked,
	}
}

// CreatePostRequest is the request body for creating a post
type CreatePostRequest struct {
	Content string   `json:"content" binding:"required"`
	Images  []string `json:"images"`
}

// UpdatePostRequest is the request body for updating a post
type UpdatePostRequest struct {
	Content string   `json:"content"`
	Images  []string `json:"images"`
}
