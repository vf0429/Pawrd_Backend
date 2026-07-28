package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
)

func TestMaterializeClinicPhotoURLDoesNotExposeAPIKey(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://api.pawrd.top/clinics", nil)
	clinic := models.Clinic{PhotoReference: "photo-reference"}

	materializeClinicPhotoURL(request, &clinic)

	if strings.Contains(clinic.PhotoURL, "key=") {
		t.Fatalf("photo URL exposed an API key: %s", clinic.PhotoURL)
	}
	if !strings.Contains(clinic.PhotoURL, "/api/maps/place-photo?") {
		t.Fatalf("unexpected proxy URL: %s", clinic.PhotoURL)
	}
}

func TestPlacePhotoProxyRejectsMissingReference(t *testing.T) {
	handler := NewPlacePhotoProxyHandler(&config.Config{MapsAPIKey: "test-key"})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/maps/place-photo", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestPlacePhotoProxyRejectsInvalidWidth(t *testing.T) {
	handler := NewPlacePhotoProxyHandler(&config.Config{MapsAPIKey: "test-key"})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/maps/place-photo?reference=ref&maxwidth=9999", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusBadRequest)
	}
}
