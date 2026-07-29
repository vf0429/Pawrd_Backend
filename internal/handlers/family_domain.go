package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

type petSummaryPayload struct {
	ID                string     `json:"id"`
	PublicSlug        string     `json:"public_slug"`
	DisplayName       string     `json:"display_name"`
	Species           string     `json:"species"`
	Breed             string     `json:"breed,omitempty"`
	Headline          string     `json:"headline,omitempty"`
	AvatarURL         string     `json:"avatar_url,omitempty"`
	DisplayAge        string     `json:"display_age,omitempty"`
	LatestWeightKg    *float64   `json:"latest_weight_kg,omitempty"`
	VaccineStatus     string     `json:"vaccine_status,omitempty"`
	LastVaccinationAt *time.Time `json:"last_vaccination_at,omitempty"`
	PostCount         int64      `json:"post_count"`
}

type familyProfilePayload struct {
	ID          string              `json:"id"`
	Handle      string              `json:"handle"`
	DisplayName string              `json:"display_name"`
	AvatarURL   string              `json:"avatar_url,omitempty"`
	Bio         string              `json:"bio,omitempty"`
	City        string              `json:"city,omitempty"`
	OwnerUserID string              `json:"owner_user_id"`
	MemberCount int                 `json:"member_count"`
	Pets        []petSummaryPayload `json:"pets"`
	Stats       map[string]int64    `json:"stats"`
	RecentPosts []models.BlogPost   `json:"recent_posts"`
}

func loadFamilyProfile(db *gorm.DB, idOrHandle string, viewerUserID string) (familyProfilePayload, int, error) {
	var family models.Family
	if err := db.Preload("Members").Preload("Pets.PublicProfile").Preload("Pets.VisibilitySettings").Preload("Pets.DerivedSummary").
		Where("id = ? OR handle = ?", idOrHandle, idOrHandle).
		First(&family).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return familyProfilePayload{}, http.StatusNotFound, err
		}
		return familyProfilePayload{}, http.StatusInternalServerError, err
	}

	var posts []models.Post
	if err := db.Preload("Images").Preload("Likes").Preload("Comments").Preload("Collections").Preload("PetTags").
		Where("family_id = ? AND visibility = ?", family.ID, "public").
		Order("created_at DESC").Limit(10).
		Find(&posts).Error; err != nil {
		return familyProfilePayload{}, http.StatusInternalServerError, err
	}

	pets := make([]petSummaryPayload, 0, len(family.Pets))
	for _, pet := range family.Pets {
		if pet.PublicProfile.PetID == "" {
			continue
		}
		var tagCount int64
		_ = db.Model(&models.PostPetTag{}).Where("pet_id = ?", pet.ID).Count(&tagCount).Error
		item := petSummaryPayload{
			ID:                pet.ID,
			PublicSlug:        pet.PublicProfile.Slug,
			DisplayName:       pet.PublicProfile.DisplayName,
			Species:           pet.Species,
			Headline:          pet.PublicProfile.Headline,
			AvatarURL:         pet.PublicProfile.AvatarURL,
			DisplayAge:        pet.DerivedSummary.DisplayAge,
			LatestWeightKg:    pickWeight(pet.VisibilitySettings.ShowLatestWeight, pet.DerivedSummary.LatestWeightKg),
			VaccineStatus:     pickString(pet.VisibilitySettings.ShowVaccineStatus, pet.DerivedSummary.VaccineStatus),
			LastVaccinationAt: pickTime(pet.VisibilitySettings.ShowVaccineStatus, pet.DerivedSummary.LastVaccinationAt),
			PostCount:         tagCount,
		}
		if pet.VisibilitySettings.ShowBreed {
			item.Breed = pet.Breed
		}
		pets = append(pets, item)
	}

	var followerCount int64
	var postCount int64
	_ = db.Model(&models.FamilyFollow{}).Where("family_id = ?", family.ID).Count(&followerCount).Error
	_ = db.Model(&models.Post{}).Where("family_id = ?", family.ID).Count(&postCount).Error

	recent := make([]models.BlogPost, 0, len(posts))
	for _, post := range posts {
		recent = append(recent, toBlogPost(db, post, viewerUserID))
	}

	return familyProfilePayload{
		ID:          family.ID,
		Handle:      family.Handle,
		DisplayName: family.DisplayName,
		AvatarURL:   family.AvatarURL,
		Bio:         family.Bio,
		City:        family.City,
		OwnerUserID: family.OwnerUserID,
		MemberCount: len(family.Members),
		Pets:        pets,
		Stats: map[string]int64{
			"posts":     postCount,
			"followers": followerCount,
			"pets":      int64(len(pets)),
		},
		RecentPosts: recent,
	}, http.StatusOK, nil
}

