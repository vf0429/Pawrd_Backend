package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupFamilyDomainTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
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
	return db
}

func seedFamilyDomainFixtures(t *testing.T, db *gorm.DB) (string, string, string) {
	t.Helper()
	family := models.Family{
		ID:          "family-1",
		OwnerUserID: "user-1",
		DisplayName: "The Chan Family",
		Handle:      "chan-family",
		IsPublic:    true,
	}
	if err := db.Create(&family).Error; err != nil {
		t.Fatalf("seed family: %v", err)
	}
	member := models.FamilyMember{
		ID:          "member-1",
		FamilyID:    family.ID,
		UserID:      family.OwnerUserID,
		DisplayName: "Vince",
		Role:        "owner",
		IsPrimary:   true,
		JoinedAt:    time.Now().UTC(),
	}
	if err := db.Create(&member).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}

	birthDate := time.Date(2022, 4, 3, 0, 0, 0, 0, time.UTC)
	weight := 4.2
	nextVac := time.Now().UTC().Add(30 * 24 * time.Hour)
	pet := models.Pet{
		ID:                   "pet-1",
		FamilyID:             family.ID,
		Name:                 "Mochi",
		Species:              "Cat",
		Breed:                "British Shorthair",
		BirthDate:            &birthDate,
		CurrentWeightKg:      &weight,
		NextVaccinationDueAt: &nextVac,
	}
	if err := db.Create(&pet).Error; err != nil {
		t.Fatalf("seed pet: %v", err)
	}
	if err := db.Create(&models.PetPublicProfile{
		ID:          "pet-public-1",
		PetID:       pet.ID,
		DisplayName: "Mochi",
		Slug:        "chan-family-mochi",
		Headline:    "Window watcher",
	}).Error; err != nil {
		t.Fatalf("seed pet profile: %v", err)
	}
	if err := db.Create(&models.PetVisibilitySetting{
		ID:                "pet-visibility-1",
		PetID:             pet.ID,
		ShowBreed:         true,
		ShowAge:           true,
		ShowLatestWeight:  false,
		ShowVaccineStatus: false,
		ShowFamilyLink:    true,
	}).Error; err != nil {
		t.Fatalf("seed pet visibility: %v", err)
	}
	summary := models.BuildPetDerivedSummary(pet, time.Now().UTC())
	summary.ID = "pet-summary-1"
	if err := db.Create(&summary).Error; err != nil {
		t.Fatalf("seed pet summary: %v", err)
	}

	post := models.Post{
		ID:           "post-1",
		AuthorID:     family.OwnerUserID,
		FamilyID:     family.ID,
		AuthorName:   family.DisplayName,
		Title:        "A family day out",
		Content:      "Sunny walk with Mochi.",
		Visibility:   "public",
		AllowComment: true,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	if err := db.Create(&models.PostPetTag{
		ID:        "tag-1",
		PostID:    post.ID,
		PetID:     pet.ID,
		IsPrimary: true,
	}).Error; err != nil {
		t.Fatalf("seed post tag: %v", err)
	}

	return family.OwnerUserID, family.Handle, pet.ID
}

func TestFamilyProfileByOwnerHandler(t *testing.T) {
	db := setupFamilyDomainTestDB(t)
	ownerUserID, _, _ := seedFamilyDomainFixtures(t, db)

	req := httptest.NewRequest(http.MethodGet, "/api/domain/family-owner/"+ownerUserID, nil)
	req.SetPathValue("ownerUserID", ownerUserID)
	rec := httptest.NewRecorder()

	NewFamilyProfileByOwnerHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		OwnerUserID string `json:"owner_user_id"`
		Handle      string `json:"handle"`
		Pets        []struct {
			PublicSlug string `json:"public_slug"`
			Breed      string `json:"breed"`
		} `json:"pets"`
		Stats map[string]int64 `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.OwnerUserID != ownerUserID {
		t.Fatalf("expected owner_user_id %s, got %s", ownerUserID, payload.OwnerUserID)
	}
	if payload.Handle != "chan-family" {
		t.Fatalf("expected handle chan-family, got %s", payload.Handle)
	}
	if len(payload.Pets) != 1 || payload.Pets[0].PublicSlug != "chan-family-mochi" {
		t.Fatalf("expected seeded pet payload, got %+v", payload.Pets)
	}
	if payload.Stats["posts"] != 1 || payload.Stats["pets"] != 1 {
		t.Fatalf("expected stats to reflect seed data, got %+v", payload.Stats)
	}
}

func TestPostPetTagsHandlerFamilyFilter(t *testing.T) {
	db := setupFamilyDomainTestDB(t)
	_, handle, _ := seedFamilyDomainFixtures(t, db)

	var family models.Family
	if err := db.Where("handle = ?", handle).First(&family).Error; err != nil {
		t.Fatalf("load family: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/domain/posts?family_id="+family.ID, nil)
	rec := httptest.NewRecorder()

	NewPostPetTagsHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var posts []models.BlogPost
	if err := json.Unmarshal(rec.Body.Bytes(), &posts); err != nil {
		t.Fatalf("decode posts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	if posts[0].Title != "A family day out" {
		t.Fatalf("expected seeded title, got %s", posts[0].Title)
	}
}

func TestFamilyFollowersDetailHandler(t *testing.T) {
	db := setupFamilyDomainTestDB(t)
	_, handle, _ := seedFamilyDomainFixtures(t, db)

	if err := db.Create(&models.Post{
		ID:           "post-follower-1",
		AuthorID:     "user-2",
		AuthorName:   "Follower User",
		AuthorAvatar: "https://example.com/avatar.png",
		Title:        "Hello",
		Visibility:   "public",
		AllowComment: true,
	}).Error; err != nil {
		t.Fatalf("seed follower post: %v", err)
	}

	var family models.Family
	if err := db.Where("handle = ?", handle).First(&family).Error; err != nil {
		t.Fatalf("load family: %v", err)
	}

	if err := db.Create(&models.FamilyFollow{
		ID:             "family-follow-1",
		FamilyID:       family.ID,
		FollowerUserID: "user-2",
	}).Error; err != nil {
		t.Fatalf("seed family follow: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/domain/families/"+handle+"/followers-detail", nil)
	req.SetPathValue("idOrHandle", handle)
	rec := httptest.NewRecorder()

	NewFamilyFollowersDetailHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Users []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Users) != 1 || payload.Users[0].ID != "user-2" || payload.Users[0].Name != "Follower User" {
		t.Fatalf("unexpected followers payload: %+v", payload.Users)
	}
}
