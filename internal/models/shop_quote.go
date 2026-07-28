package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const ShopQuoteSnapshotVersion = 1

const (
	ShopQuoteStatusDeliveryRequired = "delivery_selection_required"
	ShopQuoteStatusDiscountInvalid  = "discount_invalid"
	ShopQuoteStatusReady            = "ready"
	ShopQuoteStatusConsumed         = "consumed"
)

// ShopCheckoutQuote persists the exact Shopify Cart result that Pawrd offered
// to a user. SnapshotSHA256 detects accidental/corrupt modifications before a
// PaymentIntent consumes the quote.
type ShopCheckoutQuote struct {
	ID                           string `gorm:"primaryKey;size:36"`
	UserID                       string `gorm:"index;size:36;not null"`
	ShopifyCartID                string `gorm:"uniqueIndex;size:255;not null"`
	Status                       string `gorm:"index;size:40;not null"`
	Currency                     string `gorm:"size:8;not null"`
	SubtotalAmountMinor          int64  `gorm:"not null"`
	DiscountAmountMinor          int64  `gorm:"not null"`
	ShippingAmountMinor          int64  `gorm:"not null"`
	TaxAmountMinor               int64  `gorm:"not null"`
	TotalAmountMinor             int64  `gorm:"not null"`
	DiscountCode                 string `gorm:"size:255"`
	DiscountCodeApplicable       bool
	DeliveryGroupID              string    `gorm:"size:255"`
	SelectedDeliveryOptionHandle string    `gorm:"size:500"`
	SnapshotJSON                 string    `gorm:"type:text;not null"`
	SnapshotSHA256               string    `gorm:"size:64;not null"`
	ExpiresAt                    time.Time `gorm:"index;not null"`
	ConsumedAt                   *time.Time
	OrderID                      string `gorm:"index;size:36"`
	PaymentIntentID              string `gorm:"index;size:255"`
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type ShopQuoteSnapshot struct {
	Version                int                       `json:"version"`
	ShopifyCartID          string                    `json:"shopifyCartId"`
	ShopifyCartUpdatedAt   time.Time                 `json:"shopifyCartUpdatedAt"`
	UserID                 string                    `json:"userId"`
	Status                 string                    `json:"status"`
	Currency               string                    `json:"currency"`
	LineItems              []ShopQuoteSnapshotItem   `json:"lineItems"`
	DeliveryOptions        []ShopQuoteDeliveryOption `json:"deliveryOptions"`
	SelectedDeliveryOption *ShopQuoteDeliveryOption  `json:"selectedDeliveryOption,omitempty"`
	Discount               ShopQuoteDiscount         `json:"discount"`
	Amounts                ShopQuoteAmounts          `json:"amounts"`
	Customer               ShopQuoteCustomer         `json:"customer"`
	Shipping               ShopQuoteShipping         `json:"shipping"`
	Warnings               []string                  `json:"warnings,omitempty"`
	QuotedAt               time.Time                 `json:"quotedAt"`
	ExpiresAt              time.Time                 `json:"expiresAt"`
}

type ShopQuoteSnapshotItem struct {
	Source           string `json:"source"`
	Handle           string `json:"handle"`
	VariantID        string `json:"variantId"`
	Title            string `json:"title"`
	VariantTitle     string `json:"variantTitle"`
	ImageURL         string `json:"imageUrl"`
	Quantity         int    `json:"quantity"`
	UnitAmountMinor  int64  `json:"unitAmountMinor"`
	RequiresShipping bool   `json:"requiresShipping"`
}

type ShopQuoteDeliveryOption struct {
	DeliveryGroupID string `json:"deliveryGroupId"`
	Handle          string `json:"handle"`
	Code            string `json:"code"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	DeliveryMethod  string `json:"deliveryMethod"`
	AmountMinor     int64  `json:"amountMinor"`
	Currency        string `json:"currency"`
}

type ShopQuoteDiscount struct {
	Code       string `json:"code"`
	Applicable bool   `json:"applicable"`
	TargetType string `json:"targetType"`
}

type ShopQuoteAmounts struct {
	SubtotalAmountMinor int64 `json:"subtotal"`
	DiscountAmountMinor int64 `json:"discount"`
	ShippingAmountMinor int64 `json:"shipping"`
	TaxAmountMinor      int64 `json:"tax"`
	TotalAmountMinor    int64 `json:"total"`
}

type ShopQuoteCustomer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type ShopQuoteShipping struct {
	RecipientName string `json:"recipientName"`
	Phone         string `json:"phone"`
	Address1      string `json:"address1"`
	District      string `json:"district"`
	Region        string `json:"region"`
	CountryCode   string `json:"countryCode"`
}

func (q *ShopCheckoutQuote) SetSnapshot(snapshot ShopQuoteSnapshot) error {
	if snapshot.Version != ShopQuoteSnapshotVersion {
		return fmt.Errorf("unsupported shop quote snapshot version %d", snapshot.Version)
	}
	// PostgreSQL timestamptz persists microseconds, while Go time.Time can
	// carry nanoseconds. Canonicalize every sealed timestamp before hashing so
	// an immediate database round trip cannot invalidate the quote.
	snapshot.ShopifyCartUpdatedAt = canonicalShopQuoteTime(snapshot.ShopifyCartUpdatedAt)
	snapshot.QuotedAt = canonicalShopQuoteTime(snapshot.QuotedAt)
	snapshot.ExpiresAt = canonicalShopQuoteTime(snapshot.ExpiresAt)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode shop quote snapshot: %w", err)
	}
	sum := sha256.Sum256(raw)
	q.ShopifyCartID = strings.TrimSpace(snapshot.ShopifyCartID)
	q.UserID = strings.TrimSpace(snapshot.UserID)
	q.Status = strings.TrimSpace(snapshot.Status)
	q.Currency = strings.ToUpper(strings.TrimSpace(snapshot.Currency))
	q.SubtotalAmountMinor = snapshot.Amounts.SubtotalAmountMinor
	q.DiscountAmountMinor = snapshot.Amounts.DiscountAmountMinor
	q.ShippingAmountMinor = snapshot.Amounts.ShippingAmountMinor
	q.TaxAmountMinor = snapshot.Amounts.TaxAmountMinor
	q.TotalAmountMinor = snapshot.Amounts.TotalAmountMinor
	q.DiscountCode = strings.TrimSpace(snapshot.Discount.Code)
	q.DiscountCodeApplicable = snapshot.Discount.Applicable
	q.ExpiresAt = canonicalShopQuoteTime(snapshot.ExpiresAt)
	q.DeliveryGroupID = ""
	q.SelectedDeliveryOptionHandle = ""
	if snapshot.SelectedDeliveryOption != nil {
		q.DeliveryGroupID = strings.TrimSpace(snapshot.SelectedDeliveryOption.DeliveryGroupID)
		q.SelectedDeliveryOptionHandle = strings.TrimSpace(snapshot.SelectedDeliveryOption.Handle)
	}
	q.SnapshotJSON = string(raw)
	q.SnapshotSHA256 = hex.EncodeToString(sum[:])
	return q.Validate()
}

func (q ShopCheckoutQuote) DecodeAndVerifySnapshot() (ShopQuoteSnapshot, error) {
	var snapshot ShopQuoteSnapshot
	raw := []byte(q.SnapshotJSON)
	sum := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(q.SnapshotSHA256)) {
		return snapshot, fmt.Errorf("shop quote snapshot integrity check failed")
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, fmt.Errorf("decode shop quote snapshot: %w", err)
	}
	if snapshot.Version != ShopQuoteSnapshotVersion {
		return snapshot, fmt.Errorf("unsupported shop quote snapshot version %d", snapshot.Version)
	}
	snapshotStatus := strings.TrimSpace(snapshot.Status)
	recordStatus := strings.TrimSpace(q.Status)
	statusMatches := snapshotStatus == recordStatus
	if recordStatus == ShopQuoteStatusConsumed {
		statusMatches = q.ConsumedAt != nil && snapshotStatus == ShopQuoteStatusReady
	}
	deliveryGroupID := ""
	deliveryOptionHandle := ""
	if snapshot.SelectedDeliveryOption != nil {
		deliveryGroupID = strings.TrimSpace(snapshot.SelectedDeliveryOption.DeliveryGroupID)
		deliveryOptionHandle = strings.TrimSpace(snapshot.SelectedDeliveryOption.Handle)
	}
	if strings.TrimSpace(snapshot.ShopifyCartID) != strings.TrimSpace(q.ShopifyCartID) ||
		strings.TrimSpace(snapshot.UserID) != strings.TrimSpace(q.UserID) ||
		!statusMatches ||
		strings.ToUpper(strings.TrimSpace(snapshot.Currency)) != strings.ToUpper(strings.TrimSpace(q.Currency)) ||
		snapshot.Amounts.SubtotalAmountMinor != q.SubtotalAmountMinor ||
		snapshot.Amounts.DiscountAmountMinor != q.DiscountAmountMinor ||
		snapshot.Amounts.ShippingAmountMinor != q.ShippingAmountMinor ||
		snapshot.Amounts.TaxAmountMinor != q.TaxAmountMinor ||
		snapshot.Amounts.TotalAmountMinor != q.TotalAmountMinor ||
		strings.TrimSpace(snapshot.Discount.Code) != strings.TrimSpace(q.DiscountCode) ||
		snapshot.Discount.Applicable != q.DiscountCodeApplicable ||
		deliveryGroupID != strings.TrimSpace(q.DeliveryGroupID) ||
		deliveryOptionHandle != strings.TrimSpace(q.SelectedDeliveryOptionHandle) ||
		!canonicalShopQuoteTime(snapshot.ExpiresAt).Equal(canonicalShopQuoteTime(q.ExpiresAt)) {
		return snapshot, fmt.Errorf("shop quote snapshot does not match indexed fields")
	}
	return snapshot, nil
}

func canonicalShopQuoteTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func (q ShopCheckoutQuote) Validate() error {
	if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.UserID) == "" || strings.TrimSpace(q.ShopifyCartID) == "" {
		return fmt.Errorf("shop quote identity is incomplete")
	}
	if q.Currency != "HKD" {
		return fmt.Errorf("shop quote currency must be HKD")
	}
	switch q.Status {
	case ShopQuoteStatusDeliveryRequired, ShopQuoteStatusDiscountInvalid, ShopQuoteStatusReady, ShopQuoteStatusConsumed:
	default:
		return fmt.Errorf("shop quote status is invalid")
	}
	if q.SubtotalAmountMinor < 0 || q.DiscountAmountMinor < 0 || q.ShippingAmountMinor < 0 ||
		q.TaxAmountMinor < 0 || q.TotalAmountMinor <= 0 {
		return fmt.Errorf("shop quote contains invalid amounts")
	}
	if q.SubtotalAmountMinor-q.DiscountAmountMinor+q.ShippingAmountMinor+q.TaxAmountMinor != q.TotalAmountMinor {
		return fmt.Errorf("shop quote amounts do not reconcile")
	}
	if q.ExpiresAt.IsZero() {
		return fmt.Errorf("shop quote expiry is required")
	}
	if strings.TrimSpace(q.SnapshotJSON) == "" || len(strings.TrimSpace(q.SnapshotSHA256)) != 64 {
		return fmt.Errorf("shop quote snapshot seal is required")
	}
	return nil
}
