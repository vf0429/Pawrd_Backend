package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
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
			Phone:        phoneNotSetPrefix + uuid.NewString(),
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
	Code    string `json:"code,omitempty"`
	Expires int64  `json:"expires_at"`
}

// exposeVerificationCodes reports whether dev mode is enabled: OTP codes may be
// returned in API responses and printed to stdout. Off by default — set
// DEV_EXPOSE_VERIFICATION_CODES=true in development only.
func exposeVerificationCodes() bool {
	return strings.TrimSpace(strings.ToLower(os.Getenv("DEV_EXPOSE_VERIFICATION_CODES"))) == "true"
}

type VerifyCheckRequest struct {
	Contact string `json:"contact"`
	Method  string `json:"method"`
	Purpose string `json:"purpose"`
	Code    string `json:"code"`
}

// NewAuthVerifySendHandler handles POST /api/auth/verify/send (dev-only OTP)
// Requires Bearer JWT — the user is identified from the token, never from X-User-Id.
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

		userID, ok := authenticatedUserID(w, r)
		if !ok {
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
			UserID:    userID,
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
		// In production the code is never exposed via API or logs.
		resp := VerifySendResponse{Expires: expiresAt.Unix()}
		if exposeVerificationCodes() {
			resp.Code = code
			fmt.Printf("[DEV-VERIFICATION] %s %s code for %s: %s (expires %s)\n",
				req.Purpose, req.Method, req.Contact, code, expiresAt.Format(time.RFC3339))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// NewAuthVerifyCheckHandler handles POST /api/auth/verify/check
// Requires Bearer JWT — the user is identified from the token, never from X-User-Id.
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

		userID, ok := authenticatedUserID(w, r)
		if !ok {
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

		changeContact := req.Purpose == "change_email" || req.Purpose == "change_phone"

		// Consume the code and update the contact in ONE transaction: any
		// failure (conflict, DB error) rolls back so the code stays unconsumed.
		err := models.AuthDB.Transaction(func(tx *gorm.DB) error {
			var v models.Verification
			if err := tx.Where(
				"user_id = ? AND contact = ? AND method = ? AND purpose = ? AND code = ? AND verified = ? AND expires_at > ?",
				userID, req.Contact, req.Method, req.Purpose, req.Code, false, time.Now().UTC(),
			).Order("created_at DESC").First(&v).Error; err != nil {
				return errInvalidVerificationCode
			}

			if changeContact {
				var user models.AuthUser
				if err := tx.First(&user, "id = ?", userID).Error; err != nil {
					return err
				}

				field := "phone"
				if req.Method == "email" {
					field = "email"
				}
				var conflict int64
				if err := tx.Model(&models.AuthUser{}).
					Where(field+" = ? AND id != ?", req.Contact, user.ID).
					Count(&conflict).Error; err != nil {
					return err
				}
				if conflict > 0 {
					return errContactAlreadyInUse
				}

				if req.Method == "email" {
					user.Email = req.Contact
				} else {
					user.Phone = req.Contact
				}
				if err := tx.Save(&user).Error; err != nil {
					return err
				}
			}

			v.Verified = true
			return tx.Save(&v).Error
		})
		switch {
		case errors.Is(err, errInvalidVerificationCode):
			writeAuthError(w, http.StatusUnauthorized, "Invalid or expired verification code")
			return
		case errors.Is(err, errContactAlreadyInUse):
			writeAuthError(w, http.StatusConflict, "Contact already in use by another account")
			return
		case err != nil:
			writeAuthError(w, http.StatusInternalServerError, "Failed to complete verification")
			return
		}

		writeJSON(w, http.StatusOK, map[string]bool{"verified": true})
	}
}

var (
	errInvalidVerificationCode = errors.New("invalid or expired verification code")
	errContactAlreadyInUse     = errors.New("contact already in use")
)

func generateVerificationCode() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

// ── Account (self-service profile) ─────────────────────────────────────────

// accountPayload is the safe account representation for /api/auth/me and /api/profile/me.
// Phone is omitted (null) when unset or still a registration placeholder.
type accountPayload struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	Phone     *string `json:"phone"`
	AvatarURL string  `json:"avatar_url"`
}

const phoneNotSetPrefix = "phone-not-set-"

func accountPayloadOf(user *models.AuthUser) accountPayload {
	return accountPayload{
		ID:        fmt.Sprintf("%d", user.ID),
		Name:      user.Name,
		Email:     user.Email,
		Phone:     publicPhone(user.Phone),
		AvatarURL: user.AvatarURL,
	}
}

func publicPhone(phone string) *string {
	trimmed := strings.TrimSpace(phone)
	if trimmed == "" || strings.HasPrefix(trimmed, phoneNotSetPrefix) {
		return nil
	}
	return &trimmed
}

