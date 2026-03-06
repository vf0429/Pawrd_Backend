package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/vf0429/Petwell_Backend/internal/auth"
	"github.com/vf0429/Petwell_Backend/internal/models"
	"golang.org/x/crypto/bcrypt"
)

// ── Login ──────────────────────────────────────────────────────────────────

type LoginRequest struct {
	Identifier string `json:"identifier"` // email or phone
	Password   string `json:"password"`
}

// NewAuthLoginHandler handles POST /api/auth/login
// Returns a 1-year JWT so the user stays signed in like Instagram.
func NewAuthLoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		identifier := strings.TrimSpace(req.Identifier)
		if identifier == "" || req.Password == "" {
			writeAuthError(w, http.StatusBadRequest, "Email and password are required")
			return
		}

		var user models.AuthUser
		if err := models.AuthDB.Where("email = ? OR phone = ?", identifier, identifier).First(&user).Error; err != nil {
			writeAuthError(w, http.StatusUnauthorized, "Invalid credentials")
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
			writeAuthError(w, http.StatusUnauthorized, "Invalid credentials")
			return
		}

		token, err := auth.GenerateToken(fmt.Sprintf("%d", user.ID), user.Email, user.Name)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to generate token")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.AuthTokenResponse{
			Token: token,
			User:  user.ToResponse(),
		})
	}
}

// ── Register ───────────────────────────────────────────────────────────────

type RegisterRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// NewAuthRegisterHandler handles POST /api/auth/register
func NewAuthRegisterHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		req.Email = strings.TrimSpace(strings.ToLower(req.Email))

		if req.Name == "" || req.Email == "" || req.Password == "" {
			writeAuthError(w, http.StatusBadRequest, "Name, email and password are required")
			return
		}
		if len(req.Password) < 6 {
			writeAuthError(w, http.StatusBadRequest, "Password must be at least 6 characters")
			return
		}

		// Check duplicate email
		var existing models.AuthUser
		if err := models.AuthDB.Where("email = ?", req.Email).First(&existing).Error; err == nil {
			writeAuthError(w, http.StatusConflict, "Email already registered")
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to process password")
			return
		}

		// Phone is required by the schema but not by this flow — use a unique placeholder
		user := models.AuthUser{
			Email:        req.Email,
			Phone:        "phone-not-set-" + uuid.New().String(),
			PasswordHash: string(hash),
			Name:         req.Name,
		}
		if err := models.AuthDB.Create(&user).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to create account")
			return
		}

		token, err := auth.GenerateToken(fmt.Sprintf("%d", user.ID), user.Email, user.Name)
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to generate token")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(models.AuthTokenResponse{
			Token: token,
			User:  user.ToResponse(),
		})
	}
}

// ── Helper ─────────────────────────────────────────────────────────────────

func writeAuthError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
