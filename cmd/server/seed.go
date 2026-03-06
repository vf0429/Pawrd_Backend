package main

import (
	"log"

	"github.com/google/uuid"
	"github.com/vf0429/Petwell_Backend/internal/models"
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
		{"Vince", "vince@petwell.com", "Test123!"},
		{"Alice", "alice@petwell.com", "Test123!"},
		{"Bob", "bob@petwell.com", "Test123!"},
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
