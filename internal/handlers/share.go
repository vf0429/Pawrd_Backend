package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// NewPostShareHandler returns the handler for POST /posts/{id}/share.
//
// It sends the shared post as a direct message to each target user, so the
// share shows up in both participants' chat conversation list (rendered as a
// post card via the message's PostID). The actor is identified by the
// X-User-Id / X-User-Name / X-User-Avatar headers (same convention as the
// like/collect endpoints). Shares bypass the "one message until reply" gating.
//
// Request body: { "targetUserIds": ["..."] }
// Response:     { "shared": <int> }  // number of recipients actually messaged
func NewPostShareHandler(db *gorm.DB) http.HandlerFunc {
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

		var body struct {
			TargetUserIDs []string `json:"targetUserIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(body.TargetUserIDs) == 0 {
			http.Error(w, "targetUserIds required", http.StatusBadRequest)
			return
		}

		// Verify the post exists (and grab its title for the notification text).
		var post models.Post
		if err := db.Select("id, author_id, title").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "post not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to lookup post: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// De-duplicate targets and skip self-shares.
		seen := make(map[string]bool)
		shared := 0
		for _, target := range body.TargetUserIDs {
			target = strings.TrimSpace(target)
			if target == "" || target == userID || seen[target] {
				continue
			}
			seen[target] = true

			msg := models.ChatMessage{
				ConversationID: models.ConversationID(userID, target),
				SenderID:       userID,
				RecipientID:    target,
				Content:        post.Title,
				PostID:         postID,
			}
			if err := db.Create(&msg).Error; err != nil {
				http.Error(w, "failed to send share: "+err.Error(), http.StatusInternalServerError)
				return
			}
			shared++
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"shared": shared})
	}
}
