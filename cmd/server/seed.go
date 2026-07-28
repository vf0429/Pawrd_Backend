package main

import (
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// SeedTestAccounts creates the three dev test accounts in users.db.
// Safe to call on every startup — skips accounts that already exist.
func SeedTestAccounts() {
	type account struct {
		name  string
		email string
		pw    string
	}
	accounts := []account{
		{"Vince", "vince@pawrd.com", "Test123!"},
		{"Alice", "alice@pawrd.com", "Test123!"},
		{"Bob", "bob@pawrd.com", "Test123!"},
	}

	for _, a := range accounts {
		var existing models.AuthUser
		if err := models.AuthDB.Where("email = ?", a.email).First(&existing).Error; err == nil {
			continue // already exists
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(a.pw), bcrypt.DefaultCost)
		if err != nil {
			log.Printf("Failed to hash password for %s: %v", a.email, err)
			continue
		}
		user := models.AuthUser{
			Email:        a.email,
			Phone:        "phone-not-set-" + uuid.New().String(),
			PasswordHash: string(hash),
			Name:         a.name,
		}
		if err := models.AuthDB.Create(&user).Error; err != nil {
			log.Printf("Failed to seed account %s: %v", a.email, err)
			continue
		}
		log.Printf("Seeded test account: %s (%s)", a.name, a.email)
	}
}

func EnsureDomainSeedData(db *gorm.DB) {
	var owner models.AuthUser
	if err := models.AuthDB.Where("email = ?", "vince@pawrd.com").First(&owner).Error; err != nil {
		log.Printf("Domain seed skipped: owner account missing: %v", err)
		return
	}

	var family models.Family
	if err := db.Where("owner_user_id = ?", owner.ToResponse().ID).First(&family).Error; err == nil {
		return
	}

	family = models.Family{
		OwnerUserID: owner.ToResponse().ID,
		DisplayName: "The Chan Family",
		Handle:      "chan-family",
		AvatarURL:   "",
		Bio:         "Two pets, one warm home, and lots of everyday stories.",
		City:        "Hong Kong",
		IsPublic:    true,
	}
	if err := db.Create(&family).Error; err != nil {
		log.Printf("Domain seed family create failed: %v", err)
		return
	}

	member := models.FamilyMember{
		FamilyID:     family.ID,
		UserID:       owner.ToResponse().ID,
		DisplayName:  owner.Name,
		Role:         "owner",
		Relationship: "Parent",
		IsPrimary:    true,
	}
	_ = db.Create(&member).Error

	birthA := time.Date(2022, 4, 3, 0, 0, 0, 0, time.UTC)
	birthB := time.Date(2021, 9, 18, 0, 0, 0, 0, time.UTC)
	weightA := 4.2
	weightB := 22.6
	lastVac := time.Date(2026, 2, 16, 0, 0, 0, 0, time.UTC)
	nextVac := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)

	pets := []models.Pet{
		{
			FamilyID:             family.ID,
			Name:                 "Mochi",
			Species:              "Cat",
			Breed:                "British Shorthair",
			Sex:                  "Female",
			BirthDate:            &birthA,
			CurrentWeightKg:      &weightA,
			LastVaccinationAt:    &lastVac,
			NextVaccinationDueAt: &nextVac,
		},
		{
			FamilyID:             family.ID,
			Name:                 "Buddy",
			Species:              "Dog",
			Breed:                "Golden Retriever",
			Sex:                  "Male",
			BirthDate:            &birthB,
			CurrentWeightKg:      &weightB,
			LastVaccinationAt:    &lastVac,
			NextVaccinationDueAt: &nextVac,
		},
	}

	for _, pet := range pets {
		if err := db.Create(&pet).Error; err != nil {
			log.Printf("Domain seed pet create failed: %v", err)
			continue
		}
		publicProfile := models.PetPublicProfile{
			PetID:       pet.ID,
			DisplayName: pet.Name,
			Slug:        slugifyPetName(family.Handle, pet.Name),
			Headline:    defaultHeadlineForSpecies(pet.Species),
			Bio:         defaultBioForPet(pet.Name),
			AvatarURL:   "",
		}
		visibility := models.PetVisibilitySetting{
			PetID:             pet.ID,
			ShowBreed:         true,
			ShowAge:           true,
			ShowLatestWeight:  false,
			ShowVaccineStatus: false,
			ShowFamilyLink:    true,
		}
		summary := models.BuildPetDerivedSummary(pet, time.Now().UTC())
		_ = db.Create(&publicProfile).Error
		_ = db.Create(&visibility).Error
		_ = db.Create(&summary).Error
	}

	var seededPets []models.Pet
	_ = db.Where("family_id = ?", family.ID).Find(&seededPets).Error
	if len(seededPets) < 2 {
		return
	}
	mochiID := seededPets[0].ID
	buddyID := seededPets[1].ID

	posts := []models.Post{
		{
			AuthorID:     owner.ToResponse().ID,
			FamilyID:     family.ID,
			AuthorName:   family.DisplayName,
			AuthorAvatar: "",
			Title:        "Morning walk by the harbour",
			Content:      "Buddy led the way while Mochi watched from the stroller.",
			ImageColor:   "blue",
			Visibility:   "public",
			AllowComment: true,
		},
		{
			AuthorID:     owner.ToResponse().ID,
			FamilyID:     family.ID,
			AuthorName:   family.DisplayName,
			AuthorAvatar: "",
			Title:        "Mochi's new window corner",
			Content:      "A sunny spot and a soft blanket solved the whole afternoon.",
			ImageColor:   "orange",
			Visibility:   "public",
			AllowComment: true,
		},
		{
			AuthorID:     owner.ToResponse().ID,
			FamilyID:     family.ID,
			AuthorName:   family.DisplayName,
			AuthorAvatar: "",
			Title:        "Vaccination day done",
			Content:      "Both pets were brave, and now everyone deserves treats and a nap.",
			ImageColor:   "green",
			Visibility:   "followers",
			AllowComment: true,
		},
	}
	for idx := range posts {
		var existing models.Post
		if err := db.Where("family_id = ? AND title = ?", family.ID, posts[idx].Title).First(&existing).Error; err == nil {
			posts[idx] = existing
			continue
		}
		_ = db.Create(&posts[idx]).Error
	}

	tags := []models.PostPetTag{
		{PostID: posts[0].ID, PetID: buddyID, IsPrimary: true},
		{PostID: posts[0].ID, PetID: mochiID, IsPrimary: false},
		{PostID: posts[1].ID, PetID: mochiID, IsPrimary: true},
		{PostID: posts[2].ID, PetID: buddyID, IsPrimary: true},
		{PostID: posts[2].ID, PetID: mochiID, IsPrimary: false},
	}
	for _, tag := range tags {
		var count int64
		_ = db.Model(&models.PostPetTag{}).Where("post_id = ? AND pet_id = ?", tag.PostID, tag.PetID).Count(&count).Error
		if count == 0 {
			_ = db.Create(&tag).Error
		}
	}
}

