package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// NewPostPollVoteHandler returns the handler for POST /posts/{id}/poll/vote.
// The requesting user (X-User-Id) casts or changes their single vote on the
// post's poll. Body: { "optionId": "..." }. Response: the updated BlogPoll.
func NewPostPollVoteHandler(db *gorm.DB) http.HandlerFunc {
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
			OptionID string `json:"optionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		optionID := strings.TrimSpace(body.OptionID)
		if optionID == "" {
			http.Error(w, "optionId required", http.StatusBadRequest)
			return
		}

		// Resolve the poll for this post.
		var poll models.PostPoll
		if err := db.First(&poll, "post_id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "poll not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load poll: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// The option must belong to this poll.
		var option models.PostPollOption
		if err := db.First(&option, "id = ? AND poll_id = ?", optionID, poll.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "invalid option", http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to load option: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// One vote per user, changeable: update the existing row or insert a new one.
		err := db.Transaction(func(tx *gorm.DB) error {
			var existing models.PostPollVote
			e := tx.Where("poll_id = ? AND user_id = ?", poll.ID, userID).First(&existing).Error
			switch {
			case e == nil:
				existing.OptionID = optionID
				existing.UpdatedAt = time.Now()
				return tx.Save(&existing).Error
			case errors.Is(e, gorm.ErrRecordNotFound):
				return tx.Create(&models.PostPollVote{
					PollID:   poll.ID,
					OptionID: optionID,
					UserID:   userID,
				}).Error
			default:
				return e
			}
		})
		if err != nil {
			http.Error(w, "failed to record vote: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(loadBlogPoll(db, postID, userID))
	}
}
