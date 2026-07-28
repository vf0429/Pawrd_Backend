package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type shopFulfillmentJobBeforeDispatchMarker struct {
	ID              string `gorm:"primaryKey;size:36"`
	PaymentIntentID string `gorm:"uniqueIndex;size:255;not null"`
	Payload         string `gorm:"type:text;not null"`
	Status          string `gorm:"index;size:24;not null"`
	Attempts        int    `gorm:"not null;default:0"`
	NextAttemptAt   time.Time
	LockedUntil     *time.Time
	LeaseOwner      string `gorm:"index;size:36"`
	LastError       string `gorm:"size:1000"`
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (shopFulfillmentJobBeforeDispatchMarker) TableName() string {
	return "shop_fulfillment_jobs"
}

type shopRefundBeforeFirstSubmissionMarker struct {
	ID                 string  `gorm:"primaryKey;size:36"`
	OrderID            string  `gorm:"index;size:36;not null"`
	PaymentIntentID    string  `gorm:"index;size:255;not null"`
	StripeRefundID     *string `gorm:"uniqueIndex;size:255"`
	IdempotencyKey     string  `gorm:"uniqueIndex;size:255;not null"`
	AmountMinor        int64   `gorm:"not null"`
	Currency           string  `gorm:"size:8;not null"`
	Reason             string  `gorm:"size:64;not null"`
	Status             string  `gorm:"index;size:24;not null"`
	StripeStatus       string  `gorm:"size:32"`
	StripeEventCreated int64   `gorm:"not null;default:0"`
	FailureReason      string  `gorm:"size:1000"`
	RequestedBy        string  `gorm:"size:64;not null"`
	CompletedAt        *time.Time

	ShopifyMirrorStatus        string  `gorm:"index;size:24"`
	ShopifyRefundID            *string `gorm:"uniqueIndex;size:255"`
	ShopifyRefundTransactionID string  `gorm:"size:255"`
	ShopifyMirrorError         string  `gorm:"size:1000"`
	ShopifyMirroredAt          *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

func (shopRefundBeforeFirstSubmissionMarker) TableName() string {
	return "shop_refunds"
}

func TestAutoMigrateAddsP0ExternalDispatchSafetyColumns(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&shopFulfillmentJobBeforeDispatchMarker{},
		&shopRefundBeforeFirstSubmissionMarker{},
	); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn(
		&ShopFulfillmentJob{},
		"DispatchStartedAt",
	) {
		t.Fatal("legacy fulfillment schema unexpectedly has dispatch_started_at")
	}
	if db.Migrator().HasColumn(
		&ShopRefund{},
		"StripeFirstSubmittedAt",
	) {
		t.Fatal("legacy refund schema unexpectedly has stripe_first_submitted_at")
	}

	if err := db.AutoMigrate(&ShopFulfillmentJob{}, &ShopRefund{}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(
		&ShopFulfillmentJob{},
		"DispatchStartedAt",
	) {
		t.Fatal("AutoMigrate did not add dispatch_started_at")
	}
	if !db.Migrator().HasColumn(
		&ShopRefund{},
		"StripeFirstSubmittedAt",
	) {
		t.Fatal("AutoMigrate did not add stripe_first_submitted_at")
	}
}
