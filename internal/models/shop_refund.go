package models

import "time"

const (
	ShopRefundStatusPending   = "pending"
	ShopRefundStatusSucceeded = "succeeded"
	ShopRefundStatusFailed    = "failed"

	ShopRefundReasonQuoteExpired      = "system_quote_expired"
	ShopRefundReasonFulfillmentFailed = "system_fulfillment_failed"

	ShopRefundMirrorStatusPending       = "pending"
	ShopRefundMirrorStatusRetrying      = "retrying"
	ShopRefundMirrorStatusSucceeded     = "succeeded"
	ShopRefundMirrorStatusFailed        = "failed"
	ShopRefundMirrorStatusNotApplicable = "not_applicable"
)

// ShopRefund is Pawrd's durable audit record for a Stripe refund. A pending
// record reserves its amount before Stripe is called, which prevents another
// operator request from refunding the same order beyond its paid total.
type ShopRefund struct {
	ID                     string  `gorm:"primaryKey;size:36"`
	OrderID                string  `gorm:"index;size:36;not null"`
	PaymentIntentID        string  `gorm:"index;size:255;not null"`
	StripeRefundID         *string `gorm:"uniqueIndex;size:255"`
	IdempotencyKey         string  `gorm:"uniqueIndex;size:255;not null"`
	AmountMinor            int64   `gorm:"not null"`
	Currency               string  `gorm:"size:8;not null"`
	Reason                 string  `gorm:"size:64;not null"`
	Status                 string  `gorm:"index;size:24;not null"`
	StripeStatus           string  `gorm:"size:32"`
	StripeEventCreated     int64   `gorm:"not null;default:0"`
	StripeFirstSubmittedAt *time.Time
	FailureReason          string `gorm:"size:1000"`
	RequestedBy            string `gorm:"size:64;not null"`
	CompletedAt            *time.Time
	// Shopify is only an operational mirror. These fields describe recording
	// the already-succeeded Stripe refund as an external Shopify transaction;
	// they never authorize a second movement of money or inventory restock.
	ShopifyMirrorStatus        string  `gorm:"index;size:24"`
	ShopifyRefundID            *string `gorm:"uniqueIndex;size:255"`
	ShopifyRefundTransactionID string  `gorm:"size:255"`
	ShopifyMirrorError         string  `gorm:"size:1000"`
	ShopifyMirroredAt          *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}
