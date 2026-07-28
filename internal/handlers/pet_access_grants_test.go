package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupGrantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.AuthUser{}, &models.PetAccessGrant{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	models.AuthDB = db
	return db
}

func seedAuthUsers(t *testing.T, db *gorm.DB) (ownerID string, recipientID string, token string) {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-only-jwt-secret-at-least-32-characters")
	owner := models.AuthUser{Email: "owner@example.com", Phone: "owner-phone", PasswordHash: "x", Name: "Owner"}
	recipient := models.AuthUser{Email: "vet@example.com", Phone: "vet-phone", PasswordHash: "x", Name: "Vet"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if err := db.Create(&recipient).Error; err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	ownerID = owner.ToResponse().ID
	recipientID = recipient.ToResponse().ID
	jwt, err := auth.GenerateToken(ownerID, owner.Email, owner.Name)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return ownerID, recipientID, jwt
}

func TestCreateListRevokeAndResolveGrant(t *testing.T) {
	db := setupGrantTestDB(t)
	t.Setenv("SHARE_PUBLIC_BASE_URL", "https://pawrd.app")
	ownerID, recipientID, token := seedAuthUsers(t, db)
	_ = ownerID

	body := map[string]any{
		"recipient_user_id":      recipientID,
		"recipient_display_name": "Dr. Vet",
		"recipient_kind":         "veterinarian",
		"scenario":               "vetVisit",
		"scopes":                 []string{"identity", "medicalSummary"},
		"allow_download":         false,
		"starts_at":              time.Now().UTC().Format(time.RFC3339),
		"expires_at":             time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/profile/pets/pet-123/share-grants", bytes.NewReader(payload))
	req.SetPathValue("petId", "pet-123")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	NewPetAccessGrantsHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var createResp struct {
		Grant struct {
			ID         string   `json:"id"`
			ShareToken string   `json:"share_token"`
			ShareURL   string   `json:"share_url"`
			Scopes     []string `json:"scopes"`
			Status     string   `json:"status"`
		} `json:"grant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createResp.Grant.ID == "" || createResp.Grant.ShareToken == "" || createResp.Grant.ShareURL == "" {
		t.Fatalf("expected tokenized response, got %+v", createResp.Grant)
	}
	if createResp.Grant.ShareURL != "https://pawrd.app/share/"+createResp.Grant.ShareToken {
		t.Fatalf("expected front-end share URL, got %s", createResp.Grant.ShareURL)
	}
	if createResp.Grant.Status != "active" {
		t.Fatalf("expected active status, got %s", createResp.Grant.Status)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/profile/pets/pet-123/share-grants", nil)
	req.SetPathValue("petId", "pet-123")
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	NewPetAccessGrantsHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/share/"+createResp.Grant.ShareToken, nil)
	req.SetPathValue("token", createResp.Grant.ShareToken)
	rec = httptest.NewRecorder()
	NewShareResolveHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 resolving share, got %d body=%s", rec.Code, rec.Body.String())
	}

	revokePayload := []byte(`{"reason":"test revoke"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/profile/pets/pet-123/share-grants/"+createResp.Grant.ID+"/revoke", bytes.NewReader(revokePayload))
	req.SetPathValue("petId", "pet-123")
	req.SetPathValue("grantId", createResp.Grant.ID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	NewPetAccessGrantRevokeHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 revoke, got %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/share/"+createResp.Grant.ShareToken, nil)
	req.SetPathValue("token", createResp.Grant.ShareToken)
	rec = httptest.NewRecorder()
	NewShareResolveHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("expected 410 after revoke, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSharePublicBaseURLPriority(t *testing.T) {
	for _, key := range []string{
		"SHARE_PUBLIC_BASE_URL",
		"PUBLIC_WEB_BASE_URL",
		"NEXT_PUBLIC_SITE_URL",
		"PUBLIC_BASE_URL",
	} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}

	t.Setenv("PUBLIC_BASE_URL", "https://backend.example.com")
	if got := sharePublicBaseURL(); got != "https://backend.example.com" {
		t.Fatalf("expected PUBLIC_BASE_URL fallback, got %s", got)
	}

	t.Setenv("NEXT_PUBLIC_SITE_URL", "https://site.example.com")
	if got := sharePublicBaseURL(); got != "https://site.example.com" {
		t.Fatalf("expected NEXT_PUBLIC_SITE_URL override, got %s", got)
	}

	t.Setenv("PUBLIC_WEB_BASE_URL", "https://web.example.com")
	if got := sharePublicBaseURL(); got != "https://web.example.com" {
		t.Fatalf("expected PUBLIC_WEB_BASE_URL override, got %s", got)
	}

	t.Setenv("SHARE_PUBLIC_BASE_URL", "https://share.example.com")
	if got := sharePublicBaseURL(); got != "https://share.example.com" {
		t.Fatalf("expected SHARE_PUBLIC_BASE_URL override, got %s", got)
	}
}

func TestShareURLFallsBackToBackendRoute(t *testing.T) {
	db := setupGrantTestDB(t)
	t.Setenv("PUBLIC_BASE_URL", "http://localhost:8000")
	ownerID, recipientID, token := seedAuthUsers(t, db)
	_ = ownerID

	body := map[string]any{
		"recipient_user_id":      recipientID,
		"recipient_display_name": "Dr. Vet",
		"recipient_kind":         "veterinarian",
		"scenario":               "vetVisit",
		"scopes":                 []string{"identity"},
		"allow_download":         false,
		"starts_at":              time.Now().UTC().Format(time.RFC3339),
		"expires_at":             time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}
	payload, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/profile/pets/pet-123/share-grants", bytes.NewReader(payload))
	req.SetPathValue("petId", "pet-123")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	NewPetAccessGrantsHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var createResp struct {
		Grant struct {
			ShareToken string `json:"share_token"`
			ShareURL   string `json:"share_url"`
		} `json:"grant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createResp.Grant.ShareURL != "http://localhost:8000/api/share/"+createResp.Grant.ShareToken {
		t.Fatalf("expected backend share URL fallback, got %s", createResp.Grant.ShareURL)
	}
}
