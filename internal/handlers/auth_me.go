package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

type AuthContactRecordResponse struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Verified  bool   `json:"verified"`
	IsPrimary bool   `json:"is_primary"`
}

type AuthNeedsResponse struct {
	MissingEmail bool `json:"missing_email"`
	MissingPhone bool `json:"missing_phone"`
}

type AuthAccountProfileResponse struct {
	User     models.AuthUserResponse     `json:"user"`
	Contacts []AuthContactRecordResponse `json:"contacts"`
	Needs    AuthNeedsResponse           `json:"needs"`
}

// NewAuthMeHandler returns the current account truth for an authenticated
// session. The signed user_id is authoritative so older JWTs can recover the
// current email from AuthDB even if their embedded email claim is stale.
func NewAuthMeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Allow", "GET, OPTIONS")

		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			writeAuthError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}

		claims, ok := authenticatedAuthClaims(w, r)
		if !ok {
			return
		}
		if models.AuthDB == nil {
			writeAuthError(w, http.StatusServiceUnavailable, "Auth account storage is unavailable")
			return
		}

		var user models.AuthUser
		err := models.AuthDB.First(&user, "id = ?", strings.TrimSpace(claims.UserID)).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeAuthError(w, http.StatusNotFound, "Auth account not found")
			return
		}
		if err != nil {
			writeAuthError(w, http.StatusServiceUnavailable, "Auth account storage is unavailable")
			return
		}

		writeJSON(w, http.StatusOK, authAccountProfileResponse(user))
	}
}

func authenticatedAuthClaims(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		writeAuthError(w, http.StatusUnauthorized, "Missing authorization header")
		return nil, false
	}
	tokenString := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if tokenString == "" {
		writeAuthError(w, http.StatusUnauthorized, "Invalid or expired token")
		return nil, false
	}
	claims, err := auth.ValidateToken(tokenString)
	if err != nil || claims == nil || strings.TrimSpace(claims.UserID) == "" {
		writeAuthError(w, http.StatusUnauthorized, "Invalid or expired token")
		return nil, false
	}
	return claims, true
}

func authAccountProfileResponse(user models.AuthUser) AuthAccountProfileResponse {
	email := strings.TrimSpace(user.Email)
	phone := strings.TrimSpace(user.Phone)
	missingEmail := email == ""
	missingPhone := missingAuthPhone(phone)
	contacts := make([]AuthContactRecordResponse, 0, 2)

	if !missingEmail {
		contacts = append(contacts, AuthContactRecordResponse{
			Type:      "email",
			Value:     email,
			Verified:  false,
			IsPrimary: true,
		})
	}
	if !missingPhone {
		contacts = append(contacts, AuthContactRecordResponse{
			Type:      "phone",
			Value:     phone,
			Verified:  false,
			IsPrimary: true,
		})
	}

	return AuthAccountProfileResponse{
		User:     user.ToResponse(),
		Contacts: contacts,
		Needs: AuthNeedsResponse{
			MissingEmail: missingEmail,
			MissingPhone: missingPhone,
		},
	}
}

func missingAuthPhone(phone string) bool {
	normalized := strings.ToLower(strings.TrimSpace(phone))
	return normalized == "" || strings.HasPrefix(normalized, "phone-not-set-")
}
