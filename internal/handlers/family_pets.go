package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// Field limits for the family-pet write API.
const (
	maxClientPetIDLen = 100
	maxPetNameLen     = 100
	maxPetSpeciesLen  = 50
	maxPetBreedLen    = 100
	maxPetSexLen      = 20
	maxPetAvatarLen   = 500
	minPetBirthYear   = 1990
)

// createPetRequest is the body for POST /api/domain/families/me/pets.
// Private/medical fields (microchip, notes, allergies, weight, vaccination)
// are intentionally NOT part of this struct and are never accepted or stored.
type createPetRequest struct {
	ClientPetID string `json:"client_pet_id"`
	Name        string `json:"name"`
	Species     string `json:"species"`
	Breed       string `json:"breed"`
	Sex         string `json:"sex"`
	BirthYear   *int   `json:"birth_year"`
	AvatarURL   string `json:"avatar_url"`
}

// updatePetRequest is the body for PUT /api/domain/families/me/pets/{petID}.
// All fields optional; only provided fields are updated.
type updatePetRequest struct {
	Name      *string `json:"name"`
	Species   *string `json:"species"`
	Breed     *string `json:"breed"`
	Sex       *string `json:"sex"`
	BirthYear *int    `json:"birth_year"`
	AvatarURL *string `json:"avatar_url"`
}

// petMutationResponse is returned by the pet create/update endpoints.
type petMutationResponse struct {
	PetID       string `json:"pet_id"`
	ClientPetID string `json:"client_pet_id"`
	PublicSlug  string `json:"public_slug"`
}

func petMutationResponseOf(pet *models.Pet, slug string) petMutationResponse {
	clientPetID := ""
	if pet.ClientPetID != nil {
		clientPetID = *pet.ClientPetID
	}
	return petMutationResponse{
		PetID:       pet.ID,
		ClientPetID: clientPetID,
		PublicSlug:  slug,
	}
}

func birthDateFromYear(year int) time.Time {
	return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func validBirthYear(year int) bool {
	return year >= minPetBirthYear && year <= time.Now().UTC().Year()
}

var petSlugPartRe = regexp.MustCompile(`[^a-z0-9]+`)

// petPublicSlug builds a unique public slug: family handle + slugified pet
// name + short random suffix (collisions retried by the caller).
func petPublicSlug(familyHandle, petName string) string {
	part := petSlugPartRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(petName)), "-")
	part = strings.Trim(part, "-")
	if part == "" {
		part = "pet"
	}
	return fmt.Sprintf("%s-%s-%s", familyHandle, part, uuid.NewString()[:6])
}

// loadOwnedFamily resolves the JWT user's family. Returns gorm.ErrRecordNotFound
// (wrapped) when the user has no family.
func loadOwnedFamily(db *gorm.DB, userID string) (models.Family, error) {
	var family models.Family
	if err := db.Where("owner_user_id = ?", userID).First(&family).Error; err != nil {
		return models.Family{}, err
	}
	return family, nil
}

