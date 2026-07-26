package handlers

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// toBlogPost converts a fully-loaded models.Post (with Images/Likes/Comments preloaded)
// into the iOS-facing BlogPost shape, including the per-viewer `IsLiked` flag.
// `requesterID` may be empty for anonymous viewers (then `IsLiked` is always false).
func toBlogPost(db *gorm.DB, p models.Post, requesterID string) models.BlogPost {
	imageUrls := make([]string, 0, len(p.Images))
	imageMeta := make([]models.BlogImageMeta, 0, len(p.Images))
	for _, img := range p.Images {
		imageUrls = append(imageUrls, img.ImageURL)
		imageMeta = append(imageMeta, models.BlogImageMeta{
			URL:          img.ImageURL,
			ThumbnailURL: img.ThumbnailURL,
			Width:        img.Width,
			Height:       img.Height,
		})
	}
	isLiked := false
	if requesterID != "" {
		for _, like := range p.Likes {
			if like.UserID == requesterID {
				isLiked = true
				break
			}
		}
	}
	isCollected := false
	if requesterID != "" {
		for _, collect := range p.Collections {
			if collect.UserID == requesterID {
				isCollected = true
				break
			}
		}
	}
	familyHandle := ""
	familyName := ""
	if db != nil && strings.TrimSpace(p.FamilyID) != "" {
		var family models.Family
		if err := db.Select("id", "handle", "display_name").First(&family, "id = ?", p.FamilyID).Error; err == nil {
			familyHandle = family.Handle
			familyName = family.DisplayName
		}
	}
	return models.BlogPost{
		ID:           p.ID,
		AuthorID:     p.AuthorID,
		FamilyID:     p.FamilyID,
		FamilyHandle: familyHandle,
		FamilyName:   familyName,
		AuthorName:   p.AuthorName,
		AuthorAvatar: p.AuthorAvatar,
		Title:        p.Title,
		Content:      p.Content,
		ImageColor:   p.ImageColor,
		Location:     p.Location,
		Visibility:   p.Visibility,
		AllowComment: p.AllowComment,
		Likes:        len(p.Likes),
		CollectCount: len(p.Collections),
		Comments:     len(p.Comments),
		ViewCount:    p.Views,
		Timestamp:    p.CreatedAt,
		ImageUrls:    imageUrls,
		ImageMeta:    imageMeta,
		IsLiked:      isLiked,
		IsCollected:  isCollected,
		Poll:         loadBlogPoll(db, p.ID, requesterID),
	}
}

// loadBlogPoll returns the poll attached to a post (or nil when there is none),
// with aggregated per-option vote counts and the requester's current choice.
func loadBlogPoll(db *gorm.DB, postID, requesterID string) *models.BlogPoll {
	if db == nil {
		return nil
	}
	var poll models.PostPoll
	if err := db.
		Preload("Options", func(d *gorm.DB) *gorm.DB { return d.Order("sort_order ASC") }).
		First(&poll, "post_id = ?", postID).Error; err != nil {
		return nil
	}

	type voteCount struct {
		OptionID string
		C        int
	}
	var counts []voteCount
	db.Model(&models.PostPollVote{}).
		Select("option_id, count(*) as c").
		Where("poll_id = ?", poll.ID).
		Group("option_id").
		Scan(&counts)

	countByOption := make(map[string]int, len(counts))
	total := 0
	for _, c := range counts {
		countByOption[c.OptionID] = c.C
		total += c.C
	}

	votedOptionID := ""
	if strings.TrimSpace(requesterID) != "" {
		var v models.PostPollVote
		if err := db.Select("option_id").
			Where("poll_id = ? AND user_id = ?", poll.ID, requesterID).
			First(&v).Error; err == nil {
			votedOptionID = v.OptionID
		}
	}

	options := make([]models.BlogPollOption, 0, len(poll.Options))
	for _, o := range poll.Options {
		options = append(options, models.BlogPollOption{
			ID:    o.ID,
			Text:  o.Text,
			Votes: countByOption[o.ID],
		})
	}

	return &models.BlogPoll{
		ID:            poll.ID,
		Question:      poll.Question,
		TotalVotes:    total,
		VotedOptionID: votedOptionID,
		Options:       options,
	}
}