type UpdateAccountRequest struct {
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

// NewAuthMeUpdateHandler handles PATCH /api/auth/me
// Requires Bearer JWT. Updates only the fields present in the body.
func NewAuthMeUpdateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPatch {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		var req UpdateAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		var user models.AuthUser
		if err := models.AuthDB.First(&user, "id = ?", userID).Error; err != nil {
			writeAuthError(w, http.StatusNotFound, "Account not found")
			return
		}

		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				writeAuthError(w, http.StatusBadRequest, "Name cannot be empty")
				return
			}
			user.Name = name
		}
		if req.AvatarURL != nil {
			user.AvatarURL = strings.TrimSpace(*req.AvatarURL)
		}

		if err := models.AuthDB.Save(&user).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to update account")
			return
		}

		writeJSON(w, http.StatusOK, accountPayloadOf(&user))
	}
}

type UpdatePhoneRequest struct {
	Phone string `json:"phone"`
}

// NewAuthMePhoneUpdateHandler handles PATCH /api/auth/me/phone
// Requires Bearer JWT. First-time completion only: it can replace a
// registration placeholder phone; changing a real phone must go through the
// verification flow (403 otherwise). Normalizes HK numbers to +852XXXXXXXX
// and enforces uniqueness across accounts.
func NewAuthMePhoneUpdateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPatch {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		var req UpdatePhoneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		normalized, ok := normalizeHKPhone(req.Phone)
		if !ok {
			writeAuthError(w, http.StatusBadRequest, "Invalid Hong Kong phone number")
			return
		}

		var user models.AuthUser
		if err := models.AuthDB.First(&user, "id = ?", userID).Error; err != nil {
			writeAuthError(w, http.StatusNotFound, "Account not found")
			return
		}

		// First-time completion only: changing an already-verified real phone
		// must go through the OTP flow (verify/send + verify/check change_phone).
		if !strings.HasPrefix(strings.TrimSpace(user.Phone), phoneNotSetPrefix) {
			writeAuthError(w, http.StatusForbidden, "Phone number already set; use the verification flow to change it")
			return
		}

		var conflictCount int64
		if err := models.AuthDB.Model(&models.AuthUser{}).
			Where("phone = ? AND id != ?", normalized, user.ID).
			Count(&conflictCount).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to check phone availability")
			return
		}
		if conflictCount > 0 {
			writeAuthError(w, http.StatusConflict, "Phone number already in use by another account")
			return
		}

		user.Phone = normalized
		if err := models.AuthDB.Save(&user).Error; err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to update phone number")
			return
		}

		writeJSON(w, http.StatusOK, accountPayloadOf(&user))
	}
}

// normalizeHKPhone normalizes a Hong Kong mobile number to +852XXXXXXXX.
// Accepts 8 digits starting with 5/6/9, optionally prefixed with 852 or +852.
func normalizeHKPhone(phone string) (string, bool) {
	var digits strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if strings.HasPrefix(d, "852") && len(d) == 11 {
		d = d[3:]
	}
	if len(d) != 8 {
		return "", false
	}
	switch d[0] {
	case '5', '6', '9':
		return "+852" + d, true
	}
	return "", false
}

// ── Family helpers ─────────────────────────────────────────────────────────

func createFamilyForUser(db *gorm.DB, user *models.AuthUser) error {
	if db == nil {
		return fmt.Errorf("main db not available")
	}
	ownerID := fmt.Sprintf("%d", user.ID)

	var existing models.Family
	err := db.Where("owner_user_id = ?", ownerID).First(&existing).Error
	if err == nil {
		return nil // already has a family
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to check existing family: %w", err)
	}

	displayName := fmt.Sprintf("The %s Family", user.Name)
	baseHandle := slugifyForHandle(user.Name)

	// Family + owner member are created in ONE transaction. On a unique
	// conflict (a concurrent request created the family first) the canonical
	// family is re-selected; a handle collision retries with a fresh handle.
	for attempt := 0; attempt < 3; attempt++ {
		handle, err := uniqueFamilyHandle(db, baseHandle)
		if err != nil {
			return err
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			family := models.Family{
				OwnerUserID: ownerID,
				DisplayName: displayName,
				Handle:      handle,
				AvatarURL:   "",
				Bio:         "",
				City:        "",
				IsPublic:    true,
			}
			if err := tx.Create(&family).Error; err != nil {
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
			return tx.Create(&member).Error
		})
		if err == nil {
			return nil
		}
		if !isUniqueConstraintError(err) {
			return err
		}

		// Unique conflict: if the owner_user_id row now exists, a concurrent
		// request won the race — that family is the canonical one.
		var canonical models.Family
		if selErr := db.Where("owner_user_id = ?", ownerID).First(&canonical).Error; selErr == nil {
			return nil
		}
		// Otherwise it was a handle collision; retry with a fresh handle.
	}
	return fmt.Errorf("could not create family: handle conflicts after retries")
}

// isUniqueConstraintError matches unique-constraint violations across SQLite
// and PostgreSQL without requiring gorm's TranslateError option.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key")
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
