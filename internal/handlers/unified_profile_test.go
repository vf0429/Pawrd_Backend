package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupUnifiedProfileTestDB opens an in-memory DB with all tables the
// account/profile handlers touch, and points models.AuthDB at it.
func setupUnifiedProfileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-only-jwt-secret-at-least-32-characters")
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.AuthUser{},
		&models.Verification{},
		&models.Family{},
		&models.FamilyMember{},
		&models.Pet{},
		&models.PetPublicProfile{},
		&models.PetVisibilitySetting{},
		&models.PetDerivedSummary{},
		&models.Post{},
		&models.PostPetTag{},
		&models.PostImage{},
		&models.PostLike{},
		&models.PostCollection{},
		&models.PostComment{},
		&models.FamilyFollow{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	models.AuthDB = db
	return db
}

func seedUnifiedUser(t *testing.T, db *gorm.DB, email, name string) (string, string) {
	t.Helper()
	user := models.AuthUser{
		Email:        email,
		Phone:        phoneNotSetPrefix + email,
		PasswordHash: "x",
		Name:         name,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userID := fmt.Sprintf("%d", user.ID)
	token, err := auth.GenerateToken(userID, user.Email, user.Name)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return userID, token
}

func authedRequest(t *testing.T, method, path, token string, body any) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return httptest.NewRecorder(), req
}

