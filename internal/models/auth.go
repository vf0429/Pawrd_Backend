package models

import (
	"fmt"
	"time"

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

// AuthDB holds a reference to the users database
var AuthDB *gorm.DB
