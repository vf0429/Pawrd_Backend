package handlers

import (
	"bytes"
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

func setupPostsTestDB(t *testing.T) *gorm.DB {
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
		&models.Post{},
		&models.PostPetTag{},
		&models.PostImage{},
		&models.PostLike{},
		&models.PostComment{},
		&models.PostCollection{},
		&models.UserFollow{},
		&models.FamilyFollow{},
		&models.PostPoll{},
		&models.PostPollOption{},
		&models.PostPollVote{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedPostOwnershipFixtures(t *testing.T, db *gorm.DB) (string, string, string) {
	t.Helper()
	family := models.Family{
		ID:          "family-1",
		OwnerUserID: "user-1",
		DisplayName: "The Chan Family",
		Handle:      "chan-family",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := db.Create(&family).Error; err != nil {
		t.Fatalf("seed family: %v", err)
	}
	if err := db.Create(&models.Pet{
		ID:        "pet-1",
		FamilyID:  family.ID,
		Name:      "Mochi",
		Species:   "Cat",
		Breed:     "British Shorthair",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed family pet: %v", err)
	}
	if err := db.Create(&models.Pet{
		ID:        "pet-2",
		FamilyID:  "family-2",
		Name:      "Other",
		Species:   "Dog",
		Breed:     "Retriever",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed foreign pet: %v", err)
	}
	return family.OwnerUserID, family.ID, "pet-1"
}

func TestCreatePostPersistsFamilyAndPetTags(t *testing.T) {
	db := setupPostsTestDB(t)
	authorID, familyID, petID := seedPostOwnershipFixtures(t, db)

	body := map[string]any{
		"title":        "Tagged family walk",
		"content":      "A small family update",
		"visibility":   "public",
		"allowComment": true,
		"pet_ids":      []string{petID},
		"imageUrls":    []string{},
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/posts", bytes.NewReader(payload))
	req.Header.Set("X-User-Id", authorID)
	req.Header.Set("X-User-Name", "Vince")
	rec := httptest.NewRecorder()

	NewPostsHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var posts []models.Post
	if err := db.Find(&posts).Error; err != nil {
		t.Fatalf("query posts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	if posts[0].FamilyID != familyID {
		t.Fatalf("expected family id %s, got %s", familyID, posts[0].FamilyID)
	}

	var tags []models.PostPetTag
	if err := db.Find(&tags).Error; err != nil {
		t.Fatalf("query tags: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 post tag, got %d", len(tags))
	}
	if tags[0].PetID != petID || !tags[0].IsPrimary {
		t.Fatalf("unexpected tag payload: %+v", tags[0])
	}
}

func TestCreatePostRejectsCrossFamilyPetTags(t *testing.T) {
	db := setupPostsTestDB(t)
	authorID, _, _ := seedPostOwnershipFixtures(t, db)

	body := map[string]any{
		"title":   "Invalid tag",
		"content": "Should fail",
		"pet_ids": []string{"pet-2"},
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/posts", bytes.NewReader(payload))
	req.Header.Set("X-User-Id", authorID)
	req.Header.Set("X-User-Name", "Vince")
	rec := httptest.NewRecorder()

	NewPostsHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}

	var tags int64
	if err := db.Model(&models.PostPetTag{}).Count(&tags).Error; err != nil {
		t.Fatalf("count tags: %v", err)
	}
	if tags != 0 {
		t.Fatalf("expected no tags persisted, got %d", tags)
	}
}

func TestFollowingFeedUsesFollowedAuthorsOnly(t *testing.T) {
	db := setupPostsTestDB(t)

	families := []models.Family{
		{ID: "family-author", OwnerUserID: "author-1", DisplayName: "Author Family", Handle: "author-family"},
		{ID: "family-only", OwnerUserID: "author-2", DisplayName: "Family Only", Handle: "family-only"},
	}
	for _, family := range families {
		if err := db.Create(&family).Error; err != nil {
			t.Fatalf("seed family: %v", err)
		}
	}

	posts := []models.Post{
		{ID: "post-followed-author", AuthorID: "author-1", FamilyID: "family-author", Title: "Followed author"},
		{ID: "post-followed-family-only", AuthorID: "author-2", FamilyID: "family-only", Title: "Family follow only"},
		{ID: "post-unfollowed", AuthorID: "author-3", Title: "Unfollowed"},
	}
	for _, post := range posts {
		if err := db.Create(&post).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}

	if err := db.Create(&models.UserFollow{
		FollowerID: "viewer",
		FolloweeID: "author-1",
	}).Error; err != nil {
		t.Fatalf("seed user follow: %v", err)
	}
	if err := db.Create(&models.FamilyFollow{
		FamilyID:       "family-only",
		FollowerUserID: "viewer",
	}).Error; err != nil {
		t.Fatalf("seed family follow: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/posts?feed=following&limit=20", nil)
	req.Header.Set("X-User-Id", "viewer")
	rec := httptest.NewRecorder()
	NewPostsHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got []models.BlogPost
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].ID != "post-followed-author" {
		t.Fatalf("expected only followed author's post, got %+v", got)
	}
}
