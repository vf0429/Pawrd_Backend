package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
)

const (
	defaultPlacePhotoWidth = 800
	maxPlacePhotoWidth     = 1600
)

var placePhotoEndpoint = "https://maps.googleapis.com/maps/api/place/photo"

func materializeClinicPhotoURL(r *http.Request, clinic *models.Clinic) {
	if clinic == nil || strings.TrimSpace(clinic.PhotoReference) == "" {
		if clinic != nil {
			clinic.PhotoURL = ""
		}
		return
	}

	query := url.Values{}
	query.Set("reference", clinic.PhotoReference)
	query.Set("maxwidth", strconv.Itoa(defaultPlacePhotoWidth))
	clinic.PhotoURL = requestBaseURL(r) + "/api/maps/place-photo?" + query.Encode()
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "https" {
		scheme = "https"
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func NewPlacePhotoProxyHandler(cfg *config.Config) http.HandlerFunc {
	client := &http.Client{Timeout: 15 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg == nil || strings.TrimSpace(cfg.MapsAPIKey) == "" {
			http.Error(w, "Google Maps is not configured", http.StatusServiceUnavailable)
			return
		}

		reference := strings.TrimSpace(r.URL.Query().Get("reference"))
		if reference == "" || len(reference) > 2048 || strings.ContainsAny(reference, "\r\n") {
			http.Error(w, "invalid photo reference", http.StatusBadRequest)
			return
		}

		width := defaultPlacePhotoWidth
		if rawWidth := strings.TrimSpace(r.URL.Query().Get("maxwidth")); rawWidth != "" {
			parsed, err := strconv.Atoi(rawWidth)
			if err != nil || parsed < 1 || parsed > maxPlacePhotoWidth {
				http.Error(w, fmt.Sprintf("maxwidth must be between 1 and %d", maxPlacePhotoWidth), http.StatusBadRequest)
				return
			}
			width = parsed
		}

		query := url.Values{}
		query.Set("photo_reference", reference)
		query.Set("maxwidth", strconv.Itoa(width))
		query.Set("key", cfg.MapsAPIKey)

		upstreamResponse, err := client.Get(placePhotoEndpoint + "?" + query.Encode())
		if err != nil {
			http.Error(w, "Google Place Photos request failed", http.StatusBadGateway)
			return
		}
		defer upstreamResponse.Body.Close()

		if upstreamResponse.StatusCode != http.StatusOK {
			http.Error(w, "Google Place Photos returned an error", http.StatusBadGateway)
			return
		}
		contentType := upstreamResponse.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "image/") {
			http.Error(w, "Google Place Photos returned non-image content", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, upstreamResponse.Body); err != nil {
			return
		}
	}
}
