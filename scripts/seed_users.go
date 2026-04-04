package main

import (
	"fmt"
	"log"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func main() {
	db, err := gorm.Open(sqlite.Open("assets/users.db"), &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to open users.db: %v", err)
	}

	err = db.AutoMigrate(&models.AuthUser{})
	if err != nil {
		log.Fatalf("Failed to migrate: %v", err)
	}

	// Define 3 test accounts
	testUsers := []struct {
		Email    string
		Phone    string
		Password string
		Name     string
	}{
		{"test1@pawrd.com", "+85291111111", "password123", "Test User 1"},
		{"test2@pawrd.com", "+85292222222", "password123", "Test User 2"},
		{"test3@pawrd.com", "+85293333333", "password123", "Test User 3"},
	}

	for _, tu := range testUsers {
		hash, err := bcrypt.GenerateFromPassword([]byte(tu.Password), bcrypt.DefaultCost)
		if err != nil {
			log.Fatalf("Failed to hash password for %s: %v", tu.Email, err)
		}

		user := models.AuthUser{
			Email:        tu.Email,
			Phone:        tu.Phone,
			PasswordHash: string(hash),
			Name:         tu.Name,
		}

		// Upsert: skip if email already exists
		var existing models.AuthUser
		result := db.Where("email = ?", tu.Email).First(&existing)
		if result.Error != nil {
			// Not found, create
			db.Create(&user)
			fmt.Printf("Created user: %s (%s)\n", tu.Email, tu.Phone)
		} else {
			fmt.Printf("User already exists: %s (skipped)\n", tu.Email)
		}
	}

	fmt.Println("\n=== Seed Complete ===")
	fmt.Println("Database: assets/users.db")
	fmt.Println("Test accounts:")
	fmt.Println("  test1@pawrd.com / +85291111111 / password123")
	fmt.Println("  test2@pawrd.com / +85292222222 / password123")
	fmt.Println("  test3@pawrd.com / +85293333333 / password123")
}
