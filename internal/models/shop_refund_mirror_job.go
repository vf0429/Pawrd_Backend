package models

import "time"

const (
	ShopRefundMirrorJobPending    = "pending"
	ShopRefundMirrorJobProcessing = "processing"
	ShopRefundMirrorJobRetrying   = "retrying"
	ShopRefundMirrorJobCompleted  = "completed"
	ShopRefundMirrorJobFailed     = "failed"
)

// ShopRefundMirrorJob is the durable, idempotent hand-off from Stripe's
// authoritative refund state to Shopify's operational order ledger.
type ShopRefundMirrorJob struct {
	ID            string `gorm:"primaryKey;size:36"`
	RefundID      string `gorm:"uniqueIndex;size:36;not null"`
	Status        string `gorm:"index;size:24;not null"`
	Attempts      int    `gorm:"not null;default:0"`
	NextAttemptAt time.Time
	LockedUntil   *time.Time
	LeaseOwner    string `gorm:"index;size:36"`
	LastError     string `gorm:"size:1000"`
	CompletedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
