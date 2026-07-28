package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// NewPostLikeHandler returns the handler for POST /posts/{id}/like.
// It toggles the like state for the requesting user (identified by X-User-Id).
// Response: { "liked": bool, "likeCount": int }.
func NewPostLikeHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		postID := strings.TrimSpace(r.PathValue("id"))
		if postID == "" {
			http.Error(w, "post id required", http.StatusBadRequest)
			return
		}

		userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if userID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		// Verify the post exists.
		var post models.Post
		if err := db.Select("id, author_id, title").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "post not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to lookup post: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var liked bool
		var likeCount int64

		err := db.Transaction(func(tx *gorm.DB) error {
			var existing models.PostLike
			err := tx.Where("post_id = ? AND user_id = ?", postID, userID).First(&existing).Error
			switch {
			case err == nil:
				// Already liked → unlike.
				if delErr := tx.Delete(&existing).Error; delErr != nil {
					return delErr
				}
				liked = false
			case errors.Is(err, gorm.ErrRecordNotFound):
				// Not liked yet → like.
				newLike := models.PostLike{PostID: postID, UserID: userID}
				if createErr := tx.Create(&newLike).Error; createErr != nil {
					return createErr
				}
				liked = true
			default:
				return err
			}

			return tx.Model(&models.PostLike{}).
				Where("post_id = ?", postID).
				Count(&likeCount).Error
		})

		if err != nil {
			http.Error(w, "failed to toggle like: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if liked {
			actorName := r.Header.Get("X-User-Name")
			actorAvatar := r.Header.Get("X-User-Avatar")
			if actorName == "" {
				actorName = "Someone"
			}
			go CreateNotification(db, post.AuthorID, "like", userID, actorName, actorAvatar, postID, post.Title, "")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"liked":     liked,
			"likeCount": int(likeCount),
		})
	}
}
