package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// NewPostCommentsHandler returns the handler for /posts/{id}/comments.
//   GET    -> list all comments for the post (oldest first)
//   POST   -> create a new comment (author identity from X-User-* headers)
// The post id comes from the URL path parameter "id".
func NewPostCommentsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}

		postID := strings.TrimSpace(r.PathValue("id"))
		if postID == "" {
			http.Error(w, "post id required", http.StatusBadRequest)
			return
		}

		// Verify the post exists so we don't silently accept orphaned comments
		var post models.Post
		if err := db.Select("id, author_id, title").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "post not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to lookup post: "+err.Error(), http.StatusInternalServerError)
			return
		}

		switch r.Method {
		case http.MethodGet:
			var comments []models.PostComment
			if err := db.
				Where("post_id = ?", postID).
				Order("created_at ASC").
				Find(&comments).Error; err != nil {
				http.Error(w, "failed to fetch comments: "+err.Error(), http.StatusInternalServerError)
				return
			}

			result := make([]models.CommentResponse, 0, len(comments))
			for i := range comments {
				result = append(result, comments[i].ToResponse())
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)

		case http.MethodPost:
			var body models.CreateCommentRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			body.Content = strings.TrimSpace(body.Content)
			if body.Content == "" {
				http.Error(w, "content required", http.StatusBadRequest)
				return
			}

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

			comment := models.PostComment{
				PostID:       postID,
				AuthorID:     authorID,
				AuthorName:   authorName,
				AuthorAvatar: authorAvatar,
				Content:      body.Content,
			}
			if err := db.Create(&comment).Error; err != nil {
				http.Error(w, "failed to save comment: "+err.Error(), http.StatusInternalServerError)
				return
			}

			go CreateNotification(db, post.AuthorID, "comment", authorID, authorName, authorAvatar, postID, post.Title, body.Content)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(comment.ToResponse())

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// NewCommentDeleteHandler returns the handler for DELETE /posts/{id}/comments/{commentId}.
// Only the original author may delete their comment — authorization is by X-User-Id header.
func NewCommentDeleteHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		postID := strings.TrimSpace(r.PathValue("id"))
		commentID := strings.TrimSpace(r.PathValue("commentId"))
		if postID == "" || commentID == "" {
			http.Error(w, "post id and comment id required", http.StatusBadRequest)
			return
		}

		requesterID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if requesterID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		var comment models.PostComment
		if err := db.First(&comment, "id = ?", commentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "comment not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load comment: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if comment.PostID != postID {
			http.Error(w, "comment does not belong to this post", http.StatusBadRequest)
			return
		}

		if comment.AuthorID != requesterID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if err := db.Delete(&comment).Error; err != nil {
			http.Error(w, "failed to delete comment: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
