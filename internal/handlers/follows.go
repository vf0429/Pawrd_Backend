package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

func loadFamilyByIDOrHandle(db *gorm.DB, idOrHandle string) (models.Family, error) {
	idOrHandle = strings.TrimSpace(idOrHandle)
	var family models.Family
	err := db.Where("id = ? OR handle = ?", idOrHandle, idOrHandle).First(&family).Error
	return family, err
}

// NewUserFollowHandler returns the handler for POST /users/{id}/follow.
// It toggles the follow state for the requesting user (identified by X-User-Id).
// Response: { "following": bool }.
func NewUserFollowHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		followeeID := strings.TrimSpace(r.PathValue("id"))
		if followeeID == "" {
			http.Error(w, "user id required", http.StatusBadRequest)
			return
		}

		followerID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if followerID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}

		if followerID == followeeID {
			http.Error(w, "cannot follow yourself", http.StatusBadRequest)
			return
		}

		var following bool
		err := db.Transaction(func(tx *gorm.DB) error {
			var existing models.UserFollow
			err := tx.Where("follower_id = ? AND followee_id = ?", followerID, followeeID).First(&existing).Error
			switch {
			case err == nil:
				if delErr := tx.Delete(&existing).Error; delErr != nil {
					return delErr
				}
				following = false
			case errors.Is(err, gorm.ErrRecordNotFound):
				newFollow := models.UserFollow{FollowerID: followerID, FolloweeID: followeeID}
				if createErr := tx.Create(&newFollow).Error; createErr != nil {
					return createErr
				}
				following = true
			default:
				return err
			}
			return nil
		})

		if err != nil {
			http.Error(w, "failed to toggle follow: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"following": following,
		})
	}
}

// NewUserFollowersHandler returns GET /users/{id}/followers.
func NewUserFollowersHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.PathValue("id"))
		if userID == "" {
			http.Error(w, "user id required", http.StatusBadRequest)
			return
		}

		var followerIDs []string
		if err := db.Model(&models.UserFollow{}).
			Where("followee_id = ?", userID).
			Pluck("follower_id", &followerIDs).Error; err != nil {
			http.Error(w, "failed to fetch followers: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": followerIDs,
		})
	}
}

// NewUserFollowingHandler returns GET /users/{id}/following.
func NewUserFollowingHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.PathValue("id"))
		if userID == "" {
			http.Error(w, "user id required", http.StatusBadRequest)
			return
		}

		var followeeIDs []string
		if err := db.Model(&models.UserFollow{}).
			Where("follower_id = ?", userID).
			Pluck("followee_id", &followeeIDs).Error; err != nil {
			http.Error(w, "failed to fetch following: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": followeeIDs,
		})
	}
}

// NewUserFollowingDetailHandler returns GET /users/{id}/following-detail.
// Like /following, but resolves each followee's display name and avatar so the
// result can directly back a UI list (e.g. the share-to-following picker).
// Response: { "users": [ { "id", "name", "avatar" } ] }.
func NewUserFollowingDetailHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.PathValue("id"))
		if userID == "" {
			http.Error(w, "user id required", http.StatusBadRequest)
			return
		}

		var followeeIDs []string
		if err := db.Model(&models.UserFollow{}).
			Where("follower_id = ?", userID).
			Pluck("followee_id", &followeeIDs).Error; err != nil {
			http.Error(w, "failed to fetch following: "+err.Error(), http.StatusInternalServerError)
			return
		}

		type followUser struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Avatar string `json:"avatar"`
		}
		users := make([]followUser, 0, len(followeeIDs))
		for _, fid := range followeeIDs {
			name, avatar := resolveDMUser(db, fid)
			users = append(users, followUser{ID: fid, Name: name, Avatar: avatar})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": users,
		})
	}
}

// NewUserFollowersDetailHandler returns GET /users/{id}/followers-detail.
// Like /followers, but resolves each follower's display name and avatar so the
// result can directly back a UI list (e.g. the profile followers list).
// Response: { "users": [ { "id", "name", "avatar" } ] }.
func NewUserFollowersDetailHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.PathValue("id"))
		if userID == "" {
			http.Error(w, "user id required", http.StatusBadRequest)
			return
		}

		var followerIDs []string
		if err := db.Model(&models.UserFollow{}).
			Where("followee_id = ?", userID).
			Pluck("follower_id", &followerIDs).Error; err != nil {
			http.Error(w, "failed to fetch followers: "+err.Error(), http.StatusInternalServerError)
			return
		}

		type followUser struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Avatar string `json:"avatar"`
		}
		users := make([]followUser, 0, len(followerIDs))
		for _, fid := range followerIDs {
			name, avatar := resolveDMUser(db, fid)
			users = append(users, followUser{ID: fid, Name: name, Avatar: avatar})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": users,
		})
	}
}

