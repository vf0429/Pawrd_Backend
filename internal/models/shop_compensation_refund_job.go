package models

import "time"

const (
	ShopCompensationRefundJobPending    = "pending"
	ShopCompensationRefundJobProcessing = "processing"
	ShopCompensationRefundJobRetrying   = "retrying"
	ShopCompensationRefundJobCompleted  = "completed"
	ShopCompensationRefundJobFailed     = "failed"
)

// ShopCompensationRefundJob is the durable Stripe execution job for a
// system-created ShopRefund. It is separate from ShopRefundMirrorJob: this job
// moves money in Stripe, while the mirror job records an already-completed
// refund in Shopify.
type ShopCompensationRefundJob struct {
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
