package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

// --- Designer URL ---

// NewHiCustomDesignerURLHandler returns a signed designer URL for the iOS
// WKWebView / web iframe to embed. GET /api/shop/hicustom/designer-url?sku=...
// The signing happens server-side so AppSecret never reaches the client (§8.1).
func NewHiCustomDesignerURLHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sku := strings.TrimSpace(r.URL.Query().Get("sku"))
		if sku == "" {
			http.Error(w, "sku query parameter is required", http.StatusBadRequest)
			return
		}

		client, err := newHiCustomClient(cfg)
		if err != nil {
			http.Error(w, "HiCustom configuration error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		designerURL, err := client.DesignerURL(sku)
		if err != nil {
			http.Error(w, "Failed to build designer URL: "+err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"designerUrl": designerURL, "sku": sku})
	}
}

// --- CustomDesign persistence ---

type createCustomDesignRequest struct {
	BlankProductSKU  string          `json:"blankProductSku"`
	CustomProductID  string          `json:"customProductId"`
	PreviewURL       string          `json:"previewUrl"`
	Snapshot         json.RawMessage `json:"snapshot,omitempty"`
}

type customDesignResponse struct {
	ID              string `json:"id"`
	UserID          string `json:"userId"`
	BlankProductSKU string `json:"blankProductSku"`
	CustomProductID string `json:"customProductId"`
	PreviewURL      string `json:"previewUrl"`
}

// NewHiCustomDesignCreateHandler persists the result of a designer SDK
// designComplete callback. POST /api/shop/hicustom/designs with X-User-Id header.
// Returns the stored design so the client can add it to the cart (source=hicustom).
func NewHiCustomDesignCreateHandler(cfg *config.Config, db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		userID := strings.TrimSpace(r.Header.Get("X-User-Id"))
		if userID == "" {
			http.Error(w, "X-User-Id header is required", http.StatusUnauthorized)
			return
		}

		var req createCustomDesignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid payload", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.BlankProductSKU) == "" || strings.TrimSpace(req.CustomProductID) == "" {
			http.Error(w, "blankProductSku and customProductId are required", http.StatusBadRequest)
			return
		}

		snapshot := ""
		if len(req.Snapshot) > 0 && string(req.Snapshot) != "null" {
			snapshot = string(req.Snapshot)
		}

		design := models.CustomDesign{
			ID:              uuid.NewString(),
			UserID:          userID,
			BlankProductSKU: strings.TrimSpace(req.BlankProductSKU),
			CustomProductID: strings.TrimSpace(req.CustomProductID),
			PreviewURL:      strings.TrimSpace(req.PreviewURL),
			Snapshot:        snapshot,
			CreatedAt:       time.Now(),
		}
		if err := db.Create(&design).Error; err != nil {
			http.Error(w, "Failed to save design: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(customDesignResponse{
			ID:              design.ID,
			UserID:          design.UserID,
			BlankProductSKU: design.BlankProductSKU,
			CustomProductID: design.CustomProductID,
			PreviewURL:      design.PreviewURL,
		})
	}
}

// NewHiCustomDesignDetailHandler returns a stored design by id.
// GET /api/shop/hicustom/designs/{id}
func NewHiCustomDesignDetailHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			http.Error(w, "design id is required", http.StatusBadRequest)
			return
		}

		var design models.CustomDesign
		if err := db.First(&design, "id = ?", id).Error; err != nil {
			http.Error(w, "Design not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(customDesignResponse{
			ID:              design.ID,
			UserID:          design.UserID,
			BlankProductSKU: design.BlankProductSKU,
			CustomProductID: design.CustomProductID,
			PreviewURL:      design.PreviewURL,
		})
	}
}

// blankProductPrice looks up a cached BlankProduct by SKU for checkout pricing.
// Used by shop_checkout.go for source=hicustom line items.
func blankProductPrice(db *gorm.DB, sku string) (models.BlankProduct, error) {
	var bp models.BlankProduct
	if err := db.First(&bp, "sku = ?", sku).Error; err != nil {
		return bp, err
	}
	return bp, nil
}
