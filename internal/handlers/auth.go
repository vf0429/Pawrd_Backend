package handlers

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ── Login ──────────────────────────────────────────────────────────────────

type LoginRequest struct {
	Identifier string `json:"identifier"` // email or phone
	Password   string `json:"password"`
}

// NewAuthLoginHandler handles POST /api/auth/login
// Returns a 1-year JWT so the user stays signed in like Instagram.
func NewAuthLoginHandler(db *gorm.DB) http.HandlerFunc {
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

		// Ensure a family exists for this user; create a default one if missing.
		ensureFamilyForUser(db, &user)

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
func NewAuthRegisterHandler(db *gorm.DB) http.HandlerFunc {
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
			Phone:        "phone-not-set-" + uuid.NewString(),
			PasswordHash: string(hash),
			Name:         req.Name,
		}
		if err := models.AuthDB.Create(&user).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to create account")
			return
		}

		// Create the family profile immediately so the first-time setup can edit it.
		if err := createFamilyForUser(db, &user); err != nil {
			// Non-fatal: log but still return the auth token so the client can retry setup later.
			fmt.Printf("Failed to auto-create family for user %d: %v\n", user.ID, err)
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

// ── Verification ───────────────────────────────────────────────────────────

type VerifySendRequest struct {
	Contact string `json:"contact"`
	Method  string `json:"method"`  // email | phone
	Purpose string `json:"purpose"` // new_account | change_email | change_phone
}

type VerifySendResponse struct {
	Code    string `json:"code"`
	Expires int64  `json:"expires_at"`
}

type VerifyCheckRequest struct {
	Contact string `json:"contact"`
	Method  string `json:"method"`
	Purpose string `json:"purpose"`
	Code    string `json:"code"`
}

// NewAuthVerifySendHandler handles POST /api/auth/verify/send (dev-only OTP)
func NewAuthVerifySendHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req VerifySendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req.Contact = strings.TrimSpace(strings.ToLower(req.Contact))
		req.Method = strings.TrimSpace(strings.ToLower(req.Method))
		req.Purpose = strings.TrimSpace(strings.ToLower(req.Purpose))

		if req.Contact == "" || (req.Method != "email" && req.Method != "phone") {
			writeAuthError(w, http.StatusBadRequest, "Contact and valid method are required")
			return
		}

		code := generateVerificationCode()
		expiresAt := time.Now().UTC().Add(15 * time.Minute)

		v := models.Verification{
			UserID:    strings.TrimSpace(r.Header.Get("X-User-Id")),
			Contact:   req.Contact,
			Method:    req.Method,
			Code:      code,
			Purpose:   req.Purpose,
			ExpiresAt: expiresAt,
		}
		if err := models.AuthDB.Create(&v).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to store verification code")
			return
		}

		// Dev-only: print the code to stdout so tests can copy it without SMTP/SMS.
		fmt.Printf("[DEV-VERIFICATION] %s %s code for %s: %s (expires %s)\n",
			req.Purpose, req.Method, req.Contact, code, expiresAt.Format(time.RFC3339))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(VerifySendResponse{
			Code:    code,
			Expires: expiresAt.Unix(),
		})
	}
}

// NewAuthVerifyCheckHandler handles POST /api/auth/verify/check
func NewAuthVerifyCheckHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req VerifyCheckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req.Contact = strings.TrimSpace(strings.ToLower(req.Contact))
		req.Method = strings.TrimSpace(strings.ToLower(req.Method))
		req.Purpose = strings.TrimSpace(strings.ToLower(req.Purpose))
		req.Code = strings.TrimSpace(req.Code)

		userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if userID == "" {
			writeAuthError(w, http.StatusBadRequest, "User identification is required")
			return
		}

		var v models.Verification
		err := models.AuthDB.Where(
			"user_id = ? AND contact = ? AND method = ? AND purpose = ? AND code = ? AND verified = ? AND expires_at > ?",
			userID, req.Contact, req.Method, req.Purpose, req.Code, false, time.Now().UTC(),
		).Order("created_at DESC").First(&v).Error
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "Invalid or expired verification code")
			return
		}

		v.Verified = true
		models.AuthDB.Save(&v)

		// For credential changes, update the user record immediately.
		if req.Purpose == "change_email" || req.Purpose == "change_phone" {
			updateVerifiedContact(userID, req.Contact, req.Method)
		}

		writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
	}
}

func updateVerifiedContact(userID, contact, method string) {
	var user models.AuthUser
	if err := models.AuthDB.First(&user, "id = ?", userID).Error; err != nil {
		return
	}
	if method == "email" {
		user.Email = contact
	} else {
		user.Phone = contact
	}
	models.AuthDB.Save(&user)
}

func generateVerificationCode() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

// ── Family helpers ─────────────────────────────────────────────────────────

func createFamilyForUser(db *gorm.DB, user *models.AuthUser) error {
	if db == nil {
		return fmt.Errorf("main db not available")
	}
	ownerID := fmt.Sprintf("%d", user.ID)

	var existing models.Family
	if err := db.Where("owner_user_id = ?", ownerID).First(&existing).Error; err == nil {
		return nil // already has a family
	}

	displayName := fmt.Sprintf("The %s Family", user.Name)
	baseHandle := slugifyForHandle(user.Name)
	handle, err := uniqueFamilyHandle(db, baseHandle)
	if err != nil {
		return err
	}

	family := models.Family{
		OwnerUserID: ownerID,
		DisplayName: displayName,
		Handle:      handle,
		AvatarURL:   "",
		Bio:         "",
		City:        "",
		IsPublic:    true,
	}
	if err := db.Create(&family).Error; err != nil {
		return err
	}

	member := models.FamilyMember{
		FamilyID:     family.ID,
		UserID:       ownerID,
		DisplayName:  user.Name,
		Role:         "owner",
		Relationship: "Parent",
		IsPrimary:    true,
	}
	return db.Create(&member).Error
}

func ensureFamilyForUser(db *gorm.DB, user *models.AuthUser) {
	if db == nil {
		return
	}
	_ = createFamilyForUser(db, user)
}

func uniqueFamilyHandle(db *gorm.DB, base string) (string, error) {
	candidates := []string{base}
	for i := 1; i <= 100; i++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", base, i))
	}
	candidates = append(candidates, fmt.Sprintf("%s-%s", base, uuid.NewString()[:6]))

	for _, handle := range candidates {
		var count int64
		if err := db.Model(&models.Family{}).Where("handle = ?", handle).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return handle, nil
		}
	}
	return "", fmt.Errorf("could not generate unique handle")
}

func slugifyForHandle(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	// Replace spaces and special chars with hyphens
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug := re.ReplaceAllString(lower, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "family"
	}
	return slug
}

// ── Helper ─────────────────────────────────────────────────────────────────

func writeAuthError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
