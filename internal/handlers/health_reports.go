package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vf0429/Petwell_Backend/internal/models"
	"github.com/vf0429/Petwell_Backend/internal/services/reportfusion"
	"gorm.io/gorm"
)

type healthReportCreateRequest struct {
	PetID             string                      `json:"pet_id"`
	ReportType        string                      `json:"report_type"`
	ClinicName        string                      `json:"clinic_name"`
	ReportDate        string                      `json:"report_date"`
	ImageURLs         []string                    `json:"image_urls,omitempty"`
	ImageBase64       []string                    `json:"image_base64,omitempty"`
	MockVendorResults []reportfusion.VendorResult `json:"mock_vendor_results,omitempty"`
}

func NewHealthReportCreateHandler(db *gorm.DB) http.HandlerFunc {
	vendorClient := reportfusion.NewVendorClient(45 * time.Second)

	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req healthReportCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		req.PetID = strings.TrimSpace(req.PetID)
		if req.PetID == "" {
			http.Error(w, "pet_id is required", http.StatusBadRequest)
			return
		}
		if len(req.ImageURLs) == 0 && len(req.ImageBase64) == 0 && len(req.MockVendorResults) == 0 {
			http.Error(w, "image_urls or image_base64 or mock_vendor_results is required", http.StatusBadRequest)
			return
		}

		reportDate := time.Now()
		if strings.TrimSpace(req.ReportDate) != "" {
			parsed, err := time.Parse(time.RFC3339, req.ReportDate)
			if err != nil {
				http.Error(w, "report_date must be RFC3339", http.StatusBadRequest)
				return
			}
			reportDate = parsed
		}

		var (
			vendorResults []reportfusion.VendorResult
			err           error
		)
		if len(req.MockVendorResults) > 0 {
			vendorResults = req.MockVendorResults
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			vendorResults, err = vendorClient.ExtractFromAll(ctx, reportfusion.ExtractRequest{
				ImageURLs:   req.ImageURLs,
				ImageBase64: req.ImageBase64,
			})
			if err != nil {
				http.Error(w, "failed to call extraction vendors: "+err.Error(), http.StatusBadGateway)
				return
			}
		}

		fusedFields := reportfusion.Fuse(vendorResults, vendorClient.VendorSettings())
		if len(fusedFields) == 0 {
			http.Error(w, "no fields extracted from vendors", http.StatusBadGateway)
			return
		}

		rawJSON, _ := json.Marshal(map[string]interface{}{
			"vendor_results": vendorResults,
			"fused_fields":   fusedFields,
		})

		report := models.HealthReport{
			PetID:            req.PetID,
			ReportType:       normalizeReportType(req.ReportType),
			ClinicName:       strings.TrimSpace(req.ClinicName),
			ReportDate:       reportDate,
			SourceImageCount: maxInt(len(req.ImageURLs), len(req.ImageBase64)),
			RawPayloadJSON:   string(rawJSON),
			SchemaVersion:    "v1",
			FusionVersion:    "v1",
		}

		observations, overallConfidence, overallConsensus, finalStatus := buildObservations(report.ID, fusedFields)
		report.OverallConfidence = overallConfidence
		report.ConsensusScore = overallConsensus
		report.FinalReviewStatus = finalStatus

		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&report).Error; err != nil {
				return err
			}

			for i := range observations {
				observations[i].ReportID = report.ID
			}
			if len(observations) > 0 {
				if err := tx.Create(&observations).Error; err != nil {
					return err
				}
			}

			for _, vr := range vendorResults {
				raw, _ := json.Marshal(vr)
				ve := models.ReportVendorExtraction{
					ReportID:        report.ID,
					VendorID:        vr.VendorID,
					Model:           vr.Model,
					FieldCount:      len(vr.Fields),
					RawResponseJSON: string(raw),
				}
				if err := tx.Create(&ve).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			http.Error(w, "failed to persist report: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if err := db.Preload("Observations").Preload("VendorExtractions").First(&report, "id = ?", report.ID).Error; err != nil {
			http.Error(w, "failed to load created report: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"report": report,
		})
	}
}

func NewHealthReportDetailHandler(db *gorm.DB) http.HandlerFunc {
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
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}

		var report models.HealthReport
		if err := db.Preload("Observations").Preload("VendorExtractions").First(&report, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "report not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to query report: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"report": report,
		})
	}
}

func NewPetHealthProfileHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		petID := strings.TrimSpace(r.PathValue("petId"))
		if petID == "" {
			http.Error(w, "petId required", http.StatusBadRequest)
			return
		}

		var reports []models.HealthReport
		if err := db.Where("pet_id = ?", petID).
			Order("report_date DESC").
			Find(&reports).Error; err != nil {
			http.Error(w, "failed to load reports: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var observations []models.ReportObservation
		if err := db.Joins("JOIN health_reports ON health_reports.id = report_observations.report_id").
			Where("health_reports.pet_id = ? AND report_observations.is_verified = ?", petID, true).
			Order("report_observations.created_at DESC").
			Find(&observations).Error; err != nil {
			http.Error(w, "failed to load observations: "+err.Error(), http.StatusInternalServerError)
			return
		}

		latestByMetric := map[string]models.ReportObservation{}
		for _, obs := range observations {
			key := strings.ToLower(strings.TrimSpace(obs.MetricKeyRaw))
			if key == "" {
				continue
			}
			if _, ok := latestByMetric[key]; !ok {
				latestByMetric[key] = obs
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"pet_id":                petID,
			"reports":               reports,
			"verified_observations": observations,
			"latest_by_metric":      latestByMetric,
		})
	}
}

func NewObservationReviewHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPut {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		obsID := strings.TrimSpace(r.PathValue("observationId"))
		if obsID == "" {
			http.Error(w, "observationId required", http.StatusBadRequest)
			return
		}

		var req struct {
			ValueNumber  *float64 `json:"value_number,omitempty"`
			ValueText    *string  `json:"value_text,omitempty"`
			Unit         *string  `json:"unit,omitempty"`
			Flag         *string  `json:"flag,omitempty"`
			IsVerified   *bool    `json:"is_verified,omitempty"`
			ReviewStatus *string  `json:"review_status,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		var obs models.ReportObservation
		if err := db.First(&obs, "id = ?", obsID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "observation not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load observation: "+err.Error(), http.StatusInternalServerError)
			return
		}

		patch := map[string]interface{}{}
		if req.ValueNumber != nil {
			patch["value_number"] = req.ValueNumber
		}
		if req.ValueText != nil {
			patch["value_text"] = strings.TrimSpace(*req.ValueText)
		}
		if req.Unit != nil {
			patch["unit"] = strings.TrimSpace(*req.Unit)
		}
		if req.Flag != nil {
			patch["flag"] = strings.TrimSpace(*req.Flag)
		}
		if req.IsVerified != nil {
			patch["is_verified"] = *req.IsVerified
		}
		if req.ReviewStatus != nil {
			status := strings.TrimSpace(*req.ReviewStatus)
			if status != string(models.ReviewStatusAutoPass) &&
				status != string(models.ReviewStatusPendingReview) &&
				status != string(models.ReviewStatusManualConfirmRequired) {
				http.Error(w, "invalid review_status", http.StatusBadRequest)
				return
			}
			patch["review_status"] = status
		}
		if len(patch) == 0 {
			http.Error(w, "no fields to update", http.StatusBadRequest)
			return
		}

		if err := db.Model(&obs).Updates(patch).Error; err != nil {
			http.Error(w, "failed to update observation: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if err := db.First(&obs, "id = ?", obs.ID).Error; err != nil {
			http.Error(w, "failed to load updated observation: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"observation": obs,
		})
	}
}

func buildObservations(reportID string, fields []reportfusion.FusedField) ([]models.ReportObservation, float64, float64, string) {
	if len(fields) == 0 {
		return nil, 0, 0, string(models.ReviewStatusPendingReview)
	}

	observations := make([]models.ReportObservation, 0, len(fields))
	sumConfidence := 0.0
	sumConsensus := 0.0
	finalStatus := string(models.ReviewStatusAutoPass)
	for _, f := range fields {
		sumConfidence += f.FusionConfidence
		sumConsensus += f.ConsensusScore
		status := string(f.ReviewStatus)
		switch status {
		case string(models.ReviewStatusManualConfirmRequired):
			finalStatus = string(models.ReviewStatusManualConfirmRequired)
		case string(models.ReviewStatusPendingReview):
			if finalStatus != string(models.ReviewStatusManualConfirmRequired) {
				finalStatus = string(models.ReviewStatusPendingReview)
			}
		}

		bboxJSON := ""
		if len(f.SourceBBox) > 0 {
			if raw, err := json.Marshal(f.SourceBBox); err == nil {
				bboxJSON = string(raw)
			}
		}
		vendorsRaw, _ := json.Marshal(f.ContributingVendors)
		obs := models.ReportObservation{
			ReportID:            reportID,
			MetricKeyRaw:        f.MetricKey,
			ValueNumber:         f.ValueNumber,
			ValueText:           f.ValueText,
			Unit:                f.Unit,
			Confidence:          f.FusionConfidence,
			ConsensusScore:      f.ConsensusScore,
			ReviewStatus:        status,
			IsVerified:          status == string(models.ReviewStatusAutoPass),
			SourcePage:          f.SourcePage,
			SourceLine:          f.SourceLine,
			SourceBBoxJSON:      bboxJSON,
			ContributingVendors: string(vendorsRaw),
		}
		observations = append(observations, obs)
	}

	return observations, sumConfidence / float64(len(fields)), sumConsensus / float64(len(fields)), finalStatus
}

func normalizeReportType(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return "other"
	}
	switch v {
	case "blood_test", "biochemistry", "urine", "fecal", "imaging", "other":
		return v
	default:
		return "other"
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
