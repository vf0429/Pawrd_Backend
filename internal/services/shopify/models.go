package shopify

import (
	"encoding/json"
	"time"
)

// Product represents a normalized Shopify product
type Product struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Handle      string    `json:"handle"`
	ProductType string    `json:"productType"`
	Vendor      string    `json:"vendor"`
	Tags        []string  `json:"tags"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	PriceRange  PriceRange `json:"priceRange"`
	Images      []Image    `json:"images"`
	Variants    []Variant  `json:"variants"`
}

// PriceRange represents the price range of a product
type PriceRange struct {
	MinVariantPrice Money `json:"minVariantPrice"`
	MaxVariantPrice Money `json:"maxVariantPrice"`
}

// Money represents a monetary amount
type Money struct {
	Amount       string `json:"amount"`
	CurrencyCode string `json:"currencyCode"`
}

// Image represents a product image
type Image struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	AltText  string `json:"altText"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// Variant represents a product variant
type Variant struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	SKU      string `json:"sku"`
	Price    Money  `json:"price"`
	Image    *Image `json:"image,omitempty"`
	AvailableForSale bool `json:"availableForSale"`
}

// ProductResponse is the response structure for product queries
type ProductResponse struct {
	Products struct {
		Edges []struct {
			Node Product `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"products"`
}

// ProductDetailResponse is the response for a single product query
type ProductDetailResponse struct {
	Product Product `json:"product"`
}

// GraphQLError represents a GraphQL error
type GraphQLError struct {
	Message    string                 `json:"message"`
	Path       []interface{}          `json:"path,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// GraphQLResponse is the wrapper for all GraphQL responses
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// ClientError represents an error from the Shopify client
type ClientError struct {
	StatusCode int
	Message    string
}

func (e *ClientError) Error() string {
	return e.Message
}
