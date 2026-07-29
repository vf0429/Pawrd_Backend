package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// seedUserWithFamily creates an auth user plus their family (via the same
// idempotent helper the login/register/profile flows use).
func seedUserWithFamily(t *testing.T, db *gorm.DB, email, name string) (string, string, models.Family) {
	t.Helper()
	userID, token := seedUnifiedUser(t, db, email, name)
	var user models.AuthUser
	if err := db.First(&user, "id = ?", userID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if err := createFamilyForUser(db, &user); err != nil {
		t.Fatalf("create family: %v", err)
	}
	var family models.Family
	if err := db.Where("owner_user_id = ?", userID).First(&family).Error; err != nil {
		t.Fatalf("load family: %v", err)
	}
	return userID, token, family
}

func decodePetMutationResponse(t *testing.T, rec *httptest.ResponseRecorder) petMutationResponse {
	t.Helper()
	var resp petMutationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

func TestFamilyPetCreateHappyPath(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	userID, token, _ := seedUserWithFamily(t, db, "pet-owner@example.com", "Pet Owner")
	year := 2022

	rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, map[string]any{
		"client_pet_id": "ios-uuid-1",
		"name":          "Mochi",
		"species":       "Cat",
		"breed":         "British Shorthair",
		"sex":           "Female",
		"birth_year":    year,
		"avatar_url":    "https://example.com/mochi.png",
		// Private/medical fields must be ignored, never stored.
		"microchip":    "CHIP-123",
		"notes":        "allergic to chicken",
		"allergies":    "chicken",
		"weight_kg":    4.2,
		"chip_id":      "CHIP-123",
		"microchip_id": "CHIP-123",
	})
	NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decodePetMutationResponse(t, rec)
	if resp.PetID == "" || resp.PublicSlug == "" || resp.ClientPetID != "ios-uuid-1" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var pet models.Pet
	if err := db.First(&pet, "id = ?", resp.PetID).Error; err != nil {
		t.Fatalf("load pet: %v", err)
	}
	if pet.Name != "Mochi" || pet.Species != "Cat" || pet.Breed != "British Shorthair" || pet.Sex != "Female" {
		t.Fatalf("unexpected pet fields: %+v", pet)
	}
	if pet.ClientPetID == nil || *pet.ClientPetID != "ios-uuid-1" {
		t.Fatalf("client_pet_id not stored: %+v", pet.ClientPetID)
	}
	if pet.BirthDate == nil || *pet.BirthDate != birthDateFromYear(year) {
		t.Fatalf("unexpected birth date: %v", pet.BirthDate)
	}
	if pet.MicrochipID != "" || pet.PrivateNotes != "" || pet.CurrentWeightKg != nil {
		t.Fatalf("private fields must not be stored: %+v", pet)
	}

	var profile models.PetPublicProfile
	if err := db.Where("pet_id = ?", pet.ID).First(&profile).Error; err != nil {
		t.Fatalf("public profile missing: %v", err)
	}
	if profile.DisplayName != "Mochi" || profile.Slug != resp.PublicSlug || profile.AvatarURL != "https://example.com/mochi.png" {
		t.Fatalf("unexpected public profile: %+v", profile)
	}

	var visibility models.PetVisibilitySetting
	if err := db.Where("pet_id = ?", pet.ID).First(&visibility).Error; err != nil {
		t.Fatalf("visibility settings missing: %v", err)
	}
	if !visibility.ShowBreed || !visibility.ShowAge || !visibility.ShowFamilyLink {
		t.Fatalf("expected breed/age/family-link visible by default: %+v", visibility)
	}
	if visibility.ShowLatestWeight || visibility.ShowVaccineStatus || visibility.ShowMedicalSummary {
		t.Fatalf("weight/vaccine/medical must NOT auto-publish by default: %+v", visibility)
	}

	var summary models.PetDerivedSummary
	if err := db.Where("pet_id = ?", pet.ID).First(&summary).Error; err != nil {
		t.Fatalf("derived summary missing: %v", err)
	}
	expectedYears := time.Now().UTC().Year() - year
	if summary.AgeYears == nil || *summary.AgeYears != expectedYears {
		t.Fatalf("expected age %d, got %+v", expectedYears, summary.AgeYears)
	}

	// The pet must appear in the family profile and be counted in stats.
	getReq := httptest.NewRequest(http.MethodGet, "/api/domain/families/me", nil)
	getReq.Header.Set("X-User-Id", userID)
	getRec := httptest.NewRecorder()
	NewFamilyProfileMeHandler(db).ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("family profile: expected 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	var familyPayload struct {
		Pets []struct {
			ID         string `json:"id"`
			PublicSlug string `json:"public_slug"`
		} `json:"pets"`
		Stats map[string]int64 `json:"stats"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &familyPayload); err != nil {
		t.Fatalf("decode family profile: %v", err)
	}
	if familyPayload.Stats["pets"] != 1 {
		t.Fatalf("expected stats.pets = 1, got %+v", familyPayload.Stats)
	}
	if len(familyPayload.Pets) != 1 || familyPayload.Pets[0].PublicSlug != resp.PublicSlug {
		t.Fatalf("expected pet in family profile, got %+v", familyPayload.Pets)
	}
}

func TestFamilyPetCreateIdempotent(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, token, _ := seedUserWithFamily(t, db, "idem@example.com", "Idem")

	body := map[string]any{"client_pet_id": "same-id", "name": "Buddy", "species": "Dog"}

	rec1, req1 := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, body)
	NewFamilyPetCreateHandler(db).ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first POST: expected 201, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	first := decodePetMutationResponse(t, rec1)

	rec2, req2 := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, body)
	NewFamilyPetCreateHandler(db).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second POST: expected 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	second := decodePetMutationResponse(t, rec2)

	if first.PetID != second.PetID || first.PublicSlug != second.PublicSlug {
		t.Fatalf("idempotent re-POST returned different records: %+v vs %+v", first, second)
	}

	var count int64
	if err := db.Model(&models.Pet{}).Where("client_pet_id = ?", "same-id").Count(&count).Error; err != nil {
		t.Fatalf("count pets: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 pet, got %d", count)
	}
}

func TestFamilyPetCreateConcurrent(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	// Serialize SQLite access on one connection; goroutines still race between
	// the idempotency pre-check and the transactional insert.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	_, token, family := seedUserWithFamily(t, db, "race@example.com", "Race")
	handler := NewFamilyPetCreateHandler(db)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	petIDs := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, map[string]any{
				"client_pet_id": "race-pet",
				"name":          "Racer",
				"species":       "Dog",
			})
			handler.ServeHTTP(rec, req)
			codes[i] = rec.Code
			if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
				petIDs[i] = decodePetMutationResponse(t, rec).PetID
			}
		}(i)
	}
	wg.Wait()

	for i := range codes {
		if codes[i] != http.StatusOK && codes[i] != http.StatusCreated {
			t.Fatalf("goroutine %d: expected 200/201, got %d", i, codes[i])
		}
		if petIDs[i] != petIDs[0] {
			t.Fatalf("goroutine %d got pet_id %s, expected %s", i, petIDs[i], petIDs[0])
		}
	}

	var petCount int64
	if err := db.Model(&models.Pet{}).Where("family_id = ? AND client_pet_id = ?", family.ID, "race-pet").Count(&petCount).Error; err != nil {
		t.Fatalf("count pets: %v", err)
	}
	if petCount != 1 {
		t.Fatalf("expected exactly 1 pet after %d concurrent POSTs, got %d", n, petCount)
	}
}

func TestFamilyPetCreateValidation(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, token, _ := seedUserWithFamily(t, db, "valid@example.com", "Valid")

	cases := map[string]map[string]any{
		"missing client_pet_id": {"name": "X", "species": "Cat"},
		"missing name":          {"client_pet_id": "c1", "species": "Cat"},
		"missing species":       {"client_pet_id": "c2", "name": "X"},
		"oversized name":        {"client_pet_id": "c3", "name": strings.Repeat("a", 101), "species": "Cat"},
		"oversized species":     {"client_pet_id": "c4", "name": "X", "species": strings.Repeat("b", 51)},
		"birth_year too early":  {"client_pet_id": "c5", "name": "X", "species": "Cat", "birth_year": 1800},
		"birth_year in future":  {"client_pet_id": "c6", "name": "X", "species": "Cat", "birth_year": time.Now().UTC().Year() + 1},
	}
	for name, body := range cases {
		rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, body)
		NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestFamilyPetCreateRequiresJWT(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)

	rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", "", map[string]any{
		"client_pet_id": "c1", "name": "X", "species": "Cat",
	})
	req.Header.Set("X-User-Id", "1") // legacy header must NOT be accepted
	NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFamilyPetCreateNoFamily(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, token := seedUnifiedUser(t, db, "nofam@example.com", "No Family")

	rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, map[string]any{
		"client_pet_id": "c1", "name": "X", "species": "Cat",
	})
	NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFamilyPetUpdateSyncsPublicProfile(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, token, _ := seedUserWithFamily(t, db, "upd@example.com", "Updater")

	rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, map[string]any{
		"client_pet_id": "upd-pet", "name": "Old Name", "species": "Cat", "birth_year": 2020,
	})
	NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	created := decodePetMutationResponse(t, rec)

	newYear := time.Now().UTC().Year() - 3
	rec, req = authedRequest(t, http.MethodPut, "/api/domain/families/me/pets/"+created.PetID, token, map[string]any{
		"name":       "New Name",
		"avatar_url": "https://example.com/new.png",
		"birth_year": newYear,
	})
	req.SetPathValue("petID", created.PetID)
	NewFamilyPetUpdateHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decodePetMutationResponse(t, rec)
	if resp.PetID != created.PetID || resp.PublicSlug != created.PublicSlug || resp.ClientPetID != "upd-pet" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var pet models.Pet
	if err := db.First(&pet, "id = ?", created.PetID).Error; err != nil {
		t.Fatalf("reload pet: %v", err)
	}
	if pet.Name != "New Name" || pet.AvatarURL != "https://example.com/new.png" || pet.Species != "Cat" {
		t.Fatalf("unexpected pet after update: %+v", pet)
	}
	if pet.BirthDate == nil || *pet.BirthDate != birthDateFromYear(newYear) {
		t.Fatalf("birth date not updated: %v", pet.BirthDate)
	}

	var profile models.PetPublicProfile
	if err := db.Where("pet_id = ?", pet.ID).First(&profile).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.DisplayName != "New Name" || profile.AvatarURL != "https://example.com/new.png" {
		t.Fatalf("public profile not synced: %+v", profile)
	}

	var summary models.PetDerivedSummary
	if err := db.Where("pet_id = ?", pet.ID).First(&summary).Error; err != nil {
		t.Fatalf("load summary: %v", err)
	}
	if summary.AgeYears == nil || *summary.AgeYears != 3 {
		t.Fatalf("expected derived age rebuilt to 3, got %+v", summary.AgeYears)
	}

	// Partial update: breed only, everything else untouched.
	rec, req = authedRequest(t, http.MethodPut, "/api/domain/families/me/pets/"+created.PetID, token, map[string]any{
		"breed": "Siamese",
	})
	req.SetPathValue("petID", created.PetID)
	NewFamilyPetUpdateHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial update: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var pet2 models.Pet
	if err := db.First(&pet2, "id = ?", created.PetID).Error; err != nil {
		t.Fatalf("reload pet: %v", err)
	}
	if pet2.Breed != "Siamese" || pet2.Name != "New Name" || pet2.Species != "Cat" {
		t.Fatalf("partial update clobbered fields: %+v", pet2)
	}
}

func TestFamilyPetUpdateOwnership(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, tokenA, _ := seedUserWithFamily(t, db, "owner-a@example.com", "Owner A")
	_, tokenB, _ := seedUserWithFamily(t, db, "owner-b@example.com", "Owner B")

	rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", tokenA, map[string]any{
		"client_pet_id": "a-pet", "name": "A Pet", "species": "Cat",
	})
	NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	petID := decodePetMutationResponse(t, rec).PetID

	// Non-owner gets 404, not 403 — no existence leak.
	rec, req = authedRequest(t, http.MethodPut, "/api/domain/families/me/pets/"+petID, tokenB, map[string]any{"name": "Stolen"})
	req.SetPathValue("petID", petID)
	NewFamilyPetUpdateHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-owner: expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Unknown pet id also 404.
	rec, req = authedRequest(t, http.MethodPut, "/api/domain/families/me/pets/does-not-exist", tokenA, map[string]any{"name": "Ghost"})
	req.SetPathValue("petID", "does-not-exist")
	NewFamilyPetUpdateHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown pet: expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// No JWT → 401.
	rec, req = authedRequest(t, http.MethodPut, "/api/domain/families/me/pets/"+petID, "", map[string]any{"name": "Anon"})
	req.SetPathValue("petID", petID)
	NewFamilyPetUpdateHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	var pet models.Pet
	if err := db.First(&pet, "id = ?", petID).Error; err != nil {
		t.Fatalf("reload pet: %v", err)
	}
	if pet.Name != "A Pet" {
		t.Fatalf("pet must be unchanged, got %q", pet.Name)
	}
}

func TestFamilyPetCreateRollsBackOnMidTransactionFailure(t *testing.T) {
	// DB without the pet_visibility_settings table → the transaction fails
	// after the pet + public profile inserts, and both must be rolled back.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.Family{}, &models.FamilyMember{}, &models.Pet{}, &models.PetPublicProfile{}, &models.PetDerivedSummary{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	family := models.Family{OwnerUserID: "9", DisplayName: "The Nine Family", Handle: "nine-family", IsPublic: true}
	if err := db.Create(&family).Error; err != nil {
		t.Fatalf("seed family: %v", err)
	}
	token, err := auth.GenerateToken("9", "nine@example.com", "Nine")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, map[string]any{
		"client_pet_id": "rollback-pet", "name": "Rollback", "species": "Dog",
	})
	NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}

	var petCount, profileCount int64
	if err := db.Model(&models.Pet{}).Count(&petCount).Error; err != nil {
		t.Fatalf("count pets: %v", err)
	}
	if err := db.Model(&models.PetPublicProfile{}).Count(&profileCount).Error; err != nil {
		t.Fatalf("count profiles: %v", err)
	}
	if petCount != 0 || profileCount != 0 {
		t.Fatalf("expected full rollback, got %d pets and %d profiles", petCount, profileCount)
	}
}

func TestFamilyPetCreateClientPetIDScopingAcrossFamilies(t *testing.T) {
	db := setupUnifiedProfileTestDB(t)
	_, tokenA, _ := seedUserWithFamily(t, db, "fam-a@example.com", "Fam A")
	_, tokenB, _ := seedUserWithFamily(t, db, "fam-b@example.com", "Fam B")

	// The same client_pet_id in two different families is legal — the unique
	// constraint is composite (family_id, client_pet_id).
	for i, token := range []string{tokenA, tokenB} {
		rec, req := authedRequest(t, http.MethodPost, "/api/domain/families/me/pets", token, map[string]any{
			"client_pet_id": "shared-client-id",
			"name":          fmt.Sprintf("Pet %d", i),
			"species":       "Cat",
		})
		NewFamilyPetCreateHandler(db).ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("family %d: expected 201, got %d body=%s", i, rec.Code, rec.Body.String())
		}
	}

	var count int64
	if err := db.Model(&models.Pet{}).Where("client_pet_id = ?", "shared-client-id").Count(&count).Error; err != nil {
		t.Fatalf("count pets: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 pets (one per family), got %d", count)
	}
}
