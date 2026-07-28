package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupPollTestDB(t *testing.T) *gorm.DB {
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
		&models.PostPetTag{},
		&models.PostPoll{},
		&models.PostPollOption{},
		&models.PostPollVote{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// createPostWithPoll posts a poll-bearing post and returns the created BlogPost.
func createPostWithPoll(t *testing.T, db *gorm.DB, authorID string) models.BlogPost {
	t.Helper()
	body := map[string]any{
		"title":   "Best treat?",
		"content": "Vote below",
		"poll": map[string]any{
			"question": "What's your pet's favorite treat?",
			"options":  []string{"Chicken", "Cheese", "  "}, // blank option is dropped
		},
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/posts", bytes.NewReader(payload))
	req.Header.Set("X-User-Id", authorID)
	req.Header.Set("X-User-Name", "Author")
	rec := httptest.NewRecorder()

	NewPostsHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create post: expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var created models.BlogPost
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created post: %v", err)
	}
	return created
}

func votePoll(t *testing.T, db *gorm.DB, postID, userID, optionID string) models.BlogPoll {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"optionId": optionID})
	req := httptest.NewRequest(http.MethodPost, "/posts/"+postID+"/poll/vote", bytes.NewReader(payload))
	req.SetPathValue("id", postID)
	req.Header.Set("X-User-Id", userID)
	rec := httptest.NewRecorder()

	NewPostPollVoteHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("vote: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var poll models.BlogPoll
	if err := json.Unmarshal(rec.Body.Bytes(), &poll); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	return poll
}

func TestPollCreateVoteAndChangeVote(t *testing.T) {
	db := setupPollTestDB(t)
	created := createPostWithPoll(t, db, "author-1")

	// Poll persisted with the two non-blank options, no votes yet.
	if created.Poll == nil {
		t.Fatal("expected poll on created post")
	}
	if len(created.Poll.Options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(created.Poll.Options))
	}
	if created.Poll.TotalVotes != 0 || created.Poll.VotedOptionID != "" {
		t.Fatalf("fresh poll should have no votes: %+v", created.Poll)
	}

	chicken := created.Poll.Options[0].ID
	cheese := created.Poll.Options[1].ID

	// Two distinct users vote for Chicken.
	votePoll(t, db, created.ID, "voter-1", chicken)
	p := votePoll(t, db, created.ID, "voter-2", chicken)
	if p.TotalVotes != 2 {
		t.Fatalf("expected 2 total votes, got %d", p.TotalVotes)
	}
	if p.VotedOptionID != chicken {
		t.Fatalf("voter-2 should be on chicken, got %s", p.VotedOptionID)
	}

	// voter-2 changes their vote to Cheese — total stays 2, distribution shifts.
	p = votePoll(t, db, created.ID, "voter-2", cheese)
	if p.TotalVotes != 2 {
		t.Fatalf("changing a vote must not change the total, got %d", p.TotalVotes)
	}
	if p.VotedOptionID != cheese {
		t.Fatalf("voter-2 should now be on cheese, got %s", p.VotedOptionID)
	}
	counts := map[string]int{}
	for _, o := range p.Options {
		counts[o.ID] = o.Votes
	}
	if counts[chicken] != 1 || counts[cheese] != 1 {
		t.Fatalf("expected 1/1 split after change, got chicken=%d cheese=%d", counts[chicken], counts[cheese])
	}

	// Only one vote row per (poll, user) — voter-2 did not accumulate two rows.
	var voteRows int64
	db.Model(&models.PostPollVote{}).Where("user_id = ?", "voter-2").Count(&voteRows)
	if voteRows != 1 {
		t.Fatalf("expected exactly 1 vote row for voter-2, got %d", voteRows)
	}
}

func TestPollVoteRejectsForeignOption(t *testing.T) {
	db := setupPollTestDB(t)
	created := createPostWithPoll(t, db, "author-1")

	payload, _ := json.Marshal(map[string]string{"optionId": "not-a-real-option"})
	req := httptest.NewRequest(http.MethodPost, "/posts/"+created.ID+"/poll/vote", bytes.NewReader(payload))
	req.SetPathValue("id", created.ID)
	req.Header.Set("X-User-Id", "voter-1")
	rec := httptest.NewRecorder()

	NewPostPollVoteHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for foreign option, got %d body=%s", rec.Code, rec.Body.String())
	}
}
