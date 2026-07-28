package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// resolveDMUser looks up a user's display name and avatar.
//
// The primary source is the auth database (AuthUser). Seed/test/demo accounts
// often exist only as post authors and have no AuthUser row, or have an empty
// AvatarURL there. To make those accounts render properly in chat and the share
// picker, any missing name/avatar falls back to the user's most recent post
// (Post.AuthorName / Post.AuthorAvatar) in the main database.
func resolveDMUser(db *gorm.DB, id string) (name, avatar string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", ""
	}

	if models.AuthDB != nil {
		var user models.AuthUser
		if err := models.AuthDB.First(&user, "id = ?", id).Error; err == nil {
			name = strings.TrimSpace(user.Name)
			avatar = strings.TrimSpace(user.AvatarURL)
		}
	}

	// Fill any gaps from the user's latest post.
	if (name == "" || avatar == "") && db != nil {
		var post models.Post
		if err := db.Select("author_name, author_avatar").
			Where("author_id = ?", id).
			Order("created_at DESC").
			First(&post).Error; err == nil {
			if name == "" {
				name = strings.TrimSpace(post.AuthorName)
			}
			if avatar == "" {
				avatar = strings.TrimSpace(post.AuthorAvatar)
			}
		}
	}

	return name, avatar
}

// dmOtherParticipant returns the conversation participant that is not `me`.
func dmOtherParticipant(conversationID, me string) string {
	parts := strings.SplitN(conversationID, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	if parts[0] == me {
		return parts[1]
	}
	return parts[0]
}

// NewMessageSendHandler handles POST /messages/send.
//
// Body:    { "toUserId": "...", "content": "..." }
// Headers: X-User-Id (sender)
//
// Anyone may message anyone (no friend request needed), but until the recipient
// has replied at least once, the sender is limited to a single message in that
// conversation. Once the recipient replies, the conversation is unrestricted.
func NewMessageSendHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		senderID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if senderID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		var body struct {
			ToUserID string `json:"toUserId"`
			Content  string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		recipientID := strings.TrimSpace(body.ToUserID)
		content := strings.TrimSpace(body.Content)
		if recipientID == "" || content == "" {
			http.Error(w, "toUserId and content required", http.StatusBadRequest)
			return
		}
		if recipientID == senderID {
			http.Error(w, "cannot message yourself", http.StatusBadRequest)
			return
		}

		conversationID := models.ConversationID(senderID, recipientID)

		// Enforce the "one message until reply" gate for cold conversations.
		var recipientReplies int64
		db.Model(&models.ChatMessage{}).
			Where("conversation_id = ? AND sender_id = ?", conversationID, recipientID).
			Count(&recipientReplies)

		if recipientReplies == 0 {
			var mine int64
			db.Model(&models.ChatMessage{}).
				Where("conversation_id = ? AND sender_id = ?", conversationID, senderID).
				Count(&mine)
			if mine >= 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "You can only send one message until they reply.",
					"code":  "awaiting_reply",
				})
				return
			}
		}

		message := models.ChatMessage{
			ConversationID: conversationID,
			SenderID:       senderID,
			RecipientID:    recipientID,
			Content:        content,
		}
		if err := db.Create(&message).Error; err != nil {
			http.Error(w, "failed to send message: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(message)
	}
}

// dmConversationDTO is one row in the conversations list.
type dmConversationDTO struct {
	ConversationID  string    `json:"conversationId"`
	OtherUserID     string    `json:"otherUserId"`
	OtherUserName   string    `json:"otherUserName"`
	OtherUserAvatar string    `json:"otherUserAvatar"`
	LastMessage     string    `json:"lastMessage"`
	LastMessageAt   time.Time `json:"lastMessageAt"`
	LastFromMe      bool      `json:"lastFromMe"`
	UnreadCount     int       `json:"unreadCount"`
}

// NewConversationsHandler handles GET /messages/conversations.
// Returns the requesting user's conversations, newest activity first.
func NewConversationsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		me := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if me == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		var convIDs []string
		db.Model(&models.ChatMessage{}).
			Where("sender_id = ? OR recipient_id = ?", me, me).
			Distinct("conversation_id").
			Pluck("conversation_id", &convIDs)

		conversations := make([]dmConversationDTO, 0, len(convIDs))
		for _, cid := range convIDs {
			var last models.ChatMessage
			if err := db.Where("conversation_id = ?", cid).
				Order("created_at DESC").
				First(&last).Error; err != nil {
				continue
			}

			var unread int64
			db.Model(&models.ChatMessage{}).
				Where("conversation_id = ? AND recipient_id = ? AND is_read = ?", cid, me, false).
				Count(&unread)

			otherID := dmOtherParticipant(cid, me)
			name, avatar := resolveDMUser(db, otherID)

			conversations = append(conversations, dmConversationDTO{
				ConversationID:  cid,
				OtherUserID:     otherID,
				OtherUserName:   name,
				OtherUserAvatar: avatar,
				LastMessage:     last.Content,
				LastMessageAt:   last.CreatedAt,
				LastFromMe:      last.SenderID == me,
				UnreadCount:     int(unread),
			})
		}

		sort.Slice(conversations, func(i, j int) bool {
			return conversations[i].LastMessageAt.After(conversations[j].LastMessageAt)
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"conversations": conversations})
	}
}

// NewMessageThreadHandler handles GET /messages/thread?withUserId=X.
// Returns the full thread (oldest first) between the requester and X, and
// marks the requester's incoming messages in that thread as read.
func NewMessageThreadHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		me := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if me == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		other := strings.TrimSpace(r.URL.Query().Get("withUserId"))
		if other == "" {
			http.Error(w, "withUserId required", http.StatusBadRequest)
			return
		}

		conversationID := models.ConversationID(me, other)

		var messages []models.ChatMessage
		if err := db.Where("conversation_id = ?", conversationID).
			Order("created_at ASC").
			Find(&messages).Error; err != nil {
			http.Error(w, "failed to load messages: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Mark the other party's messages as read now that they've been fetched.
		db.Model(&models.ChatMessage{}).
			Where("conversation_id = ? AND recipient_id = ? AND is_read = ?", conversationID, me, false).
			Update("is_read", true)

		otherName, otherAvatar := resolveDMUser(db, other)

		// Whether the requester is currently gated to a single message.
		var otherReplies int64
		db.Model(&models.ChatMessage{}).
			Where("conversation_id = ? AND sender_id = ?", conversationID, other).
			Count(&otherReplies)
		var myMessages int64
		db.Model(&models.ChatMessage{}).
			Where("conversation_id = ? AND sender_id = ?", conversationID, me).
			Count(&myMessages)
		awaitingReply := otherReplies == 0 && myMessages >= 1

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages":        messages,
			"otherUserId":     other,
			"otherUserName":   otherName,
			"otherUserAvatar": otherAvatar,
			"awaitingReply":   awaitingReply,
		})
	}
}

// NewMessagesUnreadCountHandler handles GET /messages/unread-count.
func NewMessagesUnreadCountHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		me := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if me == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		var count int64
		db.Model(&models.ChatMessage{}).
			Where("recipient_id = ? AND is_read = ?", me, false).
			Count(&count)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": int(count)})
	}
}