// NewFamilyPetCreateHandler handles POST /api/domain/families/me/pets
// JWT-only. Idempotent on (family_id, client_pet_id): re-posting the same
// client_pet_id returns the existing pet instead of creating a duplicate.
func NewFamilyPetCreateHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		var req createPetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		req.ClientPetID = strings.TrimSpace(req.ClientPetID)
		req.Name = strings.TrimSpace(req.Name)
		req.Species = strings.TrimSpace(req.Species)
		req.Breed = strings.TrimSpace(req.Breed)
		req.Sex = strings.TrimSpace(req.Sex)
		req.AvatarURL = strings.TrimSpace(req.AvatarURL)

		if req.ClientPetID == "" || req.Name == "" || req.Species == "" {
			writeAuthError(w, http.StatusBadRequest, "client_pet_id, name and species are required")
			return
		}
		if len(req.ClientPetID) > maxClientPetIDLen || len(req.Name) > maxPetNameLen ||
			len(req.Species) > maxPetSpeciesLen || len(req.Breed) > maxPetBreedLen ||
			len(req.Sex) > maxPetSexLen || len(req.AvatarURL) > maxPetAvatarLen {
			writeAuthError(w, http.StatusBadRequest, "One or more fields exceed the maximum length")
			return
		}
		if req.BirthYear != nil && !validBirthYear(*req.BirthYear) {
			writeAuthError(w, http.StatusBadRequest, "birth_year is out of range")
			return
		}

		family, err := loadOwnedFamily(db, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeAuthError(w, http.StatusNotFound, "Family not found")
				return
			}
			writeAuthError(w, http.StatusInternalServerError, "Failed to load family")
			return
		}

		// Idempotency pre-check.
		var existing models.Pet
		err = db.Where("family_id = ? AND client_pet_id = ?", family.ID, req.ClientPetID).First(&existing).Error
		if err == nil {
			writeJSON(w, http.StatusOK, petMutationResponseOf(&existing, publicSlugOf(db, existing.ID)))
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			writeAuthError(w, http.StatusInternalServerError, "Failed to check existing pet")
			return
		}

		// Create Pet + PetPublicProfile + PetVisibilitySetting + PetDerivedSummary
		// in ONE transaction. Unique conflicts on (family_id, client_pet_id) mean a
		// concurrent request won — re-select and return that record. Slug
		// collisions retry with a fresh random suffix.
		for attempt := 0; attempt < 3; attempt++ {
			pet := models.Pet{
				FamilyID:    family.ID,
				ClientPetID: &req.ClientPetID,
				Name:        req.Name,
				Species:     req.Species,
				Breed:       req.Breed,
				Sex:         req.Sex,
				AvatarURL:   req.AvatarURL,
			}
			if req.BirthYear != nil {
				birthDate := birthDateFromYear(*req.BirthYear)
				pet.BirthDate = &birthDate
			}
			slug := petPublicSlug(family.Handle, req.Name)

			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&pet).Error; err != nil {
					return err
				}
				profile := models.PetPublicProfile{
					PetID:       pet.ID,
					DisplayName: pet.Name,
					Slug:        slug,
					AvatarURL:   pet.AvatarURL,
				}
				if err := tx.Create(&profile).Error; err != nil {
					return err
				}
				visibility := models.PetVisibilitySetting{
					PetID:              pet.ID,
					ShowBreed:          true,
					ShowAge:            true,
					ShowLatestWeight:   false,
					ShowVaccineStatus:  false,
					ShowFamilyLink:     true,
					ShowMedicalSummary: false,
				}
				if err := tx.Create(&visibility).Error; err != nil {
					return err
				}
				summary := models.BuildPetDerivedSummary(pet, time.Now().UTC())
				return tx.Create(&summary).Error
			})
			if err == nil {
				writeJSON(w, http.StatusCreated, petMutationResponseOf(&pet, slug))
				return
			}
			if !isUniqueConstraintError(err) {
				writeAuthError(w, http.StatusInternalServerError, "Failed to create pet")
				return
			}

			// Unique conflict: if the (family_id, client_pet_id) row now exists,
			// a concurrent request created it — return the canonical record.
			var canonical models.Pet
			if selErr := db.Where("family_id = ? AND client_pet_id = ?", family.ID, req.ClientPetID).First(&canonical).Error; selErr == nil {
				writeJSON(w, http.StatusOK, petMutationResponseOf(&canonical, publicSlugOf(db, canonical.ID)))
				return
			}
			// Otherwise it was a slug collision; retry with a fresh suffix.
		}
		writeAuthError(w, http.StatusInternalServerError, "Failed to create pet: slug conflicts after retries")
	}
}

