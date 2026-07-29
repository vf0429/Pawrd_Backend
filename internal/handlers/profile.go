package handlers

import (
	"net/http"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// profileMePayload is the unified account + family response for GET /api/profile/me.
type profileMePayload struct {
	Account accountPayload       `json:"account"`
	Family  familyProfilePayload `json:"family"`
}

// NewProfileMeHandler handles GET /api/profile/me
// Requires Bearer JWT. Ensures the user's family exists (idempotently creating
// it if missing) and returns the account plus the family profile payload.
func NewProfileMeHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		var user models.AuthUser
		if err := models.AuthDB.First(&user, "id = ?", userID).Error; err != nil {
			writeAuthError(w, http.StatusNotFound, "Account not found")
			return
		}

		// Idempotently ensure a family exists for this user.
		if err := createFamilyForUser(db, &user); err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to ensure family profile")
			return
		}

		var family models.Family
		if err := db.Where("owner_user_id = ?", userID).First(&family).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to load family profile")
			return
		}

		familyPayload, status, err := loadFamilyProfile(db, family.ID, userID)
		if err != nil {
			http.Error(w, "failed to load family: "+err.Error(), status)
			return
		}

		writeJSON(w, http.StatusOK, profileMePayload{
			Account: accountPayloadOf(&user),
			Family:  familyPayload,
		})
	}
}
