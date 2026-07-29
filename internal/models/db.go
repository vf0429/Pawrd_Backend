package models

import (
	"fmt"
	"log"
	"os"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		log.Println("DATABASE_URL variable found, connecting to PostgreSQL...")
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	} else {
		log.Println("DATABASE_URL not set, falling back to SQLite for pet cases (assets/pet_case.db)...")
		db, err = gorm.Open(sqlite.Open("assets/pet_case.db"), &gorm.Config{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	// Auto Migrate the schema
	err = db.AutoMigrate(
		&Scenario{},
		&CostItem{},
		&Insurer{},
		&Payout{},
		&Family{},
		&FamilyMember{},
		&Pet{},
		&PetPublicProfile{},
		&PetVisibilitySetting{},
		&Post{},
		&PostPetTag{},
		&PostImage{},
		&PostLike{},
		&PostCollection{},
		&PostComment{},
		&PostPoll{},
		&PostPollOption{},
		&PostPollVote{},
		&PostView{},
		&UserFollow{},
		&FamilyFollow{},
		&MedicalService{},
		&Partner{},
		&AppBookingMirror{},
		&HealthReport{},
		&ReportObservation{},
		&ReportVendorExtraction{},
		&PetAccessGrant{},
		&PetDerivedSummary{},
		&Notification{},
		&ChatMessage{},
		&BlankProduct{},
		&CustomDesign{},
		&HiCustomOrder{},
		&ShopOrder{},
		&ShopOrderItem{},
		&ShopIntegrationEvent{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate schema: %w", err)
	}
	if err := normalizePendingShopOrderIDs(db); err != nil {
		return nil, fmt.Errorf("failed to normalize pending shop orders: %w", err)
	}

	// If using PostgreSQL, migrate Auth schema to the same database
	if dsn != "" {
		log.Println("Migrating Auth schema to PostgreSQL database...")
		err = db.AutoMigrate(&AuthUser{}, &Verification{})
		if err != nil {
			return nil, fmt.Errorf("failed to auto migrate auth schema: %w", err)
		}
		AuthDB = db
	}

	log.Println("Database connection established and models migrated.")
	return db, nil
}

func normalizePendingShopOrderIDs(db *gorm.DB) error {
	// Empty strings in unique-indexed nullable columns must be NULL so multiple
	// pending rows don't collide (SQLite treats "" as a real value; Postgres
	// allows many NULLs). Applies to orders written before the columns became
	// nullable.
	if err := db.Model(&ShopOrder{}).
		Where("shopify_order_id = ?", "").
		Update("shopify_order_id", nil).Error; err != nil {
		return err
	}
	return db.Model(&ShopOrder{}).
		Where("payment_intent_id = ?", "").
		Update("payment_intent_id", nil).Error
}

// InitAuthDB opens a separate SQLite database for user authentication
func InitAuthDB() error {
	if AuthDB != nil {
		// Already initialized via PostgreSQL
		log.Println("AuthDB already initialized via PostgreSQL.")
		return nil
	}

	log.Println("DATABASE_URL not set, falling back to SQLite for AuthDB (assets/users.db)...")
	db, err := gorm.Open(sqlite.Open("assets/users.db"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect auth database: %w", err)
	}

	err = db.AutoMigrate(&AuthUser{}, &Verification{})
	if err != nil {
		return fmt.Errorf("failed to auto migrate auth schema: %w", err)
	}

	AuthDB = db
	log.Println("Auth database (users.db) connection established and migrated.")
	return nil
}