type petProfilePayload struct {
	ID                    string          `json:"id"`
	FamilyID              string          `json:"family_id"`
	FamilyDisplayName     string          `json:"family_display_name"`
	FamilyHandle          string          `json:"family_handle"`
	ViewerCanFollowFamily bool            `json:"viewer_can_follow_family"`
	DisplayName           string          `json:"display_name"`
	PublicSlug            string          `json:"public_slug"`
	AvatarURL             string          `json:"avatar_url,omitempty"`
	Species               string          `json:"species"`
	Breed                 string          `json:"breed,omitempty"`
	Headline              string          `json:"headline,omitempty"`
	Bio                   string          `json:"bio,omitempty"`
	DerivedSummary        map[string]any  `json:"derived_summary"`
	Visibility            map[string]bool `json:"visibility"`
}

func NewFamilyProfileHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		idOrHandle := strings.TrimSpace(r.PathValue("idOrHandle"))
		if idOrHandle == "" {
			http.Error(w, "family id or handle required", http.StatusBadRequest)
			return
		}

		payload, status, err := loadFamilyProfile(db, idOrHandle, strings.TrimSpace(r.Header.Get("X-User-Id")))
		if err != nil {
			if status == http.StatusNotFound {
				http.Error(w, "family not found", status)
				return
			}
			http.Error(w, "failed to load family: "+err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func NewFamilyProfileByOwnerHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ownerUserID := strings.TrimSpace(r.PathValue("ownerUserID"))
		if ownerUserID == "" {
			http.Error(w, "owner user id required", http.StatusBadRequest)
			return
		}

		var family models.Family
		if err := db.Select("id").Where("owner_user_id = ?", ownerUserID).First(&family).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "family not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to locate family: "+err.Error(), http.StatusInternalServerError)
			return
		}

		payload, status, err := loadFamilyProfile(db, family.ID, strings.TrimSpace(r.Header.Get("X-User-Id")))
		if err != nil {
			http.Error(w, "failed to load family: "+err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

type updateFamilyRequest struct {
	DisplayName string `json:"display_name"`
	Handle      string `json:"handle"`
	AvatarURL   string `json:"avatar_url"`
	Bio         string `json:"bio"`
	City        string `json:"city"`
}

// NewFamilyProfileMeHandler handles GET / PUT / POST /api/domain/families/me
// for the currently authenticated user. GET is identified by X-User-Id (legacy).
// PUT/POST are JWT-only (Bearer required; no X-User-Id fallback).
func NewFamilyProfileMeHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}

		switch r.Method {
		case http.MethodGet:
			userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
			if userID == "" {
				http.Error(w, "X-User-Id header required", http.StatusUnauthorized)
				return
			}
			var family models.Family
			if err := db.Where("owner_user_id = ?", userID).First(&family).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					http.Error(w, "family not found", http.StatusNotFound)
					return
				}
				http.Error(w, "failed to load family: "+err.Error(), http.StatusInternalServerError)
				return
			}
			payload, status, err := loadFamilyProfile(db, family.ID, userID)
			if err != nil {
				http.Error(w, "failed to load family: "+err.Error(), status)
				return
			}
			writeJSON(w, http.StatusOK, payload)

		case http.MethodPost, http.MethodPut:
			// Writes are JWT-only; X-User-Id is NOT accepted here.
			userID, ok := authenticatedUserID(w, r)
			if !ok {
				return
			}

			var req updateFamilyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeAuthError(w, http.StatusBadRequest, "Invalid request body")
				return
			}

			req.DisplayName = strings.TrimSpace(req.DisplayName)
			req.Handle = strings.TrimSpace(strings.ToLower(req.Handle))
			req.Bio = strings.TrimSpace(req.Bio)
			req.City = strings.TrimSpace(req.City)

			var family models.Family
			err := db.Where("owner_user_id = ?", userID).First(&family).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "failed to load family: "+err.Error(), http.StatusInternalServerError)
				return
			}

			if errors.Is(err, gorm.ErrRecordNotFound) {
				if req.DisplayName == "" || req.Handle == "" {
					writeAuthError(w, http.StatusBadRequest, "display_name and handle are required")
					return
				}
				family = models.Family{
					OwnerUserID: userID,
					DisplayName: req.DisplayName,
					Handle:      req.Handle,
					AvatarURL:   req.AvatarURL,
					Bio:         req.Bio,
					City:        req.City,
					IsPublic:    true,
				}
				if err := db.Create(&family).Error; err != nil {
					http.Error(w, "failed to create family: "+err.Error(), http.StatusInternalServerError)
					return
				}
				member := models.FamilyMember{
					FamilyID:     family.ID,
					UserID:       userID,
					DisplayName:  req.DisplayName,
					Role:         "owner",
					Relationship: "Parent",
					IsPrimary:    true,
				}
				_ = db.Create(&member).Error
			} else {
				if req.DisplayName != "" {
					family.DisplayName = req.DisplayName
				}
				if req.Handle != "" && req.Handle != family.Handle {
					var count int64
					_ = db.Model(&models.Family{}).Where("handle = ? AND id != ?", req.Handle, family.ID).Count(&count).Error
					if count > 0 {
						writeAuthError(w, http.StatusConflict, "Handle already taken")
						return
					}
					family.Handle = req.Handle
				}
				family.AvatarURL = req.AvatarURL
				family.Bio = req.Bio
				family.City = req.City
				if err := db.Save(&family).Error; err != nil {
					http.Error(w, "failed to update family: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}

			payload, status, err := loadFamilyProfile(db, family.ID, userID)
			if err != nil {
				http.Error(w, "failed to load family: "+err.Error(), status)
				return
			}
			writeJSON(w, http.StatusOK, payload)

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func NewPetProfileHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		slug := strings.TrimSpace(r.PathValue("slug"))
		if slug == "" {
			http.Error(w, "pet slug required", http.StatusBadRequest)
			return
		}

		var public models.PetPublicProfile
		if err := db.Where("slug = ?", slug).First(&public).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "pet profile not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load pet profile: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var pet models.Pet
		if err := db.Preload("VisibilitySettings").Preload("DerivedSummary").First(&pet, "id = ?", public.PetID).Error; err != nil {
			http.Error(w, "failed to load pet: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var family models.Family
		if err := db.First(&family, "id = ?", pet.FamilyID).Error; err != nil {
			http.Error(w, "failed to load family: "+err.Error(), http.StatusInternalServerError)
			return
		}

		derived := map[string]any{
			"display_age": pet.DerivedSummary.DisplayAge,
		}
		if pet.VisibilitySettings.ShowLatestWeight {
			derived["latest_weight_kg"] = pet.DerivedSummary.LatestWeightKg
		}
		if pet.VisibilitySettings.ShowVaccineStatus {
			derived["vaccine_status"] = pet.DerivedSummary.VaccineStatus
			derived["last_vaccination_at"] = pet.DerivedSummary.LastVaccinationAt
			derived["next_vaccination_due_at"] = pet.DerivedSummary.NextVaccinationDueAt
		}

		payload := petProfilePayload{
			ID:                    pet.ID,
			FamilyID:              family.ID,
			FamilyDisplayName:     family.DisplayName,
			FamilyHandle:          family.Handle,
			ViewerCanFollowFamily: strings.TrimSpace(r.Header.Get("X-User-Id")) != "" && strings.TrimSpace(r.Header.Get("X-User-Id")) != family.OwnerUserID,
			DisplayName:           public.DisplayName,
			PublicSlug:            public.Slug,
			AvatarURL:             public.AvatarURL,
			Species:               pet.Species,
			Headline:              public.Headline,
			Bio:                   public.Bio,
			DerivedSummary:        derived,
			Visibility: map[string]bool{
				"show_breed":          pet.VisibilitySettings.ShowBreed,
				"show_age":            pet.VisibilitySettings.ShowAge,
				"show_latest_weight":  pet.VisibilitySettings.ShowLatestWeight,
				"show_vaccine_status": pet.VisibilitySettings.ShowVaccineStatus,
				"show_family_link":    pet.VisibilitySettings.ShowFamilyLink,
			},
		}
		if pet.VisibilitySettings.ShowBreed {
			payload.Breed = pet.Breed
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func NewPostPetTagsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		petID := strings.TrimSpace(r.URL.Query().Get("pet_id"))
		familyID := strings.TrimSpace(r.URL.Query().Get("family_id"))
		if petID == "" && familyID == "" {
			http.Error(w, "pet_id or family_id required", http.StatusBadRequest)
			return
		}

		query := db.Preload("Images").Preload("Likes").Preload("Comments").Preload("Collections").Where("visibility = ?", "public").Order("created_at DESC")
		if familyID != "" {
			query = query.Where("family_id = ?", familyID)
		}
		if petID != "" {
			var postIDs []string
			if err := db.Model(&models.PostPetTag{}).Where("pet_id = ?", petID).Pluck("post_id", &postIDs).Error; err != nil {
				http.Error(w, "failed to load tags: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if len(postIDs) == 0 {
				writeJSON(w, http.StatusOK, []models.BlogPost{})
				return
			}
			query = query.Where("id IN ?", postIDs)
		}

		var posts []models.Post
		if err := query.Find(&posts).Error; err != nil {
			http.Error(w, "failed to load posts: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp := make([]models.BlogPost, 0, len(posts))
		for _, post := range posts {
			resp = append(resp, toBlogPost(db, post, strings.TrimSpace(r.Header.Get("X-User-Id"))))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func pickWeight(visible bool, weight *float64) *float64 {
	if !visible {
		return nil
	}
	return weight
}

func pickString(visible bool, value string) string {
	if !visible {
		return ""
	}
	return value
}

func pickTime(visible bool, value *time.Time) *time.Time {
	if !visible {
		return nil
	}
	return value
}