// NewFamilyPetUpdateHandler handles PUT /api/domain/families/me/pets/{petID}
// JWT-only. The pet must belong to the caller's family — 404 otherwise, so
// existence of other families' pets is not leaked.
func NewFamilyPetUpdateHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		petID := strings.TrimSpace(r.PathValue("petID"))
		if petID == "" {
			writeAuthError(w, http.StatusBadRequest, "pet id required")
			return
		}

		var req updatePetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAuthError(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		if req.Name != nil {
			*req.Name = strings.TrimSpace(*req.Name)
			if *req.Name == "" || len(*req.Name) > maxPetNameLen {
				writeAuthError(w, http.StatusBadRequest, "name must be 1-100 characters")
				return
			}
		}
		if req.Species != nil {
			*req.Species = strings.TrimSpace(*req.Species)
			if *req.Species == "" || len(*req.Species) > maxPetSpeciesLen {
				writeAuthError(w, http.StatusBadRequest, "species must be 1-50 characters")
				return
			}
		}
		if req.Breed != nil && len(strings.TrimSpace(*req.Breed)) > maxPetBreedLen {
			writeAuthError(w, http.StatusBadRequest, "breed exceeds the maximum length")
			return
		}
		if req.Sex != nil && len(strings.TrimSpace(*req.Sex)) > maxPetSexLen {
			writeAuthError(w, http.StatusBadRequest, "sex exceeds the maximum length")
			return
		}
		if req.AvatarURL != nil && len(strings.TrimSpace(*req.AvatarURL)) > maxPetAvatarLen {
			writeAuthError(w, http.StatusBadRequest, "avatar_url exceeds the maximum length")
			return
		}
		if req.BirthYear != nil && !validBirthYear(*req.BirthYear) {
			writeAuthError(w, http.StatusBadRequest, "birth_year is out of range")
			return
		}

		family, err := loadOwnedFamily(db, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeAuthError(w, http.StatusNotFound, "Family not found")
				return
			}
			writeAuthError(w, http.StatusInternalServerError, "Failed to load family")
			return
		}

		var pet models.Pet
		err = db.Where("id = ? AND family_id = ?", petID, family.ID).First(&pet).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Unknown id OR owned by another family — same 404, no leak.
				writeAuthError(w, http.StatusNotFound, "Pet not found")
				return
			}
			writeAuthError(w, http.StatusInternalServerError, "Failed to load pet")
			return
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			if req.Name != nil {
				pet.Name = *req.Name
			}
			if req.Species != nil {
				pet.Species = *req.Species
			}
			if req.Breed != nil {
				pet.Breed = strings.TrimSpace(*req.Breed)
			}
			if req.Sex != nil {
				pet.Sex = strings.TrimSpace(*req.Sex)
			}
			if req.AvatarURL != nil {
				pet.AvatarURL = strings.TrimSpace(*req.AvatarURL)
			}
			if req.BirthYear != nil {
				birthDate := birthDateFromYear(*req.BirthYear)
				pet.BirthDate = &birthDate
			}
			if err := tx.Save(&pet).Error; err != nil {
				return err
			}

			// Keep the public profile in sync with name/avatar changes.
			if req.Name != nil || req.AvatarURL != nil {
				var profile models.PetPublicProfile
				err := tx.Where("pet_id = ?", pet.ID).First(&profile).Error
				switch {
				case err == nil:
					if req.Name != nil {
						profile.DisplayName = pet.Name
					}
					if req.AvatarURL != nil {
						profile.AvatarURL = pet.AvatarURL
					}
					if err := tx.Save(&profile).Error; err != nil {
						return err
					}
				case errors.Is(err, gorm.ErrRecordNotFound):
					// Legacy pet without a public profile — nothing to sync.
				default:
					return err
				}
			}

			// Rebuild the derived summary when age-relevant data changes.
			if req.BirthYear != nil {
				rebuilt := models.BuildPetDerivedSummary(pet, time.Now().UTC())
				var summary models.PetDerivedSummary
				err := tx.Where("pet_id = ?", pet.ID).First(&summary).Error
				switch {
				case err == nil:
					summary.DisplayAge = rebuilt.DisplayAge
					summary.AgeYears = rebuilt.AgeYears
					summary.ComputedAt = rebuilt.ComputedAt
					summary.SourceUpdatedAt = rebuilt.SourceUpdatedAt
					if err := tx.Save(&summary).Error; err != nil {
						return err
					}
				case errors.Is(err, gorm.ErrRecordNotFound):
					if err := tx.Create(&rebuilt).Error; err != nil {
						return err
					}
				default:
					return err
				}
			}
			return nil
		})
		if err != nil {
			writeAuthError(w, http.StatusInternalServerError, "Failed to update pet")
			return
		}

		writeJSON(w, http.StatusOK, petMutationResponseOf(&pet, publicSlugOf(db, pet.ID)))
	}
}

// publicSlugOf looks up a pet's public slug; empty when the pet has no
// public profile (legacy rows only).
func publicSlugOf(db *gorm.DB, petID string) string {
	var profile models.PetPublicProfile
	if err := db.Select("slug").Where("pet_id = ?", petID).First(&profile).Error; err != nil {
		return ""
	}
	return profile.Slug
}
