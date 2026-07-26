package hicustom

// Package hicustom is the client for the HiCustom (指纹科技) open platform.
// The official API spec is gated behind open-platform login
// (https://www.hicustom.com/open_platform/home); the endpoint paths, field
// names and signature rules below follow the documented-but-unverified design
// in docs/hicustom_integration_design.md §7–§10 and MUST be confirmed against
// the official docs before production use. Every such assumption is tagged TODO.

// BlankProduct is the service-layer transport for a HiCustom blank product.
// Mirrors models.BlankProduct (GORM) but kept separate to match the
// shopify service pattern (services/shopify/models.go vs internal/models).
type BlankProduct struct {
	SKU          string `json:"sku"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	PrintArea    string `json:"printArea,omitempty"`
	Price        string `json:"price"`
	CurrencyCode string `json:"currencyCode"`
	CoverURL     string `json:"coverUrl"`
	ImageURL     string `json:"imageUrl,omitempty"`
	Available    bool   `json:"available"`
}

// ProductListResult is a page of blank products plus pagination cursor info.
type ProductListResult struct {
	Products   []BlankProduct
	HasMore    bool
	NextCursor string
}

// CreateOrderItem is one line in a HiCustom order push.
type CreateOrderItem struct {
	CustomProductID string `json:"customProductId"`
	SKU             string `json:"sku"`
	Quantity        int    `json:"quantity"`
}

// CreateOrderRequest is the payload pushed to HiCustom after a successful payment.
type CreateOrderRequest struct {
	OrderNo         string            `json:"orderNo"` // Pawrd idempotency key
	Items           []CreateOrderItem `json:"items"`
	RecipientName   string            `json:"recipientName"`
	RecipientPhone  string            `json:"recipientPhone"`
	RecipientCountry string           `json:"recipientCountry,omitempty"`
	RecipientState   string           `json:"recipientState,omitempty"`
	RecipientCity    string           `json:"recipientCity,omitempty"`
	RecipientAddress string           `json:"recipientAddress"`
	RecipientZip     string           `json:"recipientZip,omitempty"`
}

// CreateOrderResult is the response from HiCustom's create-order endpoint.
type CreateOrderResult struct {
	HiCustomOrderID string `json:"hiCustomOrderId"`
	Status          string `json:"status"`
}
