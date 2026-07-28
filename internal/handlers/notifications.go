package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// CreateNotification inserts a notification record. It skips if actor == recipient.
func CreateNotification(db *gorm.DB, userID, notifType, actorID, actorName, actorAvatar, postID, postTitle, content string) {
	if userID == actorID {
		return
	}
	n := models.Notification{
		UserID:      userID,
		Type:        notifType,
		ActorID:     actorID,
		ActorName:   actorName,
		ActorAvatar: actorAvatar,
		PostID:      postID,
		PostTitle:   postTitle,
		Content:     content,
	}
	db.Create(&n)
}

// NewNotificationsHandler handles GET /notifications (list) and POST /notifications/read-all.
func NewNotificationsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if userID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		limit := 30
		if l := r.URL.Query().Get("limit"); l != "" {
			if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
				limit = parsed
			}
		}

		cursor := r.URL.Query().Get("cursor")

		query := db.Where("user_id = ?", userID).Order("created_at DESC").Limit(limit + 1)
		if cursor != "" {
			query = query.Where("created_at < ?", cursor)
		}

		var notifications []models.Notification
		if err := query.Find(&notifications).Error; err != nil {
			http.Error(w, "failed to fetch notifications: "+err.Error(), http.StatusInternalServerError)
			return
		}

		hasMore := len(notifications) > limit
		if hasMore {
			notifications = notifications[:limit]
		}

		results := make([]models.NotificationResponse, 0, len(notifications))
		for i := range notifications {
			results = append(results, notifications[i].ToResponse())
		}

		var nextCursor string
		if hasMore && len(notifications) > 0 {
			nextCursor = notifications[len(notifications)-1].CreatedAt.Format("2006-01-02T15:04:05Z07:00")
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"notifications": results,
			"nextCursor":    nextCursor,
			"hasMore":       hasMore,
		})
	}
}

// NewNotificationsUnreadCountHandler handles GET /notifications/unread-count.
func NewNotificationsUnreadCountHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if userID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		var count int64
		db.Model(&models.Notification{}).Where("user_id = ? AND is_read = false", userID).Count(&count)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": int(count)})
	}
}

// NewNotificationsReadAllHandler handles POST /notifications/read-all.
func NewNotificationsReadAllHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if userID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		db.Model(&models.Notification{}).
			Where("user_id = ? AND is_read = false", userID).
			Update("is_read", true)

		w.WriteHeader(http.StatusNoContent)
	}
}
