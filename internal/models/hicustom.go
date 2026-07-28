package models

import "time"

// BlankProduct is a cached HiCustom blank (customizable base) product. Synced
// from HiCustom's "空白产品列表" endpoint and stored locally so the catalog can
// list customizable products without a live round-trip on every request.
// See docs/hicustom_integration_design.md §10.2, §13.4.
type BlankProduct struct {
	ID           string    `gorm:"primaryKey" json:"id"`
	SKU          string    `gorm:"uniqueIndex" json:"sku"` // HiCustom product SKU
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Category     string    `json:"category"`
	PrintArea    string    `json:"printArea,omitempty"`    // JSON blob of print-area config
	Price        string    `json:"price"`                  // factory settlement price
	CurrencyCode string    `json:"currencyCode"`
	CoverURL     string    `json:"coverUrl"`
	ImageURL     string    `json:"imageUrl,omitempty"`
	Available    bool      `json:"available"`
	SyncedAt     time.Time `json:"syncedAt"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CustomDesign persists the result of a user's design session in the HiCustom
// designer SDK. The SDK callback returns a customProductId + preview; we store
// it so checkout can reference the design and fulfillment can push it to HiCustom.
type CustomDesign struct {
	ID              string    `gorm:"primaryKey" json:"id"`
	UserID          string    `gorm:"index" json:"userId"`
	BlankProductSKU string    `gorm:"index" json:"blankProductSku"`
	CustomProductID string    `json:"customProductId"` // SDK callback result
	PreviewURL      string    `json:"previewUrl"`
	Snapshot        string    `json:"snapshot,omitempty"` // JSON blob of design parameters
	CreatedAt       time.Time `json:"createdAt"`
}

// HiCustomOrder tracks the push state of a paid order to the HiCustom factory.
// Created by the Stripe webhook's hicustom fulfillment branch (Phase C wires the
// real push; Phase B reserved the branch).
type HiCustomOrder struct {
	ID              string    `gorm:"primaryKey" json:"id"`
	PawrdOrderID    string    `gorm:"uniqueIndex" json:"pawrdOrderId"`
	HiCustomOrderID string    `json:"hiCustomOrderId,omitempty"`
	PaymentIntentID string    `gorm:"index" json:"paymentIntentId"`
	Status          string    `json:"status"` // pending/producing/shipped/done/failed
	LogisticsNo     string    `json:"logisticsNo,omitempty"`
	IdempotencyKey  string    `gorm:"uniqueIndex" json:"idempotencyKey"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
