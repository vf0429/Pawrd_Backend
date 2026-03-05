package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vf0429/Petwell_Backend/internal/config"
	"github.com/vf0429/Petwell_Backend/internal/services/shopify"
)

// ShopProduct represents the simplified product format for iOS
type ShopProduct struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Price         string `json:"price"`
	CurrencyCode  string `json:"currencyCode"`
	ImageURL      string `json:"imageUrl"`
	ProductType   string `json:"productType"`
	Vendor        string `json:"vendor"`
	Handle        string `json:"handle"`
	VariantID     string `json:"variantId"`
	Available     bool   `json:"available"`
}

// ProductsResponse is the response structure for product list
type ProductsResponse struct {
	Products   []ShopProduct `json:"products"`
	HasMore    bool          `json:"hasMore"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// ShopifyClient interface allows both real and mock clients
type ShopifyClient interface {
	FetchProducts(first int, after string) ([]shopify.Product, bool, string, error)
	FetchProductByHandle(handle string) (*shopify.Product, error)
}

// NewShopHandler creates a handler for shop endpoints
func NewShopHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Initialize Shopify client (real or mock)
		var client ShopifyClient
		var err error

		if cfg.UseMockShopify {
			client = shopify.NewMockClient()
		} else {
			client, err = shopify.NewClient(cfg)
			if err != nil {
				http.Error(w, "Shopify configuration error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		// Parse query parameters
		first := 20
		if limit := r.URL.Query().Get("limit"); limit != "" {
			if parsed, err := strconv.Atoi(limit); err == nil && parsed > 0 && parsed <= 100 {
				first = parsed
			}
		}
		after := r.URL.Query().Get("cursor")

		// Fetch products from Shopify
		products, hasMore, nextCursor, err := client.FetchProducts(first, after)
		if err != nil {
			http.Error(w, "Failed to fetch products: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Transform to iOS format
		shopProducts := make([]ShopProduct, 0, len(products))
		for _, p := range products {
			shopProducts = append(shopProducts, transformProduct(p))
		}

		// Return response
		w.Header().Set("Content-Type", "application/json")
		response := ProductsResponse{
			Products:   shopProducts,
			HasMore:    hasMore,
			NextCursor: nextCursor,
		}
		json.NewEncoder(w).Encode(response)
	}
}

// NewShopProductDetailHandler creates a handler for single product lookup
func NewShopProductDetailHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract handle from URL path
		// Expected: /api/shop/products/{handle}
		handle := r.PathValue("handle")
		if handle == "" {
			// Try query parameter as fallback
			handle = r.URL.Query().Get("handle")
		}
		if handle == "" {
			http.Error(w, "Product handle required", http.StatusBadRequest)
			return
		}

		// Initialize Shopify client (real or mock)
		var client ShopifyClient
		var err error

		if cfg.UseMockShopify {
			client = shopify.NewMockClient()
		} else {
			client, err = shopify.NewClient(cfg)
			if err != nil {
				http.Error(w, "Shopify configuration error: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		// Fetch product by handle
		product, err := client.FetchProductByHandle(handle)
		if err != nil {
			if shopErr, ok := err.(*shopify.ClientError); ok && shopErr.StatusCode == http.StatusNotFound {
				http.Error(w, "Product not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to fetch product: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Return response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(transformProduct(*product))
	}
}

// transformProduct converts a shopify.Product to ShopProduct for iOS
func transformProduct(p shopify.Product) ShopProduct {
	sp := ShopProduct{
		ID:          p.ID,
		Title:       p.Title,
		Description: p.Description,
		Price:       p.PriceRange.MinVariantPrice.Amount,
		CurrencyCode: p.PriceRange.MinVariantPrice.CurrencyCode,
		ProductType: p.ProductType,
		Vendor:      p.Vendor,
		Handle:      p.Handle,
	}

	// Get first image URL if available
	if len(p.Images) > 0 {
		sp.ImageURL = p.Images[0].URL
	}

	// Get first variant info if available
	if len(p.Variants) > 0 {
		sp.VariantID = p.Variants[0].ID
		sp.Available = p.Variants[0].AvailableForSale
		// Use variant price if available (may differ from min price)
		sp.Price = p.Variants[0].Price.Amount
		sp.CurrencyCode = p.Variants[0].Price.CurrencyCode
	}

	return sp
}
