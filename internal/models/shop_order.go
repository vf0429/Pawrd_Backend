package models

import "time"

// ShopOrder is Pawrd's durable mirror of a paid Shopify order. Stripe remains
// the payment processor; Shopify is used for merchant operations and returns.
type ShopOrder struct {
	ID                   string  `gorm:"primaryKey;size:36"`
	UserID               string  `gorm:"index;size:36;not null"`
	PaymentIntentID      *string `gorm:"uniqueIndex;size:255"`
	ShopifyOrderID       *string `gorm:"uniqueIndex;size:255"`
	ShopifyOrderLegacyID string  `gorm:"index;size:64"`
	ShopifyOrderName     string  `gorm:"size:64"`
	Status               string  `gorm:"index;size:32;not null"`
	FinancialStatus      string  `gorm:"size:32"`
	FulfillmentStatus    string  `gorm:"size:32"`
	Currency             string  `gorm:"size:8;not null"`
	TotalAmountMinor     int64   `gorm:"not null"`
	CustomerName         string  `gorm:"size:160"`
	CustomerEmail        string  `gorm:"size:254"`
	CustomerPhone        string  `gorm:"size:32"`
	ShippingAddress1     string  `gorm:"size:255"`
	ShippingDistrict     string  `gorm:"size:100"`
	ShippingRegion       string  `gorm:"size:100"`
	ShippingCountry      string  `gorm:"size:100"`
	TrackingCompany      string  `gorm:"size:120"`
	TrackingNumber       string  `gorm:"size:160"`
	TrackingURL          string  `gorm:"size:500"`
	EstimatedDeliveryAt  *time.Time
	DeliveredAt          *time.Time
	CustomerReceivedAt   *time.Time
	ReturnID             string          `gorm:"size:255"`
	ReturnName           string          `gorm:"size:64"`
	ReturnStatus         string          `gorm:"size:32"`
	ReturnReason         string          `gorm:"size:32"`
	ReturnNote           string          `gorm:"size:500"`
	FailureReason        string          `gorm:"size:1000"`
	Items                []ShopOrderItem `gorm:"foreignKey:OrderID;constraint:OnDelete:CASCADE"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ShopifyOrderGID returns the Shopify Admin GraphQL order ID when fulfillment
// has created one. Pending Stripe checkouts intentionally keep this column NULL
// so the unique index does not treat every unfulfilled order as the same value.
func (o ShopOrder) ShopifyOrderGID() string {
	if o.ShopifyOrderID == nil {
		return ""
	}
	return *o.ShopifyOrderID
}

// PaymentIntentIDValue returns the Stripe PaymentIntent ID, or "" while the
// order waits for the intent to be created. The column is NULL (not "") so the
// unique index tolerates any number of pre-intent orders in both Postgres and
// SQLite — same pattern as ShopifyOrderID.
func (o ShopOrder) PaymentIntentIDValue() string {
	if o.PaymentIntentID == nil {
		return ""
	}
	return *o.PaymentIntentID
}

type ShopOrderItem struct {
	ID                string `gorm:"primaryKey;size:36"`
	OrderID           string `gorm:"index;size:36;not null"`
	ShopifyLineItemID string `gorm:"size:255"`
	Source            string `gorm:"size:24;not null"`
	Handle            string `gorm:"size:255"`
	VariantID         string `gorm:"size:255"`
	Title             string `gorm:"size:255;not null"`
	ImageURL          string `gorm:"size:1000"`
	Quantity          int    `gorm:"not null"`
	UnitAmountMinor   int64  `gorm:"not null"`
	Currency          string `gorm:"size:8;not null"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ShopIntegrationEvent makes Stripe and Shopify webhook processing idempotent.
type ShopIntegrationEvent struct {
	ID              string `gorm:"primaryKey;size:36"`
	Provider        string `gorm:"uniqueIndex:idx_shop_event;size:24;not null"`
	ExternalEventID string `gorm:"uniqueIndex:idx_shop_event;size:255;not null"`
	Topic           string `gorm:"size:100;not null"`
	Status          string `gorm:"index;size:24;not null"`
	LastError       string `gorm:"size:1000"`
	ProcessedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