// serveExploreFeed renders the Discover feed: every refresh reshuffles the order
// (seeded so pagination within one refresh stays consistent), and posts the
// viewer hasn't opened are surfaced ahead of ones they have. The pagination
// cursor is the opaque string "seed:offset"; an absent cursor (a fresh refresh)
// draws a new seed, so the next refresh reshuffles and brings up unseen posts.
func serveExploreFeed(w http.ResponseWriter, db *gorm.DB, requesterID string, limit int, cursorStr string) {
	if limit <= 0 {
		limit = 20
	}

	var seed int64
	offset := 0
	if cursorStr != "" {
		parts := strings.SplitN(cursorStr, ":", 2)
		if len(parts) == 2 {
			seed, _ = strconv.ParseInt(parts[0], 10, 64)
			offset, _ = strconv.Atoi(parts[1])
		}
	}
	if seed == 0 {
		seed = rand.Int63()
		if seed == 0 {
			seed = 1
		}
	}
	if offset < 0 {
		offset = 0
	}

	// All candidate post IDs (created_at gives stable tie-breaking before shuffle).
	var ids []string
	if err := db.Model(&models.Post{}).Order("created_at DESC").Pluck("id", &ids).Error; err != nil {
		http.Error(w, "failed to list posts: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Which posts this viewer has already opened.
	seen := make(map[string]bool)
	if requesterID != "" {
		var seenIDs []string
		db.Model(&models.PostView{}).Where("user_id = ?", requesterID).Pluck("post_id", &seenIDs)
		for _, id := range seenIDs {
			seen[id] = true
		}
	}

	// Unseen first; shuffle within each group by a seed-stable hash.
	sort.SliceStable(ids, func(i, j int) bool {
		si, sj := seen[ids[i]], seen[ids[j]]
		if si != sj {
			return !si // unseen (false) sorts before seen (true)
		}
		return shuffleKey(ids[i], seed) < shuffleKey(ids[j], seed)
	})

	total := len(ids)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	pageIDs := ids[start:end]
	hasMore := end < total

	result := make([]models.BlogPost, 0, len(pageIDs))
	if len(pageIDs) > 0 {
		var pagePosts []models.Post
		if err := db.
			Preload("Images").
			Preload("Likes").
			Preload("Comments").
			Preload("Collections").
			Where("id IN ?", pageIDs).
			Find(&pagePosts).Error; err != nil {
			http.Error(w, "failed to load posts: "+err.Error(), http.StatusInternalServerError)
			return
		}
		byID := make(map[string]models.Post, len(pagePosts))
		for _, p := range pagePosts {
			byID[p.ID] = p
		}
		// Emit in the shuffled order, not the DB's IN() order.
		for _, id := range pageIDs {
			if p, ok := byID[id]; ok {
				result = append(result, toBlogPost(db, p, requesterID))
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
	if hasMore {
		w.Header().Set("X-Next-Cursor", fmt.Sprintf("%d:%d", seed, end))
	}
	_ = json.NewEncoder(w).Encode(result)
}

// shuffleKey is a deterministic per-(post, seed) sort key, so a given seed always
// produces the same shuffle but a new seed reshuffles.
func shuffleKey(id string, seed int64) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(seed))
	_, _ = h.Write(b[:])
	return h.Sum64()
}

// NewPostHotKeywordsHandler returns suggested search keywords derived from recent popular posts.
func NewPostHotKeywordsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		type row struct {
			Title string
		}
		var rows []row
		db.Model(&models.Post{}).
			Select("title").
			Where("title != ''").
			Order("created_at DESC").
			Limit(50).
			Find(&rows)

		seen := make(map[string]bool)
		keywords := make([]string, 0, 10)
		for _, r := range rows {
			t := strings.TrimSpace(r.Title)
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			runes := []rune(t)
			if len(runes) > 18 {
				t = string(runes[:18])
			}
			keywords = append(keywords, t)
			if len(keywords) >= 10 {
				break
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(keywords)
	}
}

// NewPostsHandler returns an http.HandlerFunc for GET/POST /posts backed by SQLite.
// GET  /posts  — returns all posts as []BlogPost (iOS-compatible format)
// POST /posts  — creates a post, persists to SQLite, returns the created BlogPost
// Author identity is read from X-User-Id / X-User-Name / X-User-Avatar request headers.
func NewPostsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}

		switch r.Method {
		case http.MethodGet:
			limitStr := strings.TrimSpace(r.URL.Query().Get("limit"))
			cursorStr := strings.TrimSpace(r.URL.Query().Get("cursor"))
			feedType := strings.TrimSpace(r.URL.Query().Get("feed"))
			usePaging := limitStr != "" || cursorStr != ""

			limit := 0
			if usePaging {
				limit = 20
				if limitStr != "" {
					parsed, err := strconv.Atoi(limitStr)
					if err != nil || parsed <= 0 || parsed > 50 {
						http.Error(w, "invalid limit", http.StatusBadRequest)
						return
					}
					limit = parsed
				}
			}

			query := db.
				Preload("Images").
				Preload("Likes").
				Preload("Comments").
				Preload("Collections").
				Order("created_at DESC")

			requesterID := strings.TrimSpace(r.Header.Get("X-User-Id"))

			// Discover feed: shuffled per refresh, with posts the viewer hasn't
			// opened pushed to the front. Handled separately from the time-ordered
			// feeds below.
			if feedType == "" {
				serveExploreFeed(w, db, requesterID, limit, cursorStr)
				return
			}

			if feedType == "following" {
				followerID := strings.TrimSpace(r.URL.Query().Get("userId"))
				if followerID == "" {
					followerID = requesterID
				}
				if followerID == "" {
					http.Error(w, "missing user id", http.StatusUnauthorized)
					return
				}
				if requesterID == "" {
					requesterID = followerID
				}

				var followedAuthorIDs []string
				if err := db.Model(&models.UserFollow{}).
					Where("follower_id = ?", followerID).
					Pluck("followee_id", &followedAuthorIDs).Error; err != nil {
					http.Error(w, "Failed to fetch followed authors: "+err.Error(), http.StatusInternalServerError)
					return
				}

				if len(followedAuthorIDs) == 0 {
					w.Header().Set("Content-Type", "application/json")
					if usePaging {
						w.Header().Set("X-Has-More", "false")
					}
					json.NewEncoder(w).Encode([]models.BlogPost{})
					return
				}

				query = query.Where("author_id IN ?", followedAuthorIDs)
			} else if feedType == "my_posts" {
				if requesterID == "" {
					http.Error(w, "missing user id", http.StatusUnauthorized)
					return
				}
				query = query.Where("author_id = ?", requesterID)
			} else if feedType == "liked" {
				if requesterID == "" {
					http.Error(w, "missing user id", http.StatusUnauthorized)
					return
				}
				var likedPostIDs []string
				if err := db.Model(&models.PostLike{}).
					Where("user_id = ?", requesterID).
					Pluck("post_id", &likedPostIDs).Error; err != nil {
					http.Error(w, "Failed to fetch liked posts: "+err.Error(), http.StatusInternalServerError)
					return
				}
				if len(likedPostIDs) == 0 {
					w.Header().Set("Content-Type", "application/json")
					if usePaging {
						w.Header().Set("X-Has-More", "false")
					}
					json.NewEncoder(w).Encode([]models.BlogPost{})
					return
				}
				query = query.Where("id IN ?", likedPostIDs)
			} else if feedType == "collected" {
				if requesterID == "" {
					http.Error(w, "missing user id", http.StatusUnauthorized)
					return
				}
				var collectedPostIDs []string
				if err := db.Model(&models.PostCollection{}).
					Where("user_id = ?", requesterID).
					Pluck("post_id", &collectedPostIDs).Error; err != nil {
					http.Error(w, "Failed to fetch collected posts: "+err.Error(), http.StatusInternalServerError)
					return
				}
				if len(collectedPostIDs) == 0 {
					w.Header().Set("Content-Type", "application/json")
					if usePaging {
						w.Header().Set("X-Has-More", "false")
					}
					json.NewEncoder(w).Encode([]models.BlogPost{})
					return
				}
				query = query.Where("id IN ?", collectedPostIDs)
			}

			if cursorStr != "" {
				cursor, err := time.Parse(time.RFC3339, cursorStr)
				if err != nil {
					http.Error(w, "invalid cursor", http.StatusBadRequest)
					return
				}
				query = query.Where("created_at < ?", cursor)
			}

			if usePaging && limit > 0 {
				query = query.Limit(limit + 1)
			}

			var posts []models.Post
			if err := query.Find(&posts).Error; err != nil {
				http.Error(w, "Failed to fetch posts: "+err.Error(), http.StatusInternalServerError)
				return
			}

			hasMore := false
			nextCursor := ""
			if usePaging && limit > 0 && len(posts) > limit {
				hasMore = true
				posts = posts[:limit]
			}

			if usePaging && len(posts) > 0 {
				nextCursor = posts[len(posts)-1].CreatedAt.Format(time.RFC3339)
			}

			result := make([]models.BlogPost, 0, len(posts))
			for _, p := range posts {
				result = append(result, toBlogPost(db, p, requesterID))
			}
			w.Header().Set("Content-Type", "application/json")
			if usePaging {
				w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
				if nextCursor != "" {
					w.Header().Set("X-Next-Cursor", nextCursor)
				}
			}
			json.NewEncoder(w).Encode(result)

		case http.MethodPost:
			var body struct {
				Title        string   `json:"title"`
				Content      string   `json:"content"`
				ImageColor   string   `json:"imageColor"`
				Location     string   `json:"location"`
				Visibility   string   `json:"visibility"`
				AllowComment *bool    `json:"allowComment"`
				PetIDs       []string `json:"pet_ids"`
				ImageUrls    []string `json:"imageUrls"`
				ImageMeta    []struct {
					URL          string `json:"url"`
					ThumbnailURL string `json:"thumbnailUrl"`
					Width        int    `json:"width"`
					Height       int    `json:"height"`
				} `json:"imageMeta"`
				Poll *struct {
					Question string   `json:"question"`
					Options  []string `json:"options"`
				} `json:"poll"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "Invalid request body", http.StatusBadRequest)
				return
			}
			if body.Content == "" && body.Title == "" {
				http.Error(w, "title or content required", http.StatusBadRequest)
				return
			}

			// Author identity comes from headers, NOT body
			authorID := r.Header.Get("X-User-Id")
			authorName := r.Header.Get("X-User-Name")
			authorAvatar := r.Header.Get("X-User-Avatar")
			if authorName == "" {
				authorName = "Anonymous"
			}
			if authorAvatar == "" {
				authorAvatar = "person.circle.fill"
			}
			if authorID == "" {
				authorID = authorName
			}

			var familyID string
			if authorID != "" {
				var family models.Family
				if err := db.Select("id").Where("owner_user_id = ?", authorID).First(&family).Error; err == nil {
					familyID = family.ID
				}
			}

			normalizedPetIDs := make([]string, 0, len(body.PetIDs))
			seenPetIDs := make(map[string]struct{}, len(body.PetIDs))
			for _, rawPetID := range body.PetIDs {
				petID := strings.TrimSpace(rawPetID)
				if petID == "" {
					continue
				}
				if _, exists := seenPetIDs[petID]; exists {
					continue
				}
				seenPetIDs[petID] = struct{}{}
				normalizedPetIDs = append(normalizedPetIDs, petID)
			}
			if len(normalizedPetIDs) > 0 && familyID == "" {
				http.Error(w, "family-owned posts required for pet tags", http.StatusBadRequest)
				return
			}

			if len(normalizedPetIDs) > 0 {
				var ownedPets []models.Pet
				if err := db.Select("id", "family_id").Where("id IN ? AND family_id = ?", normalizedPetIDs, familyID).Find(&ownedPets).Error; err != nil {
					http.Error(w, "failed to validate pet tags: "+err.Error(), http.StatusInternalServerError)
					return
				}
				if len(ownedPets) != len(normalizedPetIDs) {
					http.Error(w, "pet tags must belong to the author's family", http.StatusBadRequest)
					return
				}
			}
			imageColor := body.ImageColor
			if imageColor == "" {
				imageColor = "blue"
			}

			visibility := body.Visibility
			if visibility == "" {
				visibility = "public"
			}
			allowComment := true
			if body.AllowComment != nil {
				allowComment = *body.AllowComment
			}

			post := models.Post{
				AuthorID:     authorID,
				FamilyID:     familyID,
				AuthorName:   authorName,
				AuthorAvatar: authorAvatar,
				Title:        body.Title,
				Content:      body.Content,
				ImageColor:   imageColor,
				Location:     body.Location,
				Visibility:   visibility,
				AllowComment: allowComment,
			}
			if err := db.Create(&post).Error; err != nil {
				http.Error(w, "Failed to save post: "+err.Error(), http.StatusInternalServerError)
				return
			}

			// Save image URLs as PostImage records
			if len(body.ImageMeta) > 0 {
				for i, meta := range body.ImageMeta {
					if meta.URL == "" {
						continue
					}
					img := models.PostImage{
						PostID:       post.ID,
						ImageURL:     meta.URL,
						ThumbnailURL: meta.ThumbnailURL,
						Width:        meta.Width,
						Height:       meta.Height,
						SortOrder:    i,
					}
					db.Create(&img)
				}
			} else {
				for i, url := range body.ImageUrls {
					img := models.PostImage{
						PostID:    post.ID,
						ImageURL:  url,
						SortOrder: i,
					}
					db.Create(&img)
				}
			}

			for idx, petID := range normalizedPetIDs {
				tag := models.PostPetTag{
					PostID:    post.ID,
					PetID:     petID,
					IsPrimary: idx == 0,
				}
				if err := db.Create(&tag).Error; err != nil {
					http.Error(w, "Failed to save pet tags: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}

			// Persist an optional poll (question + at least two non-empty options).
			if body.Poll != nil {
				question := strings.TrimSpace(body.Poll.Question)
				options := make([]string, 0, len(body.Poll.Options))
				for _, opt := range body.Poll.Options {
					if t := strings.TrimSpace(opt); t != "" {
						options = append(options, t)
					}
				}
				if question != "" && len(options) >= 2 {
					poll := models.PostPoll{PostID: post.ID, Question: question}
					if err := db.Create(&poll).Error; err == nil {
						for i, t := range options {
							db.Create(&models.PostPollOption{PollID: poll.ID, Text: t, SortOrder: i})
						}
					}
				}
			}

			response := models.BlogPost{
				ID:           post.ID,
				AuthorID:     post.AuthorID,
				FamilyID:     post.FamilyID,
				AuthorName:   post.AuthorName,
				AuthorAvatar: post.AuthorAvatar,
				Title:        post.Title,
				Content:      post.Content,
				ImageColor:   post.ImageColor,
				Location:     post.Location,
				Visibility:   post.Visibility,
				AllowComment: post.AllowComment,
				Likes:        0,
				Comments:     0,
				Timestamp:    post.CreatedAt,
				ImageUrls:    body.ImageUrls,
			}
			if len(body.ImageMeta) > 0 {
				meta := make([]models.BlogImageMeta, 0, len(body.ImageMeta))
				for _, item := range body.ImageMeta {
					if item.URL == "" {
						continue
					}
					meta = append(meta, models.BlogImageMeta{
						URL:          item.URL,
						ThumbnailURL: item.ThumbnailURL,
						Width:        item.Width,
						Height:       item.Height,
					})
				}
				response.ImageMeta = meta
			}
			response.Poll = loadBlogPoll(db, post.ID, authorID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(response)

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// NewPostSearchHandler returns an http.HandlerFunc for GET /posts/search?q=xxx&limit=20&cursor=...
func NewPostSearchHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Has-More", "false")
			json.NewEncoder(w).Encode([]models.BlogPost{})
			return
		}

		limit := 20
		if l := strings.TrimSpace(r.URL.Query().Get("limit")); l != "" {
			if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 50 {
				limit = parsed
			}
		}

		pattern := "%" + q + "%"
		query := db.
			Preload("Images").
			Preload("Likes").
			Preload("Comments").
			Preload("Collections").
			Where("title ILIKE ? OR content ILIKE ? OR author_name ILIKE ?", pattern, pattern, pattern).
			Order("created_at DESC")

		if cursorStr := strings.TrimSpace(r.URL.Query().Get("cursor")); cursorStr != "" {
			cursor, err := time.Parse(time.RFC3339, cursorStr)
			if err != nil {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
			query = query.Where("created_at < ?", cursor)
		}

		query = query.Limit(limit + 1)

		var posts []models.Post
		if err := query.Find(&posts).Error; err != nil {
			http.Error(w, "search failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		hasMore := len(posts) > limit
		if hasMore {
			posts = posts[:limit]
		}

		requesterID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		result := make([]models.BlogPost, 0, len(posts))
		for _, p := range posts {
			result = append(result, toBlogPost(db, p, requesterID))
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
		if len(posts) > 0 {
			w.Header().Set("X-Next-Cursor", posts[len(posts)-1].CreatedAt.Format(time.RFC3339))
		}
		json.NewEncoder(w).Encode(result)
	}
}

// NewPostDetailHandler returns the handler for /posts/{id}.
//
//	GET    -> single post with images/likes/comments and per-viewer `isLiked`
//	DELETE -> author-only delete (cascades to images/likes/comments via FK constraints)
func NewPostDetailHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}

		postID := strings.TrimSpace(r.PathValue("id"))
		if postID == "" {
			http.Error(w, "post id required", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			var post models.Post
			if err := db.
				Preload("Images").
				Preload("Likes").
				Preload("Comments").
				Preload("Collections").
				First(&post, "id = ?", postID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					http.Error(w, "post not found", http.StatusNotFound)
					return
				}
				http.Error(w, "failed to load post: "+err.Error(), http.StatusInternalServerError)
				return
			}

			requesterID := strings.TrimSpace(r.Header.Get("X-User-Id"))

			// Count a view for anyone other than the author. Atomic UpDate so
			// concurrent reads don't lose increments; reflect the new value in
			// the response without a re-read.
			if requesterID != "" && requesterID != post.AuthorID {
				if err := db.Model(&models.Post{}).
					Where("id = ?", postID).
					UpdateColumn("views", gorm.Expr("views + 1")).Error; err == nil {
					post.Views++
				}
			}

			// Remember that this viewer has now seen the post, so the Discover
			// feed can push posts they haven't opened to the front.
			if requesterID != "" {
				view := models.PostView{UserID: requesterID, PostID: postID}
				db.Where("user_id = ? AND post_id = ?", requesterID, postID).
					FirstOrCreate(&view)
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(toBlogPost(db, post, requesterID))

		case http.MethodDelete:
			requesterID := strings.TrimSpace(r.Header.Get("X-User-Id"))
			if requesterID == "" {
				http.Error(w, "missing X-User-Id", http.StatusUnauthorized)
				return
			}

			var post models.Post
			if err := db.Select("id", "author_id").First(&post, "id = ?", postID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					http.Error(w, "post not found", http.StatusNotFound)
					return
				}
				http.Error(w, "failed to load post: "+err.Error(), http.StatusInternalServerError)
				return
			}

			if post.AuthorID != requesterID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			// Explicit cascade in a transaction so we don't rely on the SQLite FK pragma being on.
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Where("post_id = ?", postID).Delete(&models.PostImage{}).Error; err != nil {
					return err
				}
				if err := tx.Where("post_id = ?", postID).Delete(&models.PostLike{}).Error; err != nil {
					return err
				}
				if err := tx.Where("post_id = ?", postID).Delete(&models.PostComment{}).Error; err != nil {
					return err
				}
				// Tear down the poll (votes → options → poll) before the post row.
				var poll models.PostPoll
				if err := tx.Where("post_id = ?", postID).First(&poll).Error; err == nil {
					if err := tx.Where("poll_id = ?", poll.ID).Delete(&models.PostPollVote{}).Error; err != nil {
						return err
					}
					if err := tx.Where("poll_id = ?", poll.ID).Delete(&models.PostPollOption{}).Error; err != nil {
						return err
					}
					if err := tx.Delete(&poll).Error; err != nil {
						return err
					}
				}
				return tx.Delete(&post).Error
			})
			if err != nil {
				http.Error(w, "failed to delete post: "+err.Error(), http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
