package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const authMeTestJWTSecret = "test-only-auth-me-jwt-secret-at-least-32-characters"

func setupAuthMeTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open auth me test db: %v", err)
	}
	if err := db.AutoMigrate(&models.AuthUser{}); err != nil {
		t.Fatalf("migrate auth me test db: %v", err)
	}

	previousAuthDB := models.AuthDB
	models.AuthDB = db
	t.Cleanup(func() {
		models.AuthDB = previousAuthDB
	})
	t.Setenv("JWT_SECRET", authMeTestJWTSecret)
	return db
}

func authMeTestToken(t *testing.T, userID, email string) string {
	t.Helper()

	token, err := auth.GenerateToken(userID, email, "Legacy Session")
	if err != nil {
		t.Fatalf("generate auth me test token: %v", err)
	}
	return token
}

func performAuthMeTestRequest(
	handler http.Handler,
	method string,
	token string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/api/auth/me", nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestAuthMeReturnsCurrentDBProfileForLegacyJWT(t *testing.T) {
	db := setupAuthMeTestDB(t)
	user := models.AuthUser{
		Email:        "current@example.com",
		Phone:        "phone-not-set-" + uuid.NewString(),
		PasswordHash: "test-password-hash",
		Name:         "Alice",
		AvatarURL:    "https://cdn.example.com/alice.jpg",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create auth user: %v", err)
	}

	// The token deliberately contains an old email. user_id remains the stable
	// lookup key and the response must return the current AuthDB email.
	token := authMeTestToken(t, "1", "legacy@example.com")
	response := performAuthMeTestRequest(NewAuthMeHandler(), http.MethodGet, token)
	if response.Code != http.StatusOK {
		t.Fatalf("auth me status=%d body=%s", response.Code, response.Body.String())
	}

	var profile AuthAccountProfileResponse
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode auth me response: %v", err)
	}
	if profile.User.ID != "1" ||
		profile.User.Email != "current@example.com" ||
		profile.User.Name != "Alice" ||
		profile.User.AvatarURL != "https://cdn.example.com/alice.jpg" {
		t.Fatalf("unexpected auth user response: %+v", profile.User)
	}
	if len(profile.Contacts) != 1 {
		t.Fatalf("contacts=%+v, want only the email contact", profile.Contacts)
	}
	email := profile.Contacts[0]
	if email.Type != "email" || email.Value != "current@example.com" ||
		email.Verified || !email.IsPrimary {
		t.Fatalf("unexpected email contact: %+v", email)
	}
	if profile.Needs.MissingEmail || !profile.Needs.MissingPhone {
		t.Fatalf("unexpected completion needs: %+v", profile.Needs)
	}
	if strings.Contains(response.Body.String(), "phone-not-set-") {
		t.Fatalf("placeholder phone leaked in response: %s", response.Body.String())
	}
}

func TestAuthMeReturnsCollectedPhoneAsUnverifiedContact(t *testing.T) {
	db := setupAuthMeTestDB(t)
	user := models.AuthUser{
		Email:        "phone@example.com",
		Phone:        "+85261234567",
		PasswordHash: "test-password-hash",
		Name:         "Phone User",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create auth user: %v", err)
	}

	response := performAuthMeTestRequest(
		NewAuthMeHandler(),
		http.MethodGet,
		authMeTestToken(t, "1", user.Email),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("auth me status=%d body=%s", response.Code, response.Body.String())
	}

	var profile AuthAccountProfileResponse
	if err := json.Unmarshal(response.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode auth me response: %v", err)
	}
	if len(profile.Contacts) != 2 {
		t.Fatalf("contacts=%+v, want email and phone", profile.Contacts)
	}
	phone := profile.Contacts[1]
	if phone.Type != "phone" || phone.Value != "+85261234567" ||
		phone.Verified || !phone.IsPrimary {
		t.Fatalf("unexpected phone contact: %+v", phone)
	}
	if profile.Needs.MissingEmail || profile.Needs.MissingPhone {
		t.Fatalf("unexpected completion needs: %+v", profile.Needs)
	}
}

func TestAuthMeRejectsMissingInvalidAndUserlessJWT(t *testing.T) {
	setupAuthMeTestDB(t)
	userlessToken := authMeTestToken(t, "", "alice@example.com")

	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "missing bearer token"},
		{name: "invalid bearer token", token: "not-a-valid-jwt"},
		{name: "signed token without user id", token: userlessToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := performAuthMeTestRequest(NewAuthMeHandler(), http.MethodGet, test.token)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode auth error: %v", err)
			}
			if strings.TrimSpace(payload["error"]) == "" {
				t.Fatalf("missing auth error message: %s", response.Body.String())
			}
		})
	}
}

func TestAuthMeReturnsNotFoundForUnknownJWTUser(t *testing.T) {
	setupAuthMeTestDB(t)
	response := performAuthMeTestRequest(
		NewAuthMeHandler(),
		http.MethodGet,
		authMeTestToken(t, "999", "missing@example.com"),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthMeFailsClosedWhenAuthDBIsUnavailable(t *testing.T) {
	previousAuthDB := models.AuthDB
	models.AuthDB = nil
	t.Cleanup(func() {
		models.AuthDB = previousAuthDB
	})
	t.Setenv("JWT_SECRET", authMeTestJWTSecret)

	response := performAuthMeTestRequest(
		NewAuthMeHandler(),
		http.MethodGet,
		authMeTestToken(t, "1", "alice@example.com"),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthMeAllowsOnlyGetAndOptions(t *testing.T) {
	handler := NewAuthMeHandler()

	options := performAuthMeTestRequest(handler, http.MethodOptions, "")
	if options.Code != http.StatusOK {
		t.Fatalf("OPTIONS status=%d body=%s", options.Code, options.Body.String())
	}
	if got := options.Header().Get("Access-Control-Allow-Methods"); got != "GET, OPTIONS" {
		t.Fatalf("OPTIONS allow methods=%q", got)
	}

	post := performAuthMeTestRequest(handler, http.MethodPost, "")
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d body=%s", post.Code, post.Body.String())
	}
	if got := post.Header().Get("Allow"); got != "GET, OPTIONS" {
		t.Fatalf("POST Allow=%q", got)
	}
}
