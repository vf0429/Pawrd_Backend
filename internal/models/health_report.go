package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ReviewStatus string

const (
	ReviewStatusAutoPass              ReviewStatus = "auto_pass"
	ReviewStatusPendingReview         ReviewStatus = "pending_review"
	ReviewStatusManualConfirmRequired ReviewStatus = "manual_confirm_required"
)

type HealthReport struct {
	ID                string                   `gorm:"type:text;primary_key" json:"id"`
	PetID             string                   `gorm:"type:text;not null;index" json:"pet_id"`
	ReportType        string                   `gorm:"type:text;default:'other';index" json:"report_type"`
	ClinicName        string                   `gorm:"type:text;default:''" json:"clinic_name"`
	ReportDate        time.Time                `gorm:"index" json:"report_date"`
	SourceImageCount  int                      `gorm:"default:0" json:"source_image_count"`
	RawPayloadJSON    string                   `gorm:"type:text;default:'{}'" json:"raw_payload_json"`
	SchemaVersion     string                   `gorm:"type:text;default:'v1'" json:"schema_version"`
	FusionVersion     string                   `gorm:"type:text;default:'v1'" json:"fusion_version"`
	OverallConfidence float64                  `gorm:"type:real;default:0" json:"overall_confidence"`
	ConsensusScore    float64                  `gorm:"type:real;default:0" json:"consensus_score"`
	FinalReviewStatus string                   `gorm:"type:text;default:'pending_review';index" json:"final_review_status"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	Observations      []ReportObservation      `gorm:"foreignKey:ReportID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"observations,omitempty"`
	VendorExtractions []ReportVendorExtraction `gorm:"foreignKey:ReportID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"vendor_extractions,omitempty"`
}

func (h *HealthReport) BeforeCreate(tx *gorm.DB) error {
	if h.ID == "" {
		h.ID = uuid.New().String()
	}
	return nil
}

type ReportObservation struct {
	ID                  string    `gorm:"type:text;primary_key" json:"id"`
	ReportID            string    `gorm:"type:text;not null;index" json:"report_id"`
	MetricKeyRaw        string    `gorm:"type:text;not null;index" json:"metric_key_raw"`
	MetricID            string    `gorm:"type:text;default:'';index" json:"metric_id"`
	ValueNumber         *float64  `gorm:"type:real" json:"value_number,omitempty"`
	ValueText           string    `gorm:"type:text;default:''" json:"value_text,omitempty"`
	Unit                string    `gorm:"type:text;default:''" json:"unit,omitempty"`
	Flag                string    `gorm:"type:text;default:''" json:"flag,omitempty"`
	Confidence          float64   `gorm:"type:real;default:0" json:"confidence"`
	ConsensusScore      float64   `gorm:"type:real;default:0" json:"consensus_score"`
	ReviewStatus        string    `gorm:"type:text;default:'pending_review';index" json:"review_status"`
	IsVerified          bool      `gorm:"default:false;index" json:"is_verified"`
	SourcePage          int       `gorm:"default:0" json:"source_page,omitempty"`
	SourceLine          string    `gorm:"type:text;default:''" json:"source_line,omitempty"`
	SourceBBoxJSON      string    `gorm:"type:text;default:''" json:"source_bbox_json,omitempty"`
	ContributingVendors string    `gorm:"type:text;default:''" json:"contributing_vendors,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (o *ReportObservation) BeforeCreate(tx *gorm.DB) error {
	if o.ID == "" {
		o.ID = uuid.New().String()
	}
	return nil
}

type ReportVendorExtraction struct {
	ID              string    `gorm:"type:text;primary_key" json:"id"`
	ReportID        string    `gorm:"type:text;not null;index" json:"report_id"`
	VendorID        string    `gorm:"type:text;not null;index" json:"vendor_id"`
	Model           string    `gorm:"type:text;default:''" json:"model"`
	LatencyMS       int64     `gorm:"default:0" json:"latency_ms"`
	RawResponseJSON string    `gorm:"type:text;default:'{}'" json:"raw_response_json"`
	FieldCount      int       `gorm:"default:0" json:"field_count"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (v *ReportVendorExtraction) BeforeCreate(tx *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	return nil
}
