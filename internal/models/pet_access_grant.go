package models

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PetAccessGrant struct {
	ID                   string     `gorm:"type:text;primaryKey" json:"id"`
	PetID                string     `gorm:"type:text;not null;index" json:"pet_id"`
	OwnerUserID          string     `gorm:"type:text;not null;index" json:"owner_user_id"`
	RecipientUserID      string     `gorm:"type:text;index" json:"recipient_user_id,omitempty"`
	RecipientDisplayName string     `gorm:"type:text;default:''" json:"recipient_display_name,omitempty"`
	RecipientKind        string     `gorm:"type:text;default:'';index" json:"recipient_kind,omitempty"`
	Scenario             string     `gorm:"type:text;not null;index" json:"scenario"`
	DeliveryKind         string     `gorm:"type:text;not null;index" json:"delivery_kind"`
	ScopesJSON           string     `gorm:"type:text;not null;default:'[]'" json:"-"`
	AllowDownload        bool       `gorm:"default:false" json:"allow_download"`
	StartsAt             time.Time  `gorm:"not null;index" json:"starts_at"`
	ExpiresAt            *time.Time `gorm:"index" json:"expires_at,omitempty"`
	RevokedAt            *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	TokenHash            string     `gorm:"type:text;uniqueIndex;not null" json:"-"`
	LastAccessedAt       *time.Time `gorm:"index" json:"last_accessed_at,omitempty"`
	Note                 string     `gorm:"type:text;default:''" json:"note,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (g *PetAccessGrant) BeforeCreate(tx *gorm.DB) error {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	return nil
}

func (g PetAccessGrant) EffectiveStatus(now time.Time) string {
	if g.RevokedAt != nil {
		return "revoked"
	}
	if now.Before(g.StartsAt) {
		return "scheduled"
	}
	if g.ExpiresAt != nil && !now.Before(*g.ExpiresAt) {
		return "expired"
	}
	return "active"
}

func HashShareToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
