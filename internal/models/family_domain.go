package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Family struct {
	ID          string         `gorm:"type:text;primaryKey" json:"id"`
	OwnerUserID string         `gorm:"type:text;not null;index" json:"owner_user_id"`
	DisplayName string         `gorm:"type:text;not null" json:"display_name"`
	Handle      string         `gorm:"type:text;uniqueIndex;not null" json:"handle"`
	AvatarURL   string         `gorm:"type:text;default:''" json:"avatar_url"`
	Bio         string         `gorm:"type:text;default:''" json:"bio"`
	City        string         `gorm:"type:text;default:''" json:"city"`
	IsPublic    bool           `gorm:"default:true" json:"is_public"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Members     []FamilyMember `gorm:"foreignKey:FamilyID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"members,omitempty"`
	Pets        []Pet          `gorm:"foreignKey:FamilyID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"pets,omitempty"`
	Follows     []FamilyFollow `gorm:"foreignKey:FamilyID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"follows,omitempty"`
}

func (f *Family) BeforeCreate(tx *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	return nil
}

type FamilyMember struct {
	ID           string    `gorm:"type:text;primaryKey" json:"id"`
	FamilyID     string    `gorm:"type:text;not null;index" json:"family_id"`
	UserID       string    `gorm:"type:text;not null;index:idx_family_member_user,unique" json:"user_id"`
	DisplayName  string    `gorm:"type:text;not null" json:"display_name"`
	Role         string    `gorm:"type:text;not null;default:'owner'" json:"role"`
	Relationship string    `gorm:"type:text;default:''" json:"relationship"`
	IsPrimary    bool      `gorm:"default:false" json:"is_primary"`
	JoinedAt     time.Time `json:"joined_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (m *FamilyMember) BeforeCreate(tx *gorm.DB) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if m.JoinedAt.IsZero() {
		m.JoinedAt = time.Now().UTC()
	}
	return nil
}

type Pet struct {
	ID                   string               `gorm:"type:text;primaryKey" json:"id"`
	FamilyID             string               `gorm:"type:text;not null;index" json:"family_id"`
	Name                 string               `gorm:"type:text;not null" json:"name"`
	Species              string               `gorm:"type:text;not null" json:"species"`
	Breed                string               `gorm:"type:text;default:''" json:"breed"`
	Sex                  string               `gorm:"type:text;default:''" json:"sex"`
	BirthDate            *time.Time           `json:"birth_date,omitempty"`
	AvatarURL            string               `gorm:"type:text;default:''" json:"avatar_url"`
	MicrochipID          string               `gorm:"type:text;default:''" json:"-"`
	PrivateNotes         string               `gorm:"type:text;default:''" json:"-"`
	CurrentWeightKg      *float64             `json:"-"`
	LastVaccinationAt    *time.Time           `json:"-"`
	NextVaccinationDueAt *time.Time           `json:"-"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
	PublicProfile        PetPublicProfile     `gorm:"foreignKey:PetID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"public_profile,omitempty"`
	VisibilitySettings   PetVisibilitySetting `gorm:"foreignKey:PetID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"visibility_settings,omitempty"`
	DerivedSummary       PetDerivedSummary    `gorm:"foreignKey:PetID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"derived_summary,omitempty"`
}

func (p *Pet) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

type PetPublicProfile struct {
	ID                 string    `gorm:"type:text;primaryKey" json:"id"`
	PetID              string    `gorm:"type:text;not null;uniqueIndex" json:"pet_id"`
	DisplayName        string    `gorm:"type:text;not null" json:"display_name"`
	Slug               string    `gorm:"type:text;uniqueIndex;not null" json:"slug"`
	Headline           string    `gorm:"type:text;default:''" json:"headline"`
	Bio                string    `gorm:"type:text;default:''" json:"bio"`
	AvatarURL          string    `gorm:"type:text;default:''" json:"avatar_url"`
	FeaturedTraitsJSON string    `gorm:"type:text;not null;default:'[]'" json:"featured_traits_json"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (p *PetPublicProfile) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

type PetVisibilitySetting struct {
	ID                 string    `gorm:"type:text;primaryKey" json:"id"`
	PetID              string    `gorm:"type:text;not null;uniqueIndex" json:"pet_id"`
	ShowBreed          bool      `gorm:"default:true" json:"show_breed"`
	ShowAge            bool      `gorm:"default:true" json:"show_age"`
	ShowLatestWeight   bool      `gorm:"default:false" json:"show_latest_weight"`
	ShowVaccineStatus  bool      `gorm:"default:false" json:"show_vaccine_status"`
	ShowFamilyLink     bool      `gorm:"default:true" json:"show_family_link"`
	ShowMedicalSummary bool      `gorm:"default:false" json:"show_medical_summary"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func (p *PetVisibilitySetting) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	return nil
}

