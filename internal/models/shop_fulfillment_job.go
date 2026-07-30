package models

import "time"

const (
	ShopFulfillmentJobPending    = "pending"
	ShopFulfillmentJobProcessing = "processing"
	ShopFulfillmentJobRetrying   = "retrying"
	ShopFulfillmentJobCompleted  = "completed"
	ShopFulfillmentJobCanceled   = "canceled"
	ShopFulfillmentJobFailed     = "failed"
)

// ShopFulfillmentJob is the durable hand-off between Stripe's authoritative
// payment webhook and the external Shopify order creation call. Keeping this
// work in the database lets the webhook acknowledge a paid order as soon as it
// is safely queued, while a worker retries transient Shopify failures.
type ShopFulfillmentJob struct {
	ID                string `gorm:"primaryKey;size:36"`
	PaymentIntentID   string `gorm:"uniqueIndex;size:255;not null"`
	Payload           string `gorm:"type:text;not null"`
	Status            string `gorm:"index;size:24;not null"`
	Attempts          int    `gorm:"not null;default:0"`
	NextAttemptAt     time.Time
	LockedUntil       *time.Time
	LeaseOwner        string `gorm:"index;size:36"`
	LastError         string `gorm:"size:1000"`
	DispatchStartedAt *time.Time
	CompletedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
