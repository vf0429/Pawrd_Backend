package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAuthRegisterTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatalf("open auth register test db: %v", err)
	}
	if err := db.AutoMigrate(&models.AuthUser{}, &models.Family{}, &models.FamilyMember{}); err != nil {
		t.Fatalf("migrate auth register test db: %v", err)
	}

	previousAuthDB := models.AuthDB
	models.AuthDB = db
	t.Cleanup(func() {
		models.AuthDB = previousAuthDB
	})
	t.Setenv("JWT_SECRET", "test-only-jwt-secret-at-least-32-characters")
	return db
}

func registerAccount(t *testing.T, handler http.Handler, name, email string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(RegisterRequest{
		Name:     name,
		Email:    email,
		Password: "secure-test-password",
	})
	if err != nil {
		t.Fatalf("encode registration request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestAuthRegisterCreatesUserAndFamily(t *testing.T) {
	db := setupAuthRegisterTestDB(t)
	response := registerAccount(t, NewAuthRegisterHandler(db), "  Jasper  ", "  JASPER@example.com ")

	if response.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201; body=%s", response.Code, response.Body.String())
	}

	var payload models.AuthTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode registration response: %v", err)
	}
	if payload.Token == "" {
		t.Fatal("registration response token is empty")
	}
	if payload.User.Email != "jasper@example.com" {
		t.Fatalf("response email = %q, want normalized email", payload.User.Email)
	}
	if payload.User.Name != "Jasper" {
		t.Fatalf("response name = %q, want trimmed name", payload.User.Name)
	}

	var user models.AuthUser
	if err := db.Where("email = ?", "jasper@example.com").First(&user).Error; err != nil {
		t.Fatalf("find registered user: %v", err)
	}
	if !strings.HasPrefix(user.Phone, "phone-not-set-") {
		t.Fatalf("registered phone placeholder = %q", user.Phone)
	}
	if user.PasswordHash == "" || user.PasswordHash == "secure-test-password" {
		t.Fatal("registered password was not hashed")
	}

	var family models.Family
	if err := db.Where("owner_user_id = ?", payload.User.ID).First(&family).Error; err != nil {
		t.Fatalf("find registered family: %v", err)
	}
	var member models.FamilyMember
	if err := db.Where("family_id = ? AND user_id = ?", family.ID, payload.User.ID).First(&member).Error; err != nil {
		t.Fatalf("find registered family member: %v", err)
	}
	if member.Role != "owner" || !member.IsPrimary {
		t.Fatalf("registered family member = %+v, want primary owner", member)
	}
}

func TestAuthRegisterRejectsDuplicateNormalizedEmail(t *testing.T) {
	db := setupAuthRegisterTestDB(t)
	handler := NewAuthRegisterHandler(db)

	first := registerAccount(t, handler, "First", "duplicate@example.com")
	if first.Code != http.StatusCreated {
		t.Fatalf("first register status = %d, want 201; body=%s", first.Code, first.Body.String())
	}

	second := registerAccount(t, handler, "Second", " DUPLICATE@EXAMPLE.COM ")
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want 409; body=%s", second.Code, second.Body.String())
	}

	var payload map[string]string
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}
	if payload["error"] != "Email already registered" {
		t.Fatalf("duplicate error = %q", payload["error"])
	}

	var count int64
	if err := db.Model(&models.AuthUser{}).
		Where("email = ?", "duplicate@example.com").
		Count(&count).Error; err != nil {
		t.Fatalf("count duplicate users: %v", err)
	}
	if count != 1 {
		t.Fatalf("duplicate email user count = %d, want 1", count)
	}
}
