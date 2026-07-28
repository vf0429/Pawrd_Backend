package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupExploreTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Family{},
		&models.Post{},
		&models.PostImage{},
		&models.PostLike{},
		&models.PostComment{},
		&models.PostCollection{},
		&models.PostView{},
		&models.PostPoll{},
		&models.PostPollOption{},
		&models.PostPollVote{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedExplorePosts(t *testing.T, db *gorm.DB, n int) []string {
	t.Helper()
	ids := make([]string, n)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("post-%d", i)
		ids[i] = id
		post := models.Post{
			ID:        id,
			AuthorID:  "author",
			Content:   fmt.Sprintf("content %d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&post).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}
	return ids
}

func getExplore(t *testing.T, db *gorm.DB, userID, cursor string, limit int) ([]models.BlogPost, string, bool) {
	t.Helper()
	url := fmt.Sprintf("/posts?limit=%d", limit)
	if cursor != "" {
		url += "&cursor=" + cursor
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	rec := httptest.NewRecorder()
	NewPostsHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("explore: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var posts []models.BlogPost
	if err := json.Unmarshal(rec.Body.Bytes(), &posts); err != nil {
		t.Fatalf("decode: %v", err)
	}
	hasMore := rec.Header().Get("X-Has-More") == "true"
	return posts, rec.Header().Get("X-Next-Cursor"), hasMore
}

func TestExploreFeedSurfacesUnseenFirst(t *testing.T) {
	db := setupExploreTestDB(t)
	ids := seedExplorePosts(t, db, 6)

	// viewer has already opened the first three posts.
	seen := map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}
	for id := range seen {
		if err := db.Create(&models.PostView{UserID: "viewer", PostID: id}).Error; err != nil {
			t.Fatalf("seed view: %v", err)
		}
	}

	posts, _, _ := getExplore(t, db, "viewer", "", 10)
	if len(posts) != 6 {
		t.Fatalf("expected 6 posts, got %d", len(posts))
	}

	// Every unseen post must appear before every seen post.
	sawSeen := false
	for _, p := range posts {
		if seen[p.ID] {
			sawSeen = true
		} else if sawSeen {
			t.Fatalf("unseen post %s appeared after a seen post — unseen-first violated; order=%v",
				p.ID, idsOf(posts))
		}
	}
}

func TestExploreFeedPaginationCoversAllWithoutOverlap(t *testing.T) {
	db := setupExploreTestDB(t)
	seedExplorePosts(t, db, 5)

	collected := map[string]bool{}
	cursor := ""
	for page := 0; page < 10; page++ {
		posts, next, hasMore := getExplore(t, db, "viewer", cursor, 2)
		for _, p := range posts {
			if collected[p.ID] {
				t.Fatalf("post %s returned twice across pages", p.ID)
			}
			collected[p.ID] = true
		}
		if !hasMore {
			break
		}
		if next == "" {
			t.Fatal("hasMore was true but no cursor returned")
		}
		cursor = next
	}

	if len(collected) != 5 {
		t.Fatalf("expected all 5 posts across pages, got %d", len(collected))
	}
}

func idsOf(posts []models.BlogPost) []string {
	out := make([]string, len(posts))
	for i, p := range posts {
		out[i] = p.ID
	}
	return out
}