func TestAuthMeUpdateHandler(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	userID, token := seedUnifiedUser(t, db, "me@example.com", "Old Name")

	rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me", token, map[string]any{
		"name":       "  New Name  ",
		"avatar_url": "https://example.com/a.png",
	})
	NewAuthMeUpdateHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		ID        string  `json:"id"`
		Name      string  `json:"name"`
		Email     string  `json:"email"`
		Phone     *string `json:"phone"`
		AvatarURL string  `json:"avatar_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ID != userID {
		t.Fatalf("expected id %s, got %s", userID, payload.ID)
	}
	if payload.Name != "New Name" {
		t.Fatalf("expected trimmed name, got %q", payload.Name)
	}
	if payload.AvatarURL != "https://example.com/a.png" {
		t.Fatalf("expected avatar url updated, got %q", payload.AvatarURL)
	}
	if payload.Phone != nil {
		t.Fatalf("expected placeholder phone to serialize as null, got %v", *payload.Phone)
	}
	if strings.Contains(rec.Body.String(), "PasswordHash") || strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("response must not leak password hash: %s", rec.Body.String())
	}

	var stored models.AuthUser
	if err := db.First(&stored, "id = ?", userID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Name != "New Name" || stored.AvatarURL != "https://example.com/a.png" {
		t.Fatalf("expected persisted update, got %+v", stored)
	}

	// Partial update: only name provided, avatar untouched.
	rec, req = authedRequest(t, http.MethodPatch, "/api/auth/me", token, map[string]any{"name": "Second Name"})
	NewAuthMeUpdateHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := db.First(&stored, "id = ?", userID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Name != "Second Name" || stored.AvatarURL != "https://example.com/a.png" {
		t.Fatalf("expected partial update to leave avatar intact, got %+v", stored)
	}
}

func TestAuthMeUpdateHandlerRequiresJWT(t *testing.T) {
	setupUnifiedProfileTestDB(t)

	rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me", "", map[string]any{"name": "X"})
	req.Header.Set("X-User-Id", "1") // legacy header must NOT be accepted
	NewAuthMeUpdateHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerifyHandlersRequireJWT(t *testing.T) {
	setupUnifiedProfileTestDB(t)

	sendBody := map[string]any{"contact": "me@example.com", "method": "email", "purpose": "change_email"}

	rec, req := authedRequest(t, http.MethodPost, "/api/auth/verify/send", "", sendBody)
	req.Header.Set("X-User-Id", "1") // forgeable header must NOT be accepted
	NewAuthVerifySendHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("verify/send: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec, req = authedRequest(t, http.MethodPost, "/api/auth/verify/check", "", map[string]any{
		"contact": "me@example.com", "method": "email", "purpose": "change_email", "code": "123456",
	})
	req.Header.Set("X-User-Id", "1")
	NewAuthVerifyCheckHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("verify/check: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerifySendAndCheckWithJWT(t *testing.T) {
	t.Setenv("DEV_EXPOSE_VERIFICATION_CODES", "true")
	db := setupUnifiedProfileTestDB(t)
	userID, token := seedUnifiedUser(t, db, "verify@example.com", "Verifier")

	rec, req := authedRequest(t, http.MethodPost, "/api/auth/verify/send", token, map[string]any{
		"contact": "new@example.com", "method": "email", "purpose": "change_email",
	})
	NewAuthVerifySendHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify/send: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var sendResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sendResp); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if sendResp.Code == "" {
		t.Fatalf("expected verification code in response")
	}

	var v models.Verification
	if err := db.Where("contact = ?", "new@example.com").First(&v).Error; err != nil {
		t.Fatalf("load verification: %v", err)
	}
	if v.UserID != userID {
		t.Fatalf("expected verification bound to JWT user %s, got %s", userID, v.UserID)
	}

	rec, req = authedRequest(t, http.MethodPost, "/api/auth/verify/check", token, map[string]any{
		"contact": "new@example.com", "method": "email", "purpose": "change_email", "code": sendResp.Code,
	})
	NewAuthVerifyCheckHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify/check: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var user models.AuthUser
	if err := db.First(&user, "id = ?", userID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if user.Email != "new@example.com" {
		t.Fatalf("expected email updated after verification, got %s", user.Email)
	}
}

func TestVerifySendHidesCodeInProductionMode(t *testing.T) {
	t.Setenv("DEV_EXPOSE_VERIFICATION_CODES", "false")
	db := setupUnifiedProfileTestDB(t)
	_, token := seedUnifiedUser(t, db, "prod@example.com", "Prod User")

	rec, req := authedRequest(t, http.MethodPost, "/api/auth/verify/send", token, map[string]any{
		"contact": "new@example.com", "method": "email", "purpose": "change_email",
	})
	NewAuthVerifySendHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["code"]; ok {
		t.Fatalf("production mode must not expose the code, got body=%s", rec.Body.String())
	}
	if _, ok := resp["expires_at"]; !ok {
		t.Fatalf("expected expires_at in response, got body=%s", rec.Body.String())
	}
}

func TestVerifySendExposesCodeInDevMode(t *testing.T) {
	t.Setenv("DEV_EXPOSE_VERIFICATION_CODES", "true")
	db := setupUnifiedProfileTestDB(t)
	_, token := seedUnifiedUser(t, db, "dev@example.com", "Dev User")

	rec, req := authedRequest(t, http.MethodPost, "/api/auth/verify/send", token, map[string]any{
		"contact": "new@example.com", "method": "email", "purpose": "change_email",
	})
	NewAuthVerifySendHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Code) != 6 {
		t.Fatalf("dev mode must expose the 6-digit code, got body=%s", rec.Body.String())
	}
}

func TestVerifyCheckEmailConflictDoesNotConsumeCode(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	other := models.AuthUser{Email: "taken@example.com", Phone: phoneNotSetPrefix + "taken", PasswordHash: "x", Name: "Taken"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	userID, token := seedUnifiedUser(t, db, "changer@example.com", "Changer")

	v := models.Verification{
		UserID:    userID,
		Contact:   "taken@example.com",
		Method:    "email",
		Purpose:   "change_email",
		Code:      "654321",
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := db.Create(&v).Error; err != nil {
		t.Fatalf("seed verification: %v", err)
	}

	rec, req := authedRequest(t, http.MethodPost, "/api/auth/verify/check", token, map[string]any{
		"contact": "taken@example.com", "method": "email", "purpose": "change_email", "code": "654321",
	})
	NewAuthVerifyCheckHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}

	var stored models.Verification
	if err := db.First(&stored, "id = ?", v.ID).Error; err != nil {
		t.Fatalf("reload verification: %v", err)
	}
	if stored.Verified {
		t.Fatalf("verification must NOT be consumed on conflict")
	}

	var user models.AuthUser
	if err := db.First(&user, "id = ?", userID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if user.Email != "changer@example.com" {
		t.Fatalf("account email must be unchanged on conflict, got %s", user.Email)
	}
}

func TestVerifyCheckRollsBackWhenContactUpdateFails(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	// JWT for a user that does not exist in auth_users → the contact update
	// inside the transaction fails and the code must stay unconsumed.
	token, err := auth.GenerateToken("999999", "ghost@example.com", "Ghost")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	v := models.Verification{
		UserID:    "999999",
		Contact:   "new@example.com",
		Method:    "email",
		Purpose:   "change_email",
		Code:      "111111",
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := db.Create(&v).Error; err != nil {
		t.Fatalf("seed verification: %v", err)
	}

	rec, req := authedRequest(t, http.MethodPost, "/api/auth/verify/check", token, map[string]any{
		"contact": "new@example.com", "method": "email", "purpose": "change_email", "code": "111111",
	})
	NewAuthVerifyCheckHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}

	var stored models.Verification
	if err := db.First(&stored, "id = ?", v.ID).Error; err != nil {
		t.Fatalf("reload verification: %v", err)
	}
	if stored.Verified {
		t.Fatalf("verification must NOT be consumed when the contact update fails")
	}
}

func TestCreateFamilyForUserConcurrent(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	// Serialize on one connection so SQLite shared-cache locking doesn't
	// produce spurious "table is locked" errors; the goroutines still race
	// between the check and the transactional create.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	userID, _ := seedUnifiedUser(t, db, "concurrent@example.com", "Concurrent User")
	var user models.AuthUser
	if err := db.First(&user, "id = ?", userID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = createFamilyForUser(db, &user)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	var familyCount int64
	if err := db.Model(&models.Family{}).Where("owner_user_id = ?", userID).Count(&familyCount).Error; err != nil {
		t.Fatalf("count families: %v", err)
	}
	if familyCount != 1 {
		t.Fatalf("expected exactly 1 family after %d concurrent calls, got %d", n, familyCount)
	}

	var memberCount int64
	if err := db.Model(&models.FamilyMember{}).Where("user_id = ? AND role = ?", userID, "owner").Count(&memberCount).Error; err != nil {
		t.Fatalf("count members: %v", err)
	}
	if memberCount != 1 {
		t.Fatalf("expected exactly 1 owner member, got %d", memberCount)
	}
}

func TestCreateFamilyForUserRollsBackOnMemberFailure(t *testing.T) {
	// DB without the family_members table → the member insert inside the
	// transaction fails and the family row must be rolled back.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Family{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user := &models.AuthUser{ID: 4242, Email: "orphan@example.com", Name: "Orphan"}
	if err := createFamilyForUser(db, user); err == nil {
		t.Fatalf("expected error from missing family_members table")
	}

	var familyCount int64
	if err := db.Model(&models.Family{}).Where("owner_user_id = ?", "4242").Count(&familyCount).Error; err != nil {
		t.Fatalf("count families: %v", err)
	}
	if familyCount != 0 {
		t.Fatalf("expected no orphaned family row, got %d", familyCount)
	}
}

func TestProfileMeHandlerCreatesFamilyIdempotently(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	userID, token := seedUnifiedUser(t, db, "profile@example.com", "Profile User")

	handler := NewProfileMeHandler(db)

	var lastFamilyID string
	for i := 0; i < 2; i++ {
		rec, req := authedRequest(t, http.MethodGet, "/api/profile/me", token, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d body=%s", i+1, rec.Code, rec.Body.String())
		}

		var payload struct {
			Account struct {
				ID    string  `json:"id"`
				Name  string  `json:"name"`
				Email string  `json:"email"`
				Phone *string `json:"phone"`
			} `json:"account"`
			Family struct {
				ID          string `json:"id"`
				OwnerUserID string `json:"owner_user_id"`
				MemberCount int    `json:"member_count"`
			} `json:"family"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if payload.Account.ID != userID || payload.Account.Email != "profile@example.com" {
			t.Fatalf("unexpected account payload: %+v", payload.Account)
		}
		if payload.Account.Phone != nil {
			t.Fatalf("expected placeholder phone as null, got %v", *payload.Account.Phone)
		}
		if payload.Family.ID == "" || payload.Family.OwnerUserID != userID {
			t.Fatalf("unexpected family payload: %+v", payload.Family)
		}
		if payload.Family.MemberCount != 1 {
			t.Fatalf("expected exactly one family member, got %d", payload.Family.MemberCount)
		}
		if lastFamilyID == "" {
			lastFamilyID = payload.Family.ID
		} else if payload.Family.ID != lastFamilyID {
			t.Fatalf("expected idempotent family creation, got %s then %s", lastFamilyID, payload.Family.ID)
		}
	}

	var familyCount int64
	if err := db.Model(&models.Family{}).Where("owner_user_id = ?", userID).Count(&familyCount).Error; err != nil {
		t.Fatalf("count families: %v", err)
	}
	if familyCount != 1 {
		t.Fatalf("expected exactly 1 family after repeated calls, got %d", familyCount)
	}
}

