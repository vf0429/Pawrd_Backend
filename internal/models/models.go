package models

import (
	"database/sql"
	"encoding/json"
	"time"
)

// --- Custom Types ---

type NullJsonString struct {
	sql.NullString
}

func (ns NullJsonString) MarshalJSON() ([]byte, error) {
	if !ns.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(ns.String)
}

// --- Models ---

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"` // "developer", "user"
}

type BlogPost struct {
	ID           string    `json:"id"`
	AuthorName   string    `json:"authorName"`
	AuthorAvatar string    `json:"authorAvatar"`
	Title        string    `json:"title"`
	Content      string    `json:"content"`
	ImageColor   string    `json:"imageColor"`
	Likes        int       `json:"likes"`
	Timestamp    time.Time `json:"timestamp"`
	ImageUrls    []string  `json:"imageUrls,omitempty"` // attached image URLs
}

type Clinic struct {
	ClinicID       string `json:"clinic_id"`
	Name           string `json:"name"`
	Address        string `json:"address"`
	PhoneRegular   string `json:"phone_regular"`
	PhoneEmergency string `json:"phone_emergency"`
	Whatsapp       string `json:"whatsapp"`
	OpeningHours   string `json:"opening_hours"`
	Emergency24h   string `json:"emergency_24h"`
	WebsiteURL     string `json:"website_url"`
	ApplemapURL    string `json:"applemap_url"`
	Latitude       string `json:"latitude"`
	Longitude      string `json:"longitude"`
	Rating          string `json:"rating"`
	PhotoURL        string `json:"photo_url"`
	GooglePlaceID   string `json:"google_place_id"`
	PhotoReference  string `json:"photo_reference"`
}

// --- Insurance Models ---

type InsuranceCompany struct {
	CompanyId     int            `json:"company_id"`
	CompanyName   string         `json:"company_name"`
	CompanyNameZh NullJsonString `json:"company_name_zh,omitempty"`
	CompanyLogo   NullJsonString `json:"company_logo,omitempty"`
}

type InsuranceProduct struct {
	InsuranceId       int            `json:"insurance_id"`
	ProviderId        int            `json:"provider_id"`
	InsuranceName     string         `json:"insurance_name"`
	InsuranceNameZh   NullJsonString `json:"insurance_name_zh,omitempty"`
	Remark            NullJsonString `json:"remark,omitempty"`
	RemarkZh          NullJsonString `json:"remark_zh,omitempty"`
	MinAge            NullJsonString `json:"min_age,omitempty"`
	MinAgeZh          NullJsonString `json:"min_age_zh,omitempty"`
	MaxAge            NullJsonString `json:"max_age,omitempty"`
	MaxAgeZh          NullJsonString `json:"max_age_zh,omitempty"`
	Coinsurance       NullJsonString `json:"coinsurance,omitempty"`
	CoinsuranceZh     NullJsonString `json:"coinsurance_zh,omitempty"`
	SuitablePetType   NullJsonString `json:"suitable_pet_type,omitempty"`
	SuitablePetTypeZh NullJsonString `json:"suitable_pet_type_zh,omitempty"`
	CatBreedType      NullJsonString `json:"cat_breed_type,omitempty"`
	CatBreedTypeZh    NullJsonString `json:"cat_breed_type_zh,omitempty"`
	DogBreedType      NullJsonString `json:"dog_breed_type,omitempty"`
	DogBreedTypeZh    NullJsonString `json:"dog_breed_type_zh,omitempty"`
	BreedTypeRemark   NullJsonString `json:"breed_type_remark,omitempty"`
	BreedTypeRemarkZh NullJsonString `json:"breed_type_remark_zh,omitempty"`
	PaymentMode       NullJsonString `json:"payment_mode,omitempty"`
	PaymentModeZh     NullJsonString `json:"payment_mode_zh,omitempty"`
	WaitingPeriod     NullJsonString `json:"waiting_period,omitempty"`
	WaitingPeriodZh   NullJsonString `json:"waiting_period_zh,omitempty"`
	InformationLink   NullJsonString `json:"information_link,omitempty"`
	InformationLinkZh NullJsonString `json:"information_link_zh,omitempty"`
	UpdateTime        NullJsonString `json:"update_time,omitempty"`
	Tag               NullJsonString `json:"tag,omitempty"`
	TagZh             NullJsonString `json:"tag_zh,omitempty"`
}

type CoverageItem struct {
	CoverageId     int            `json:"coverage_id"`
	CoverageType   string         `json:"coverage_type"`
	CoverageTypeZh NullJsonString `json:"coverage_type_zh,omitempty"`
}

type CoverageLimit struct {
	CoverageId    int            `json:"coverage_id"`
	ProductId     int            `json:"product_id"`
	CoverageLimit NullJsonString `json:"coverage_limit,omitempty"`
	Remark        NullJsonString `json:"remark,omitempty"`
	RemarkZh      NullJsonString `json:"remark_zh,omitempty"`
}

type SubCoverageLimit struct {
	SubCoverageId       int            `json:"sub_coverage_id"`
	ParentCoverageId    int            `json:"parent_coverage_id"`
	ProductId           int            `json:"product_id"`
	SubCoverageName     NullJsonString `json:"sub_coverage_name,omitempty"`
	SubCoverageNameZh   NullJsonString `json:"sub_coverage_name_zh,omitempty"`
	SubLimit            NullJsonString `json:"sub_limit,omitempty"`
	SubCoverageRemark   NullJsonString `json:"sub_coverage_remark,omitempty"`
	SubCoverageRemarkZh NullJsonString `json:"sub_coverage_remark_zh,omitempty"`
}

type ServiceSubcategory struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	DisplayOrder int    `json:"display_order"`
}

// --- Legacy Models (from old models/models.go) ---

type PetInsuranceComparison struct {
	ID                            int             `json:"id" db:"id"`
	InsuranceProvider             string          `json:"insurance_provider" db:"insurance_provider"`
	ProviderKey                   string          `json:"provider_key" db:"provider_key"`
	Category                      string          `json:"category" db:"category"`
	Subcategory                   string          `json:"subcategory" db:"subcategory"`
	CoveragePercentage            string          `json:"coverage_percentage" db:"coverage_percentage"`
	CancerCash                    sql.NullFloat64 `json:"cancer_cash" db:"cancer_cash"`
	CancerCashNotes               sql.NullString  `json:"cancer_cash_notes" db:"cancer_cash_notes"`
	AdditionalCriticalCashBenefit sql.NullFloat64 `json:"additional_critical_cash_benefit" db:"additional_critical_cash_benefit"`
}

type LegacyCoverageLimit struct {
	ID                int            `json:"id" db:"id"`
	LimitItem         string         `json:"limit_item" db:"limit_item"`
	ProviderKey       string         `json:"provider_key" db:"provider_key"`
	Category          string         `json:"category" db:"category"`
	Subcategory       string         `json:"subcategory" db:"subcategory"`
	Level             string         `json:"level" db:"level"`
	CoverageAmountHKD sql.NullString `json:"coverage_amount_hkd" db:"coverage_amount_hkd"`
	Notes             sql.NullString `json:"notes" db:"notes"`
}
