package shopify

import (
	"encoding/json"
	"time"
)

// Product represents a normalized Shopify product
type Product struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Handle      string            `json:"handle"`
	ProductType string            `json:"productType"`
	Category    *TaxonomyCategory `json:"category,omitempty"`
	Vendor      string            `json:"vendor"`
	Tags        []string          `json:"tags"`
	Collections []Collection      `json:"collections,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	PriceRange  PriceRange        `json:"priceRange"`
	Images      []Image           `json:"images"`
	Options     []ProductOption   `json:"options,omitempty"`
	Variants    []Variant         `json:"variants"`
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
	ID      string `json:"id"`
	URL     string `json:"url"`
	AltText string `json:"altText"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// ProductOption represents a Shopify product option such as size or color.
type ProductOption struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Collection represents a Shopify collection attached to a product.
type Collection struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Handle string `json:"handle"`
}

// TaxonomyCategory represents Shopify's standard product category.
type TaxonomyCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SelectedOption represents a selected option value on a variant.
type SelectedOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Variant represents a product variant
type Variant struct {
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	SKU              string           `json:"sku"`
	Price            Money            `json:"price"`
	CompareAtPrice   *Money           `json:"compareAtPrice,omitempty"`
	Image            *Image           `json:"image,omitempty"`
	SelectedOptions  []SelectedOption `json:"selectedOptions,omitempty"`
	AvailableForSale bool             `json:"availableForSale"`
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
