package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/vf0429/Petwell_Backend/internal/models"
	"gorm.io/gorm"
)

// NewMedicalServicesHandler handles GET /api/medical/services
// Returns all active medical service entries ordered by sort_order.
func NewMedicalServicesHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var services []models.MedicalService
		if err := db.Where("is_active = ?", true).
			Order("sort_order ASC").
			Find(&services).Error; err != nil {
			http.Error(w, "Failed to fetch services: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(services)
	}
}

// NewMedicalServiceDetailHandler handles GET /api/medical/services/{category}
// Returns the full detail of one service by category slug.
func NewMedicalServiceDetailHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		category := r.PathValue("category")
		if category == "" {
			category = r.URL.Query().Get("category")
		}
		if category == "" {
			http.Error(w, "category required", http.StatusBadRequest)
			return
		}

		var svc models.MedicalService
		if err := db.Where("category = ? AND is_active = ?", category, true).
			First(&svc).Error; err != nil {
			http.Error(w, "Service not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(svc)
	}
}

// NewMedicalAdminUpdateHandler handles PUT /api/medical/admin/services/{id}
// Requires X-Admin-Key header matching MEDICAL_ADMIN_KEY env variable.
// Partners use this to update their own service content without a new app release.
func NewMedicalAdminUpdateHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Auth check
		adminKey := os.Getenv("MEDICAL_ADMIN_KEY")
		if adminKey == "" {
			adminKey = "petwell-admin-dev" // fallback for local dev
		}
		if r.Header.Get("X-Admin-Key") != adminKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}

		var svc models.MedicalService
		if err := db.First(&svc, "id = ?", id).Error; err != nil {
			http.Error(w, "Service not found", http.StatusNotFound)
			return
		}

		// Only allow updating safe fields — category and ID are immutable
		var updates struct {
			Name        *string `json:"name"`
			NameZh      *string `json:"name_zh"`
			Description *string `json:"description"`
			DescZh      *string `json:"desc_zh"`
			ContentJSON *string `json:"content_json"`
			Provider    *string `json:"provider"`
			Contact     *string `json:"contact"`
			IsActive    *bool   `json:"is_active"`
			SortOrder   *int    `json:"sort_order"`
		}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		// Validate content_json is valid JSON if provided
		if updates.ContentJSON != nil {
			trimmed := strings.TrimSpace(*updates.ContentJSON)
			if trimmed != "" && !json.Valid([]byte(trimmed)) {
				http.Error(w, "content_json must be valid JSON", http.StatusBadRequest)
				return
			}
		}

		patch := map[string]interface{}{}
		if updates.Name != nil        { patch["name"] = *updates.Name }
		if updates.NameZh != nil      { patch["name_zh"] = *updates.NameZh }
		if updates.Description != nil { patch["description"] = *updates.Description }
		if updates.DescZh != nil      { patch["desc_zh"] = *updates.DescZh }
		if updates.ContentJSON != nil { patch["content_json"] = *updates.ContentJSON }
		if updates.Provider != nil    { patch["provider"] = *updates.Provider }
		if updates.Contact != nil     { patch["contact"] = *updates.Contact }
		if updates.IsActive != nil    { patch["is_active"] = *updates.IsActive }
		if updates.SortOrder != nil   { patch["sort_order"] = *updates.SortOrder }

		if len(patch) == 0 {
			http.Error(w, "no fields to update", http.StatusBadRequest)
			return
		}

		if err := db.Model(&svc).Updates(patch).Error; err != nil {
			http.Error(w, "Failed to update service: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(svc)
	}
}
