package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

type createPetAccessGrantRequest struct {
	RecipientUserID      string   `json:"recipient_user_id"`
	RecipientDisplayName string   `json:"recipient_display_name"`
	RecipientKind        string   `json:"recipient_kind"`
	Scenario             string   `json:"scenario"`
	Scopes               []string `json:"scopes"`
	AllowDownload        bool     `json:"allow_download"`
	StartsAt             string   `json:"starts_at"`
	ExpiresAt            string   `json:"expires_at"`
	Note                 string   `json:"note"`
}

type revokePetAccessGrantRequest struct {
	Reason string `json:"reason"`
}

type petAccessGrantResponse struct {
	ID                   string     `json:"id"`
	PetID                string     `json:"pet_id"`
	OwnerUserID          string     `json:"owner_user_id"`
	RecipientUserID      string     `json:"recipient_user_id,omitempty"`
	RecipientDisplayName string     `json:"recipient_display_name,omitempty"`
	RecipientKind        string     `json:"recipient_kind,omitempty"`
	Scenario             string     `json:"scenario"`
	DeliveryKind         string     `json:"delivery_kind"`
	Scopes               []string   `json:"scopes"`
	Status               string     `json:"status"`
	ShareToken           string     `json:"share_token,omitempty"`
	ShareURL             string     `json:"share_url,omitempty"`
	AllowDownload        bool       `json:"allow_download"`
	StartsAt             time.Time  `json:"starts_at"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	RevokedAt            *time.Time `json:"revoked_at,omitempty"`
	LastAccessedAt       *time.Time `json:"last_accessed_at,omitempty"`
	Note                 string     `json:"note,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

func NewPetAccessGrantsHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}

		ownerUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		petID := strings.TrimSpace(r.PathValue("petId"))
		if petID == "" {
			http.Error(w, "petId required", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			handleListPetAccessGrants(w, db, petID, ownerUserID)
		case http.MethodPost:
			handleCreatePetAccessGrant(w, r, db, petID, ownerUserID)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func NewPetAccessGrantRevokeHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ownerUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		petID := strings.TrimSpace(r.PathValue("petId"))
		grantID := strings.TrimSpace(r.PathValue("grantId"))
		if petID == "" || grantID == "" {
			http.Error(w, "petId and grantId required", http.StatusBadRequest)
			return
		}

		var req revokePetAccessGrantRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		var grant models.PetAccessGrant
		if err := db.Where("id = ? AND pet_id = ? AND owner_user_id = ?", grantID, petID, ownerUserID).First(&grant).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "grant not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load grant: "+err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now().UTC()
		patch := map[string]any{
			"revoked_at": now,
		}
		if reason := strings.TrimSpace(req.Reason); reason != "" {
			patch["note"] = reason
		}
		if err := db.Model(&grant).Updates(patch).Error; err != nil {
			http.Error(w, "failed to revoke grant: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := db.First(&grant, "id = ?", grant.ID).Error; err != nil {
			http.Error(w, "failed to reload revoked grant: "+err.Error(), http.StatusInternalServerError)
			return
		}

		resp, err := buildGrantResponse(grant, "")
		if err != nil {
			http.Error(w, "failed to encode response: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"grant": resp})
	}
}

func NewShareResolveHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := strings.TrimSpace(r.PathValue("token"))
		if token == "" {
			http.Error(w, "token required", http.StatusBadRequest)
			return
		}

		var grant models.PetAccessGrant
		if err := db.Where("token_hash = ?", models.HashShareToken(token)).First(&grant).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "grant not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load grant: "+err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now().UTC()
		status := grant.EffectiveStatus(now)
		if status != "active" && status != "scheduled" {
			http.Error(w, "grant unavailable: "+status, http.StatusGone)
			return
		}

		if status == "active" {
			_ = db.Model(&grant).Update("last_accessed_at", now).Error
			grant.LastAccessedAt = &now
		}

		resp, err := buildGrantResponse(grant, token)
		if err != nil {
			http.Error(w, "failed to encode response: "+err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"grant": resp,
			"shared_content": map[string]any{
				"pet_id": grant.PetID,
				"sections": []map[string]any{
					{
						"id":    "basic_profile",
						"title": "Basic profile placeholder",
						"type":  "placeholder",
					},
				},
			},
		})
	}
}

func handleListPetAccessGrants(w http.ResponseWriter, db *gorm.DB, petID, ownerUserID string) {
	var grants []models.PetAccessGrant
	if err := db.Where("pet_id = ? AND owner_user_id = ?", petID, ownerUserID).
		Order("created_at DESC").
		Find(&grants).Error; err != nil {
		http.Error(w, "failed to load grants: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := make([]petAccessGrantResponse, 0, len(grants))
	for _, grant := range grants {
		item, err := buildGrantResponse(grant, "")
		if err != nil {
			http.Error(w, "failed to decode grant scopes: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp = append(resp, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"grants": resp})
}

func handleCreatePetAccessGrant(w http.ResponseWriter, r *http.Request, db *gorm.DB, petID, ownerUserID string) {
	var req createPetAccessGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req.Scenario = strings.TrimSpace(req.Scenario)
	if req.Scenario == "" {
		http.Error(w, "scenario is required", http.StatusBadRequest)
		return
	}

	deliveryKind := "private_user"
	if req.RecipientUserID == "" {
		deliveryKind = "public_link"
	}

	if deliveryKind == "private_user" {
		recipientID := strings.TrimSpace(req.RecipientUserID)
		if recipientID == "" {
			http.Error(w, "recipient_user_id is required", http.StatusBadRequest)
			return
		}
		if recipientID == ownerUserID {
			http.Error(w, "cannot share to yourself", http.StatusBadRequest)
			return
		}
		if err := ensureAuthUserExists(recipientID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	startsAt := time.Now().UTC()
	if raw := strings.TrimSpace(req.StartsAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "starts_at must be RFC3339", http.StatusBadRequest)
			return
		}
		startsAt = parsed.UTC()
	}

	var expiresAt *time.Time
	if raw := strings.TrimSpace(req.ExpiresAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			http.Error(w, "expires_at must be RFC3339", http.StatusBadRequest)
			return
		}
		normalized := parsed.UTC()
		expiresAt = &normalized
	}

	if len(req.Scopes) == 0 {
		http.Error(w, "scopes are required", http.StatusBadRequest)
		return
	}
	scopesJSON, err := json.Marshal(req.Scopes)
	if err != nil {
		http.Error(w, "failed to encode scopes", http.StatusInternalServerError)
		return
	}

	token := uuid.NewString()
	grant := models.PetAccessGrant{
		PetID:                petID,
		OwnerUserID:          ownerUserID,
		RecipientUserID:      strings.TrimSpace(req.RecipientUserID),
		RecipientDisplayName: strings.TrimSpace(req.RecipientDisplayName),
		RecipientKind:        strings.TrimSpace(req.RecipientKind),
		Scenario:             req.Scenario,
		DeliveryKind:         deliveryKind,
		ScopesJSON:           string(scopesJSON),
		AllowDownload:        req.AllowDownload,
		StartsAt:             startsAt,
		ExpiresAt:            expiresAt,
		TokenHash:            models.HashShareToken(token),
		Note:                 strings.TrimSpace(req.Note),
	}
	if err := db.Create(&grant).Error; err != nil {
		http.Error(w, "failed to create grant: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := buildGrantResponse(grant, token)
	if err != nil {
		http.Error(w, "failed to build response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"grant": resp})
}

func ensureAuthUserExists(userID string) error {
	if models.AuthDB == nil {
		return errors.New("auth db unavailable")
	}
	var user models.AuthUser
	if err := models.AuthDB.First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("recipient user not found")
		}
		return fmt.Errorf("failed to validate recipient user: %w", err)
	}
	return nil
}

func buildGrantResponse(grant models.PetAccessGrant, rawToken string) (petAccessGrantResponse, error) {
	var scopes []string
	if err := json.Unmarshal([]byte(grant.ScopesJSON), &scopes); err != nil {
		return petAccessGrantResponse{}, err
	}

	shareURL := ""
	if rawToken != "" {
		base := sharePublicBaseURL()
		if base == "" {
			base = "http://localhost:8000"
		}
		shareURL = base + sharePublicSharePath() + url.PathEscape(rawToken)
	}

	return petAccessGrantResponse{
		ID:                   grant.ID,
		PetID:                grant.PetID,
		OwnerUserID:          grant.OwnerUserID,
		RecipientUserID:      grant.RecipientUserID,
		RecipientDisplayName: grant.RecipientDisplayName,
		RecipientKind:        grant.RecipientKind,
		Scenario:             grant.Scenario,
		DeliveryKind:         grant.DeliveryKind,
		Scopes:               scopes,
		Status:               grant.EffectiveStatus(time.Now().UTC()),
		ShareToken:           rawToken,
		ShareURL:             shareURL,
		AllowDownload:        grant.AllowDownload,
		StartsAt:             grant.StartsAt,
		ExpiresAt:            grant.ExpiresAt,
		RevokedAt:            grant.RevokedAt,
		LastAccessedAt:       grant.LastAccessedAt,
		Note:                 grant.Note,
		CreatedAt:            grant.CreatedAt,
	}, nil
}

func sharePublicBaseURL() string {
	candidates := []string{
		os.Getenv("SHARE_PUBLIC_BASE_URL"),
		os.Getenv("PUBLIC_WEB_BASE_URL"),
		os.Getenv("NEXT_PUBLIC_SITE_URL"),
		os.Getenv("PUBLIC_BASE_URL"),
	}

	for _, candidate := range candidates {
		trimmed := strings.TrimRight(strings.TrimSpace(candidate), "/")
		if trimmed != "" {
			return trimmed
		}
	}

	return ""
}

func sharePublicSharePath() string {
	if trimmed := strings.TrimSpace(os.Getenv("SHARE_PUBLIC_BASE_URL")); trimmed != "" {
		return "/share/"
	}
	if trimmed := strings.TrimSpace(os.Getenv("PUBLIC_WEB_BASE_URL")); trimmed != "" {
		return "/share/"
	}
	if trimmed := strings.TrimSpace(os.Getenv("NEXT_PUBLIC_SITE_URL")); trimmed != "" {
		return "/share/"
	}
	return "/api/share/"
}

func authenticatedUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return "", false
	}
	tokenString := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	claims, err := auth.ValidateToken(tokenString)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return "", false
	}
	return strings.TrimSpace(claims.UserID), true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