// NewUserStatsHandler returns GET /users/{id}/stats.
// Aggregates the headline counters shown on a profile:
//   - postCount:      posts authored by the user
//   - followerCount:  users following this user
//   - followingCount: users this user follows
//   - likeCount:      total likes received across all of the user's posts
//   - collectCount:   total saves received across all of the user's posts
//   - viewCount:      total views across all of the user's posts
//
// When called with an X-User-Id header, also reports whether the requester
// currently follows this user (`isFollowing`).
func NewUserStatsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.PathValue("id"))
		if userID == "" {
			http.Error(w, "user id required", http.StatusBadRequest)
			return
		}

		var postCount int64
		db.Model(&models.Post{}).Where("author_id = ?", userID).Count(&postCount)

		var followerCount int64
		db.Model(&models.UserFollow{}).Where("followee_id = ?", userID).Count(&followerCount)

		var followingCount int64
		db.Model(&models.UserFollow{}).Where("follower_id = ?", userID).Count(&followingCount)

		// Total likes received across the user's posts.
		var likeCount int64
		db.Model(&models.PostLike{}).
			Where("post_id IN (?)",
				db.Model(&models.Post{}).Select("id").Where("author_id = ?", userID),
			).Count(&likeCount)

		// Total saves received across the user's posts.
		var collectCount int64
		db.Model(&models.PostCollection{}).
			Where("post_id IN (?)",
				db.Model(&models.Post{}).Select("id").Where("author_id = ?", userID),
			).Count(&collectCount)

		// Total views across the user's posts.
		var viewSum struct{ Total int64 }
		db.Model(&models.Post{}).
			Select("COALESCE(SUM(views), 0) AS total").
			Where("author_id = ?", userID).
			Scan(&viewSum)

		isFollowing := false
		if requester := strings.TrimSpace(r.Header.Get("X-User-Id")); requester != "" && requester != userID {
			var rel int64
			db.Model(&models.UserFollow{}).
				Where("follower_id = ? AND followee_id = ?", requester, userID).
				Count(&rel)
			isFollowing = rel > 0
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"postCount":      int(postCount),
			"followerCount":  int(followerCount),
			"followingCount": int(followingCount),
			"likeCount":      int(likeCount),
			"collectCount":   int(collectCount),
			"viewCount":      int(viewSum.Total),
			"isFollowing":    isFollowing,
		})
	}
}

// NewFamilyFollowHandler toggles whether the requester follows a family.
// POST /api/domain/families/{idOrHandle}/follow
// Response: { "following": bool }.
func NewFamilyFollowHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		idOrHandle := strings.TrimSpace(r.PathValue("idOrHandle"))
		if idOrHandle == "" {
			http.Error(w, "family id or handle required", http.StatusBadRequest)
			return
		}

		family, err := loadFamilyByIDOrHandle(db, idOrHandle)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "family not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load family: "+err.Error(), http.StatusInternalServerError)
			return
		}

		followerUserID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if followerUserID == "" {
			http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
			return
		}
		if followerUserID == family.OwnerUserID {
			http.Error(w, "cannot follow your own family", http.StatusBadRequest)
			return
		}

		var following bool
		err = db.Transaction(func(tx *gorm.DB) error {
			var existing models.FamilyFollow
			err := tx.Where("family_id = ? AND follower_user_id = ?", family.ID, followerUserID).First(&existing).Error
			switch {
			case err == nil:
				if delErr := tx.Delete(&existing).Error; delErr != nil {
					return delErr
				}
				following = false
			case errors.Is(err, gorm.ErrRecordNotFound):
				newFollow := models.FamilyFollow{FamilyID: family.ID, FollowerUserID: followerUserID}
				if createErr := tx.Create(&newFollow).Error; createErr != nil {
					return createErr
				}
				following = true
			default:
				return err
			}
			return nil
		})
		if err != nil {
			http.Error(w, "failed to toggle family follow: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"following": following,
		})
	}
}

// NewFamilyFollowersDetailHandler returns GET /api/domain/families/{idOrHandle}/followers-detail.
// Response: { "users": [ { "id", "name", "avatar" } ] }.
func NewFamilyFollowersDetailHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		idOrHandle := strings.TrimSpace(r.PathValue("idOrHandle"))
		if idOrHandle == "" {
			http.Error(w, "family id or handle required", http.StatusBadRequest)
			return
		}

		family, err := loadFamilyByIDOrHandle(db, idOrHandle)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "family not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load family: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var followerIDs []string
		if err := db.Model(&models.FamilyFollow{}).
			Where("family_id = ?", family.ID).
			Order("created_at DESC").
			Pluck("follower_user_id", &followerIDs).Error; err != nil {
			http.Error(w, "failed to fetch family followers: "+err.Error(), http.StatusInternalServerError)
			return
		}

		type followUser struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Avatar string `json:"avatar"`
		}
		users := make([]followUser, 0, len(followerIDs))
		for _, fid := range followerIDs {
			name, avatar := resolveDMUser(db, fid)
			users = append(users, followUser{ID: fid, Name: name, Avatar: avatar})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": users,
		})
	}
}
