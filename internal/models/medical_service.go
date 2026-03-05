package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MedicalService stores content for each medical service category.
// Content is kept in ContentJSON (flexible JSON blob) so different service
// types can have different data structures without schema migrations.
// Partners update their data via the admin API using an X-Admin-Key.
type MedicalService struct {
	ID          string    `gorm:"type:text;primary_key" json:"id"`
	Category    string    `gorm:"type:text;not null;uniqueIndex" json:"category"` // e.g. "deworming"
	Name        string    `gorm:"type:text;not null" json:"name"`
	NameZh      string    `gorm:"type:text;default:''" json:"name_zh"`
	Icon        string    `gorm:"type:text;default:'cross.case.fill'" json:"icon"`       // SF Symbol
	ColorHex    string    `gorm:"type:text;default:'#007AFF'" json:"color_hex"`          // accent color
	Description string    `gorm:"type:text;default:''" json:"description"`
	DescZh      string    `gorm:"type:text;default:''" json:"desc_zh"`
	ContentJSON string    `gorm:"type:text;default:'{}'" json:"content_json"` // flexible structured data
	Provider    string    `gorm:"type:text;default:''" json:"provider"`       // partner clinic / brand
	Contact     string    `gorm:"type:text;default:''" json:"contact"`
	IsActive    bool      `gorm:"default:true" json:"is_active"`
	SortOrder   int       `gorm:"default:0" json:"sort_order"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func (m *MedicalService) BeforeCreate(tx *gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.New().String()
	}
	return nil
}

func (MedicalService) TableName() string {
	return "medical_services"
}
