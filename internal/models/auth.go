package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuthUser is the authentication user stored in users.db
type AuthUser struct {
	ID           uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Email        string    `json:"email" gorm:"uniqueIndex;not null"`
	Phone        string    `json:"phone" gorm:"uniqueIndex;not null"`
	PasswordHash string    `json:"-" gorm:"not null"`
	Name         string    `json:"name" gorm:"not null"`
	AvatarURL    string    `json:"avatar_url" gorm:"default:''"`
	CreatedAt    time.Time `json:"created_at"`
}

// AuthUserResponse is the safe public representation sent to clients (no password hash).
type AuthUserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url"`
	CreatedAt time.Time `json:"created_at"`
}

// ToResponse converts AuthUser to the API-safe response struct.
func (u *AuthUser) ToResponse() AuthUserResponse {
	return AuthUserResponse{
		ID:        fmt.Sprintf("%d", u.ID),
		Email:     u.Email,
		Name:      u.Name,
		AvatarURL: u.AvatarURL,
		CreatedAt: u.CreatedAt,
	}
}

// AuthTokenResponse is returned from login and register endpoints.
type AuthTokenResponse struct {
	Token string           `json:"token"`
	User  AuthUserResponse `json:"user"`
}

// Verification stores OTP codes for email/phone verification (dev-only provider bypass).
type Verification struct {
	ID        string    `json:"id" gorm:"type:text;primaryKey"`
	UserID    string    `json:"user_id" gorm:"index"`
	Contact   string    `json:"contact" gorm:"not null"`
	Method    string    `json:"method" gorm:"not null"` // email | phone
	Code      string    `json:"code" gorm:"not null"`
	Purpose   string    `json:"purpose" gorm:"not null"` // new_account | change_email | change_phone
	Verified  bool      `json:"verified" gorm:"default:false"`
	ExpiresAt time.Time `json:"expires_at" gorm:"not null"`
	CreatedAt time.Time `json:"created_at"`
}

func (v *Verification) BeforeCreate(tx *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	return nil
}

// AuthDB holds a reference to the users database
var AuthDB *gorm.DB