func TestProfileMeHandlerRequiresJWT(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)

	rec, req := authedRequest(t, http.MethodGet, "/api/profile/me", "", nil)
	req.Header.Set("X-User-Id", "1")
	NewProfileMeHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthMePhoneUpdateHandler(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	userID, token := seedUnifiedUser(t, db, "phone@example.com", "Phone User")

	rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me/phone", token, map[string]any{"phone": "9123 4567"})
	NewAuthMePhoneUpdateHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		ID    string  `json:"id"`
		Phone *string `json:"phone"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ID != userID {
		t.Fatalf("expected id %s, got %s", userID, payload.ID)
	}
	if payload.Phone == nil || *payload.Phone != "+85291234567" {
		t.Fatalf("expected normalized phone +85291234567, got %v", payload.Phone)
	}

	var stored models.AuthUser
	if err := db.First(&stored, "id = ?", userID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Phone != "+85291234567" {
		t.Fatalf("expected persisted normalized phone, got %s", stored.Phone)
	}

	// +852-prefixed input should also normalize (fresh user — the endpoint is
	// first-time-only, so the previous user now has a real phone).
	userID2, token2 := seedUnifiedUser(t, db, "phone2@example.com", "Phone User Two")
	rec, req = authedRequest(t, http.MethodPatch, "/api/auth/me/phone", token2, map[string]any{"phone": "+852 6123 4567"})
	NewAuthMePhoneUpdateHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for +852 input, got %d body=%s", rec.Code, rec.Body.String())
	}
	var stored2 models.AuthUser
	if err := db.First(&stored2, "id = ?", userID2).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored2.Phone != "+85261234567" {
		t.Fatalf("expected +85261234567, got %s", stored2.Phone)
	}
}

func TestAuthMePhoneUpdateHandlerRejectsRealPhoneChange(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	user := models.AuthUser{Email: "real@example.com", Phone: "+85291234567", PasswordHash: "x", Name: "Real Phone"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	token, err := auth.GenerateToken(fmt.Sprintf("%d", user.ID), user.Email, user.Name)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me/phone", token, map[string]any{"phone": "69876543"})
	NewAuthMePhoneUpdateHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for account with a real phone, got %d body=%s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if !strings.Contains(errResp.Error, "verification flow") {
		t.Fatalf("expected error directing to the verification flow, got %q", errResp.Error)
	}

	var stored models.AuthUser
	if err := db.First(&stored, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if stored.Phone != "+85291234567" {
		t.Fatalf("phone must be unchanged, got %s", stored.Phone)
	}
}

func TestAuthMePhoneUpdateHandlerRejectsInvalid(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, token := seedUnifiedUser(t, db, "bad-phone@example.com", "Bad Phone")

	for _, phone := range []string{"12345678", "1234567", "912345678", "+1 415 555 0132", ""} {
		rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me/phone", token, map[string]any{"phone": phone})
		NewAuthMePhoneUpdateHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("phone %q: expected 400, got %d body=%s", phone, rec.Code, rec.Body.String())
		}
	}
}

func TestAuthMePhoneUpdateHandlerConflict(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	other := models.AuthUser{Email: "taken@example.com", Phone: "+85291234567", PasswordHash: "x", Name: "Taken"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	_, token := seedUnifiedUser(t, db, "want-phone@example.com", "Want Phone")

	rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me/phone", token, map[string]any{"phone": "91234567"})
	NewAuthMePhoneUpdateHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Error == "" {
		t.Fatalf("expected clear conflict error message")
	}
}

func TestAuthMePhoneUpdateHandlerRequiresJWT(t *testing.T) {
	setupUnifiedProfileTestDB(t)

	rec, req := authedRequest(t, http.MethodPatch, "/api/auth/me/phone", "", map[string]any{"phone": "91234567"})
	req.Header.Set("X-User-Id", "1") // legacy header must NOT be accepted
	NewAuthMePhoneUpdateHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFamilyProfileMePutViaJWT(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	userID, token := seedUnifiedUser(t, db, "family@example.com", "Family Owner")

	rec, req := authedRequest(t, http.MethodPut, "/api/domain/families/me", token, map[string]any{
		"display_name": "The JWT Family",
		"handle":       "jwt-family",
		"bio":          "hello",
	})
	// Deliberately no X-User-Id header — the JWT must be enough.
	NewFamilyProfileMeHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		ID          string `json:"id"`
		Handle      string `json:"handle"`
		DisplayName string `json:"display_name"`
		OwnerUserID string `json:"owner_user_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.OwnerUserID != userID {
		t.Fatalf("expected family owned by JWT user %s, got %s", userID, payload.OwnerUserID)
	}
	if payload.Handle != "jwt-family" || payload.DisplayName != "The JWT Family" {
		t.Fatalf("unexpected family payload: %+v", payload)
	}
}

