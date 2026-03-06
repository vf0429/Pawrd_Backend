package models

import (
	"fmt"
	"log"

	"github.com/vf0429/Petwell_Backend/internal/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func InitDB(cfg *config.Config) (*gorm.DB, error) {
	// Use SQLite instead of Postgres
	db, err := gorm.Open(sqlite.Open("assets/pet_case.db"), &gorm.Config{})
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
	)
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate schema: %w", err)
	}

	log.Println("Database connection established and models migrated.")
	return db, nil
}

// InitAuthDB opens a separate SQLite database for user authentication
func InitAuthDB() error {
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
