package models

import (
	"fmt"
	"log"
	"os"

	"github.com/vf0429/Petwell_Backend/internal/config"
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
		&Post{},
		&PostImage{},
		&PostLike{},
		&MedicalService{},
		&Partner{},
		&HealthReport{},
		&ReportObservation{},
		&ReportVendorExtraction{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate schema: %w", err)
	}

	// If using PostgreSQL, migrate Auth schema to the same database
	if dsn != "" {
		log.Println("Migrating Auth schema to PostgreSQL database...")
		err = db.AutoMigrate(&AuthUser{})
		if err != nil {
			return nil, fmt.Errorf("failed to auto migrate auth schema: %w", err)
		}
		AuthDB = db
	}

	log.Println("Database connection established and models migrated.")
	return db, nil
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

	err = db.AutoMigrate(&AuthUser{})
	if err != nil {
		return fmt.Errorf("failed to auto migrate auth schema: %w", err)
	}

	AuthDB = db
	log.Println("Auth database (users.db) connection established and migrated.")
	return nil
}