func TestFamilyProfileMePutRejectsXUserIDOnly(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)

	req := httptest.NewRequest(http.MethodPut, "/api/domain/families/me", strings.NewReader(`{"display_name":"Legacy Family","handle":"legacy-family"}`))
	req.Header.Set("X-User-Id", "legacy-user-1")
	rec := httptest.NewRecorder()
	NewFamilyProfileMeHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("writes are JWT-only: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFamilyProfileMePutRejectsInvalidBearerWithForgedHeader(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)

	req := httptest.NewRequest(http.MethodPut, "/api/domain/families/me", strings.NewReader(`{"display_name":"Forged Family","handle":"forged-family"}`))
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	req.Header.Set("X-User-Id", "forged-user-1")
	rec := httptest.NewRecorder()
	NewFamilyProfileMeHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid Bearer must not fall back to X-User-Id: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	var familyCount int64
	if err := db.Model(&models.Family{}).Count(&familyCount).Error; err != nil {
		t.Fatalf("count families: %v", err)
	}
	if familyCount != 0 {
		t.Fatalf("expected no family created, got %d", familyCount)
	}
}

func TestFamilyProfileMePutRejectsAnonymous(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)

	req := httptest.NewRequest(http.MethodPut, "/api/domain/families/me", strings.NewReader(`{"display_name":"X","handle":"x"}`))
	rec := httptest.NewRecorder()
	NewFamilyProfileMeHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}