func SeedDatabase(db *gorm.DB) {
	log.Println("Starting database seed...")

	// Clear existing tables in the right order to respect foreign keys
	db.Exec("DELETE FROM payouts")
	db.Exec("DELETE FROM cost_items")
	db.Exec("DELETE FROM scenarios")
	db.Exec("DELETE FROM insurers")

	// 1. Create Insurers
	insurers := []models.Insurer{
		{ID: "onedegree", Name: "OneDegree", PlanName: "Prestige"},
		{ID: "bluecross", Name: "Blue Cross", PlanName: "Plan A"},
		{ID: "prudential", Name: "Prudential", PlanName: "Plan B"},
		{ID: "bolttech", Name: "bolttech", PlanName: "Plan 3"},
		{ID: "happytails", Name: "HappyTails", PlanName: "Ultimate Dog"},
	}
	db.Create(&insurers)

	// 2. Create Scenarios with embedded CostItems and Payouts
	scenarios := []models.Scenario{
		{
			Title:        "一般小病（严重耳道发炎）",
			Description:  "宠物常见的耳道发炎治疗费用",
			TotalCostHKD: 1500,
			CostItems: []models.CostItem{
				{ItemName: "诊金", AmountHKD: 500},
				{ItemName: "药物", AmountHKD: 500},
				{ItemName: "治疗", AmountHKD: 500},
			},
			Payouts: []models.Payout{
				{InsurerID: "onedegree", EstimatedPayoutHKD: 1350, CoveragePercentage: 90.0, IsRecommended: true},
				{InsurerID: "bluecross", EstimatedPayoutHKD: 1050, CoveragePercentage: 70.0},
				{InsurerID: "prudential", EstimatedPayoutHKD: 850, CoveragePercentage: 56.7},
				{InsurerID: "bolttech", EstimatedPayoutHKD: 600, CoveragePercentage: 40.0},
				{InsurerID: "happytails", EstimatedPayoutHKD: 0, CoveragePercentage: 0.0},
			},
		},
		{
			Title:        "重大意外手术（骨折/韧带断裂）",
			TotalCostHKD: 45000,
			CostItems: []models.CostItem{
				{ItemName: "手术费", AmountHKD: 25000},
				{ItemName: "麻醉师", AmountHKD: 6000},
				{ItemName: "手术室", AmountHKD: 4000},
				{ItemName: "住院", AmountHKD: 3000},
				{ItemName: "X光化验", AmountHKD: 5000},
				{ItemName: "杂项", AmountHKD: 2000},
			},
			Payouts: []models.Payout{
				{InsurerID: "onedegree", EstimatedPayoutHKD: 40500, CoveragePercentage: 90.0, IsRecommended: true},
				{InsurerID: "happytails", EstimatedPayoutHKD: 36000, CoveragePercentage: 80.0, IsRecommended: true},
				{InsurerID: "bluecross", EstimatedPayoutHKD: 31500, CoveragePercentage: 70.0},
				{InsurerID: "prudential", EstimatedPayoutHKD: 26000, CoveragePercentage: 57.8},
				{InsurerID: "bolttech", EstimatedPayoutHKD: 25000, CoveragePercentage: 55.6},
			},
		},
		{
			Title:        "癌症化疗",
			TotalCostHKD: 30000,
			CostItems: []models.CostItem{
				{ItemName: "化疗", AmountHKD: 30000},
			},
			Payouts: []models.Payout{
				{InsurerID: "onedegree", EstimatedPayoutHKD: 37000, CoveragePercentage: 123.3, IsRecommended: true, Analysis: "含$10000癌症现金"},
				{InsurerID: "bluecross", EstimatedPayoutHKD: 21000, CoveragePercentage: 70.0},
				{InsurerID: "prudential", EstimatedPayoutHKD: 20000, CoveragePercentage: 66.7},
				{InsurerID: "bolttech", EstimatedPayoutHKD: 18000, CoveragePercentage: 60.0},
				{InsurerID: "happytails", EstimatedPayoutHKD: 11000, CoveragePercentage: 36.7},
			},
		},
		{
			Title:        "第三者责任（狗咬人）",
			TotalCostHKD: 50000,
			CostItems: []models.CostItem{
				{ItemName: "第三者责任", AmountHKD: 50000},
			},
			Payouts: []models.Payout{
				{InsurerID: "prudential", EstimatedPayoutHKD: 47000, CoveragePercentage: 94.0, IsRecommended: true},
				{InsurerID: "bluecross", EstimatedPayoutHKD: 47000, CoveragePercentage: 94.0},
				{InsurerID: "bolttech", EstimatedPayoutHKD: 47000, CoveragePercentage: 94.0},
				{InsurerID: "happytails", EstimatedPayoutHKD: 40000, CoveragePercentage: 80.0},
				{InsurerID: "onedegree", EstimatedPayoutHKD: 0, CoveragePercentage: 0.0},
			},
		},
		{
			Title:        "遗传性疾病手术（髌骨脱臼）",
			TotalCostHKD: 20000,
			CostItems: []models.CostItem{
				{ItemName: "手术费", AmountHKD: 20000},
			},
			Payouts: []models.Payout{
				{InsurerID: "happytails", EstimatedPayoutHKD: 16000, CoveragePercentage: 80.0, IsRecommended: true},
				{InsurerID: "onedegree", EstimatedPayoutHKD: 0, CoveragePercentage: 0.0, Analysis: "遗传病不保"},
				{InsurerID: "bluecross", EstimatedPayoutHKD: 0, CoveragePercentage: 0.0, Analysis: "遗传病不保"},
				{InsurerID: "prudential", EstimatedPayoutHKD: 0, CoveragePercentage: 0.0, Analysis: "遗传病不保"},
				{InsurerID: "bolttech", EstimatedPayoutHKD: 0, CoveragePercentage: 0.0, Analysis: "遗传病不保"},
			},
		},
	}

	db.Create(&scenarios)
	log.Println("Database seeded successfully.")
}

func slugifyPetName(familyHandle, petName string) string {
	name := strings.ToLower(strings.TrimSpace(petName))
	name = strings.ReplaceAll(name, " ", "-")
	return familyHandle + "-" + name
}

func defaultHeadlineForSpecies(species string) string {
	if strings.EqualFold(species, "cat") {
		return "Sunspot collector and gentle observer"
	}
	return "Walk partner, snack inspector, and family greeter"
}

func defaultBioForPet(name string) string {
	return name + " is part of a family profile seed used to validate pet-tagged posting and privacy-aware public views."
}
