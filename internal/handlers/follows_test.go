package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUserStatsIncludesAuthorFollowLikesAndSaves(t *testing.T) {
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Post{},
		&models.PostLike{},
		&models.PostCollection{},
		&models.UserFollow{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	posts := []models.Post{
		{ID: "post-1", AuthorID: "author-1", Content: "one"},
		{ID: "post-2", AuthorID: "author-1", Content: "two"},
		{ID: "post-other", AuthorID: "author-2", Content: "other"},
	}
	if err := db.Create(&posts).Error; err != nil {
		t.Fatalf("seed posts: %v", err)
	}
	if err := db.Create(&[]models.PostLike{
		{PostID: "post-1", UserID: "reader-1"},
		{PostID: "post-2", UserID: "reader-1"},
		{PostID: "post-other", UserID: "reader-1"},
	}).Error; err != nil {
		t.Fatalf("seed likes: %v", err)
	}
	if err := db.Create(&[]models.PostCollection{
		{PostID: "post-1", UserID: "reader-1"},
		{PostID: "post-2", UserID: "reader-2"},
		{PostID: "post-other", UserID: "reader-1"},
	}).Error; err != nil {
		t.Fatalf("seed saves: %v", err)
	}
	if err := db.Create(&[]models.UserFollow{
		{FollowerID: "reader-1", FolloweeID: "author-1"},
		{FollowerID: "author-1", FolloweeID: "author-2"},
	}).Error; err != nil {
		t.Fatalf("seed follows: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/users/author-1/stats", nil)
	request.SetPathValue("id", "author-1")
	recorder := httptest.NewRecorder()
	NewUserStatsHandler(db).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		PostCount      int `json:"postCount"`
		FollowerCount  int `json:"followerCount"`
		FollowingCount int `json:"followingCount"`
		LikeCount      int `json:"likeCount"`
		CollectCount   int `json:"collectCount"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response.PostCount != 2 ||
		response.FollowerCount != 1 ||
		response.FollowingCount != 1 ||
		response.LikeCount != 2 ||
		response.CollectCount != 2 {
		t.Fatalf("unexpected stats: %+v", response)
	}
}
