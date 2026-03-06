package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/vf0429/Petwell_Backend/internal/models"
	"gorm.io/gorm"
)

// NewPostsHandler returns an http.HandlerFunc for GET/POST /posts backed by SQLite.
// GET  /posts  — returns all posts as []BlogPost (iOS-compatible format)
// POST /posts  — creates a post, persists to SQLite, returns the created BlogPost
// Author identity is read from X-User-Id / X-User-Name / X-User-Avatar request headers.
func NewPostsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}

		switch r.Method {
		case http.MethodGet:
			var posts []models.Post
			if err := db.Preload("Images").Preload("Likes").Order("created_at DESC").Find(&posts).Error; err != nil {
				http.Error(w, "Failed to fetch posts: "+err.Error(), http.StatusInternalServerError)
				return
			}

			result := make([]models.BlogPost, 0, len(posts))
			for _, p := range posts {
				imageUrls := make([]string, 0, len(p.Images))
				for _, img := range p.Images {
					imageUrls = append(imageUrls, img.ImageURL)
				}
				result = append(result, models.BlogPost{
					ID:           p.ID,
					AuthorName:   p.AuthorName,
					AuthorAvatar: p.AuthorAvatar,
					Title:        p.Title,
					Content:      p.Content,
					ImageColor:   p.ImageColor,
					Likes:        len(p.Likes),
					Timestamp:    p.CreatedAt,
					ImageUrls:    imageUrls,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)

		case http.MethodPost:
			// Decode body fields: title, content, imageColor, imageUrls
			var body struct {
				Title      string   `json:"title"`
				Content    string   `json:"content"`
				ImageColor string   `json:"imageColor"`
				ImageUrls  []string `json:"imageUrls"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "Invalid request body", http.StatusBadRequest)
				return
			}
			if body.Content == "" && body.Title == "" {
				http.Error(w, "title or content required", http.StatusBadRequest)
				return
			}

			// Author identity comes from headers, NOT body
			authorID := r.Header.Get("X-User-Id")
			authorName := r.Header.Get("X-User-Name")
			authorAvatar := r.Header.Get("X-User-Avatar")
			if authorName == "" {
				authorName = "Anonymous"
			}
			if authorAvatar == "" {
				authorAvatar = "person.circle.fill"
			}
			if authorID == "" {
				authorID = authorName
			}
			imageColor := body.ImageColor
			if imageColor == "" {
				imageColor = "blue"
			}

			post := models.Post{
				AuthorID:     authorID,
				AuthorName:   authorName,
				AuthorAvatar: authorAvatar,
				Title:        body.Title,
				Content:      body.Content,
				ImageColor:   imageColor,
			}
			if err := db.Create(&post).Error; err != nil {
				http.Error(w, "Failed to save post: "+err.Error(), http.StatusInternalServerError)
				return
			}

			// Save image URLs as PostImage records
			for i, url := range body.ImageUrls {
				img := models.PostImage{
					PostID:    post.ID,
					ImageURL:  url,
					SortOrder: i,
				}
				db.Create(&img)
			}

			response := models.BlogPost{
				ID:           post.ID,
				AuthorName:   post.AuthorName,
				AuthorAvatar: post.AuthorAvatar,
				Title:        post.Title,
				Content:      post.Content,
				ImageColor:   post.ImageColor,
				Likes:        0,
				Timestamp:    post.CreatedAt,
				ImageUrls:    body.ImageUrls,
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(response)

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