type PostPetTag struct {
	ID        string    `gorm:"type:text;primaryKey" json:"id"`
	PostID    string    `gorm:"type:text;not null;index:idx_post_pet_tag_pair,unique" json:"post_id"`
	PetID     string    `gorm:"type:text;not null;index:idx_post_pet_tag_pair,unique;index" json:"pet_id"`
	IsPrimary bool      `gorm:"default:false" json:"is_primary"`
	CreatedAt time.Time `json:"created_at"`
}

func (t *PostPetTag) BeforeCreate(tx *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	return nil
}

type FamilyFollow struct {
	ID             string    `gorm:"type:text;primaryKey" json:"id"`
	FamilyID       string    `gorm:"type:text;not null;index:idx_family_follow_pair,unique" json:"family_id"`
	FollowerUserID string    `gorm:"type:text;not null;index:idx_family_follow_pair,unique" json:"follower_user_id"`
	CreatedAt      time.Time `json:"created_at"`
}

func (f *FamilyFollow) BeforeCreate(tx *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	return nil
}

type PetDerivedSummary struct {
	ID                   string     `gorm:"type:text;primaryKey" json:"id"`
	PetID                string     `gorm:"type:text;not null;uniqueIndex" json:"pet_id"`
	DisplayAge           string     `gorm:"type:text;default:''" json:"display_age"`
	AgeYears             *int       `json:"age_years,omitempty"`
	LatestWeightKg       *float64   `json:"latest_weight_kg,omitempty"`
	VaccineStatus        string     `gorm:"type:text;default:'unknown'" json:"vaccine_status"`
	LastVaccinationAt    *time.Time `json:"last_vaccination_at,omitempty"`
	NextVaccinationDueAt *time.Time `json:"next_vaccination_due_at,omitempty"`
	ComputedAt           time.Time  `json:"computed_at"`
	SourceUpdatedAt      *time.Time `json:"source_updated_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (p *PetDerivedSummary) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.ComputedAt.IsZero() {
		p.ComputedAt = time.Now().UTC()
	}
	return nil
}

func BuildPetDerivedSummary(p Pet, now time.Time) PetDerivedSummary {
	summary := PetDerivedSummary{
		PetID:                p.ID,
		LatestWeightKg:       p.CurrentWeightKg,
		LastVaccinationAt:    p.LastVaccinationAt,
		NextVaccinationDueAt: p.NextVaccinationDueAt,
		ComputedAt:           now.UTC(),
		SourceUpdatedAt:      &p.UpdatedAt,
		VaccineStatus:        "unknown",
	}

	if p.BirthDate != nil {
		years := ageYears(*p.BirthDate, now)
		summary.AgeYears = &years
		summary.DisplayAge = formatDisplayAge(years)
	}
	if p.NextVaccinationDueAt != nil {
		if p.NextVaccinationDueAt.Before(now) {
			summary.VaccineStatus = "overdue"
		} else {
			summary.VaccineStatus = "up_to_date"
		}
	} else if p.LastVaccinationAt != nil {
		summary.VaccineStatus = "recorded"
	}

	return summary
}

func ageYears(birthDate time.Time, now time.Time) int {
	years := now.Year() - birthDate.Year()
	if now.YearDay() < birthDate.YearDay() {
		years--
	}
	if years < 0 {
		return 0
	}
	return years
}

func formatDisplayAge(years int) string {
	if years == 1 {
		return "1 year"
	}
	return fmt.Sprintf("%d years", years)
}
