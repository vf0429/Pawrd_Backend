package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vf0429/Petwell_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupHealthReportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.HealthReport{}, &models.ReportObservation{}, &models.ReportVendorExtraction{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestCreateHealthReportWithMockVendors(t *testing.T) {
	db := setupHealthReportTestDB(t)
	h := NewHealthReportCreateHandler(db)

	payload := map[string]interface{}{
		"pet_id":      "pet_123",
		"report_type": "blood_test",
		"report_date": "2026-03-07T12:00:00Z",
		"mock_vendor_results": []map[string]interface{}{
			{
				"vendor_id": "v1",
				"model":     "m1",
				"fields": []map[string]interface{}{
					{"metric_key": "ALT", "value_number": 100, "unit": "U/L", "confidence": 0.9},
				},
			},
			{
				"vendor_id": "v2",
				"model":     "m2",
				"fields": []map[string]interface{}{
					{"metric_key": "ALT", "value_number": 101, "unit": "U/L", "confidence": 0.9},
				},
			},
		},
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/profile/health-reports", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Report models.HealthReport `json:"report"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Report.PetID != "pet_123" {
		t.Fatalf("pet id mismatch: %s", resp.Report.PetID)
	}
	if len(resp.Report.Observations) == 0 {
		t.Fatalf("expected observations in response")
	}
}

func TestPetHealthOrderAscDesc(t *testing.T) {
	db := setupHealthReportTestDB(t)

	r1 := models.HealthReport{
		PetID:             "pet_abc",
		ReportType:        "blood_test",
		ReportDate:        time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		FinalReviewStatus: string(models.ReviewStatusAutoPass),
	}
	r2 := models.HealthReport{
		PetID:             "pet_abc",
		ReportType:        "blood_test",
		ReportDate:        time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC),
		FinalReviewStatus: string(models.ReviewStatusAutoPass),
	}
	if err := db.Create(&r1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&r2).Error; err != nil {
		t.Fatal(err)
	}
	vOld := 88.5
	vNew := 100.0
	oldObs := models.ReportObservation{
		ReportID:       r1.ID,
		MetricKeyRaw:   "alt",
		ValueNumber:    &vOld,
		Unit:           "U/L",
		ReviewStatus:   string(models.ReviewStatusAutoPass),
		IsVerified:     true,
		Confidence:     0.9,
		ConsensusScore: 1,
	}
	newObs := models.ReportObservation{
		ReportID:       r2.ID,
		MetricKeyRaw:   "alt",
		ValueNumber:    &vNew,
		Unit:           "U/L",
		ReviewStatus:   string(models.ReviewStatusAutoPass),
		IsVerified:     true,
		Confidence:     0.9,
		ConsensusScore: 1,
	}
	if err := db.Create(&oldObs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&newObs).Error; err != nil {
		t.Fatal(err)
	}

	h := NewPetHealthProfileHandler(db)

	// desc default
	reqDesc := httptest.NewRequest(http.MethodGet, "/api/profile/pets/pet_abc/health", nil)
	reqDesc.SetPathValue("petId", "pet_abc")
	wDesc := httptest.NewRecorder()
	h(wDesc, reqDesc)
	if wDesc.Code != http.StatusOK {
		t.Fatalf("desc expected 200, got %d", wDesc.Code)
	}
	var respDesc struct {
		Reports        []models.HealthReport               `json:"reports"`
		LatestByMetric map[string]models.ReportObservation `json:"latest_by_metric"`
	}
	if err := json.Unmarshal(wDesc.Body.Bytes(), &respDesc); err != nil {
		t.Fatal(err)
	}
	if len(respDesc.Reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(respDesc.Reports))
	}
	if !respDesc.Reports[0].ReportDate.After(respDesc.Reports[1].ReportDate) {
		t.Fatalf("desc order mismatch")
	}
	if got := *respDesc.LatestByMetric["alt"].ValueNumber; got != 100 {
		t.Fatalf("latest_by_metric should be newest value 100, got %v", got)
	}

	reqAsc := httptest.NewRequest(http.MethodGet, "/api/profile/pets/pet_abc/health?order=asc", nil)
	reqAsc.SetPathValue("petId", "pet_abc")
	wAsc := httptest.NewRecorder()
	h(wAsc, reqAsc)
	if wAsc.Code != http.StatusOK {
		t.Fatalf("asc expected 200, got %d", wAsc.Code)
	}
	var respAsc struct {
		Reports        []models.HealthReport               `json:"reports"`
		LatestByMetric map[string]models.ReportObservation `json:"latest_by_metric"`
	}
	if err := json.Unmarshal(wAsc.Body.Bytes(), &respAsc); err != nil {
		t.Fatal(err)
	}
	if !respAsc.Reports[0].ReportDate.Before(respAsc.Reports[1].ReportDate) {
		t.Fatalf("asc order mismatch")
	}
	if got := *respAsc.LatestByMetric["alt"].ValueNumber; got != 100 {
		t.Fatalf("latest_by_metric should stay newest value 100, got %v", got)
	}
}
