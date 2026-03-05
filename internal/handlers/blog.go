package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/vf0429/Petwell_Backend/internal/models"
	"gorm.io/gorm"
)

// Author represents the author of a post
type Author struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}

// PostImageResponse represents an image in the post response
type PostImageResponse struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// PostResponse represents a post in the feed
type FeedPostResponse struct {
	ID        string              `json:"id"`
	Author    Author              `json:"author"`
	Content   string              `json:"content"`
	Images    []PostImageResponse `json:"images"`
	Likes     int                 `json:"likes"`
	Comments  int                 `json:"comments"`
	CreatedAt time.Time           `json:"createdAt"`
}

// FeedResponse represents the feed response
type FeedResponse struct {
	Posts      []FeedPostResponse `json:"posts"`
	HasMore    bool               `json:"hasMore"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

// NewBlogHandler creates a handler for blog endpoints
func NewBlogHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse query parameters
		limit := 20
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 50 {
				limit = parsed
			}
		}

		feedType := r.URL.Query().Get("type")
		if feedType == "" {
			feedType = "explore" // default feed type
		}

		// Get current user ID from query (in production, this would come from auth token)
		currentUserID := r.URL.Query().Get("userId")

		// Fetch posts from database
		var posts []models.Post
		query := db.Preload("Images").Preload("Likes").Order("created_at DESC")

		// Apply cursor pagination if provided
		if cursor := r.URL.Query().Get("cursor"); cursor != "" {
			query = query.Where("created_at < ?", cursor)
		}

		// Limit results
		query = query.Limit(limit + 1) // Fetch one extra to determine hasMore

		if err := query.Find(&posts).Error; err != nil {
			http.Error(w, "Failed to fetch posts: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Determine if there are more posts
		hasMore := len(posts) > limit
		if hasMore {
			posts = posts[:limit] // Remove the extra post
		}

		// Generate next cursor
		nextCursor := ""
		if hasMore && len(posts) > 0 {
			nextCursor = posts[len(posts)-1].CreatedAt.Format(time.RFC3339)
		}

		// Transform posts to response format
		feedPosts := make([]FeedPostResponse, 0, len(posts))
		for _, post := range posts {
			feedPosts = append(feedPosts, transformToFeedPost(post, currentUserID))
		}

		// Return response
		w.Header().Set("Content-Type", "application/json")
		response := FeedResponse{
			Posts:      feedPosts,
			HasMore:    hasMore,
			NextCursor: nextCursor,
		}
		json.NewEncoder(w).Encode(response)
	}
}

// transformToFeedPost converts a Post model to FeedPostResponse
func transformToFeedPost(post models.Post, currentUserID string) FeedPostResponse {
	// Transform images
	images := make([]PostImageResponse, 0, len(post.Images))
	for _, img := range post.Images {
		images = append(images, PostImageResponse{
			URL:    img.ImageURL,
			Width:  800,  // Default width
			Height: 800,  // Default height
		})
	}

	// Count likes
	likeCount := len(post.Likes)

	// For now, comments count is 0 (would need Comment model)
	commentCount := 0

	return FeedPostResponse{
		ID:      post.ID,
		Author:  Author{
			ID:     post.AuthorID,
			Name:   "User " + post.AuthorID[:8], // Placeholder name
			Avatar: "https://api.dicebear.com/7.x/avataaars/svg?seed=" + post.AuthorID,
		},
		Content:   post.Content,
		Images:    images,
		Likes:     likeCount,
		Comments:  commentCount,
		CreatedAt: post.CreatedAt,
	}
}
