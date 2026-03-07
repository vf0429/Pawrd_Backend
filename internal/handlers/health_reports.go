package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vf0429/Petwell_Backend/internal/models"
	"github.com/vf0429/Petwell_Backend/internal/services/objectstore"
	"github.com/vf0429/Petwell_Backend/internal/services/reportfusion"
	"gorm.io/gorm"
)

type healthReportCreateRequest struct {
	PetID             string                      `json:"pet_id"`
	ReportType        string                      `json:"report_type"`
	ClinicName        string                      `json:"clinic_name"`
	ReportDate        string                      `json:"report_date"`
	ExtractionMode    string                      `json:"extraction_mode,omitempty"` // markdown | json
	ImageURLs         []string                    `json:"image_urls,omitempty"`
	ImageObjectKeys   []string                    `json:"image_object_keys,omitempty"`
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
		// Keep pet_id consistent with client-side pet identity.
		// We never generate or mutate pet_id here; we only persist the caller-provided value.
		if len(req.ImageURLs) == 0 && len(req.ImageObjectKeys) == 0 && len(req.ImageBase64) == 0 && len(req.MockVendorResults) == 0 {
			http.Error(w, "image_urls or image_object_keys or image_base64 or mock_vendor_results is required", http.StatusBadRequest)
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

		mode := normalizeExtractionMode(req.ExtractionMode)

		if len(req.ImageObjectKeys) > 0 {
			store, err := objectstore.NewCOSStoreFromEnv()
			if err != nil {
				http.Error(w, "cos not configured for image_object_keys: "+err.Error(), http.StatusBadRequest)
				return
			}
			for _, key := range req.ImageObjectKeys {
				key = strings.TrimSpace(key)
				if key == "" {
					continue
				}
				signedURL, _, err := store.PresignRead(key)
				if err != nil {
					http.Error(w, "failed to sign read url: "+err.Error(), http.StatusBadRequest)
					return
				}
				req.ImageURLs = append(req.ImageURLs, signedURL)
			}
		}

		var (
			vendorResults []reportfusion.VendorResult
			vendorMD      []reportfusion.VendorMarkdownResult
			err           error
		)
		if len(req.MockVendorResults) > 0 {
			vendorResults = req.MockVendorResults
			mode = "json"
		} else if mode == "markdown" {
			ctx, cancel := context.WithTimeout(r.Context(), extractionTimeout())
			defer cancel()
			vendorMD, err = vendorClient.ExtractMarkdownFromAll(ctx, reportfusion.ExtractRequest{
				ImageURLs:      req.ImageURLs,
				ImageBase64:    req.ImageBase64,
				ExtractionMode: "markdown",
			})
			if err != nil {
				http.Error(w, "failed to call extraction vendors: "+err.Error(), http.StatusBadGateway)
				return
			}
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), extractionTimeout())
			defer cancel()
			vendorResults, err = vendorClient.ExtractFromAll(ctx, reportfusion.ExtractRequest{
				ImageURLs:      req.ImageURLs,
				ImageBase64:    req.ImageBase64,
				ExtractionMode: "json",
			})
			if err != nil {
				http.Error(w, "failed to call extraction vendors: "+err.Error(), http.StatusBadGateway)
				return
			}
		}

		report := models.HealthReport{
			PetID:            req.PetID,
			ReportType:       normalizeReportType(req.ReportType),
			ClinicName:       strings.TrimSpace(req.ClinicName),
			ReportDate:       reportDate,
			SourceImageCount: maxInt(maxInt(len(req.ImageURLs), len(req.ImageBase64)), len(req.ImageObjectKeys)),
			RawPayloadJSON:   "{}",
			SchemaVersion:    "v1",
			FusionVersion:    "v1",
		}
		var (
			observations    []models.ReportObservation
			markdownContent string
		)
		if mode == "markdown" {
			markdownContent = combineMarkdown(vendorMD)
			rawJSON, _ := json.Marshal(map[string]interface{}{
				"extraction_mode":  "markdown",
				"markdown_content": markdownContent,
				"vendor_markdowns": vendorMD,
			})
			report.RawPayloadJSON = string(rawJSON)
			report.FinalReviewStatus = string(models.ReviewStatusPendingReview)
			report.ConsensusScore = 0
			report.OverallConfidence = 0
		} else {
			fusedFields := reportfusion.Fuse(vendorResults, vendorClient.VendorSettings())
			if len(fusedFields) == 0 {
				http.Error(w, "no fields extracted from vendors", http.StatusBadGateway)
				return
			}
			rawJSON, _ := json.Marshal(map[string]interface{}{
				"extraction_mode": "json",
				"vendor_results":  vendorResults,
				"fused_fields":    fusedFields,
			})
			report.RawPayloadJSON = string(rawJSON)
			observations, report.OverallConfidence, report.ConsensusScore, report.FinalReviewStatus = buildObservations(report.ID, fusedFields)
		}

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

			if mode == "markdown" {
				for _, vm := range vendorMD {
					raw, _ := json.Marshal(vm)
					ve := models.ReportVendorExtraction{
						ReportID:        report.ID,
						VendorID:        vm.VendorID,
						Model:           vm.Model,
						FieldCount:      0,
						RawResponseJSON: string(raw),
					}
					if err := tx.Create(&ve).Error; err != nil {
						return err
					}
				}
			} else {
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
		resp := map[string]interface{}{
			"report": report,
		}
		if mode == "markdown" {
			resp["markdown_content"] = markdownContent
			resp["extraction_mode"] = "markdown"
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func extractionTimeout() time.Duration {
	// Keep request timeout aligned with vendor timeout to avoid premature context cancellation.
	secs := 60
	if raw := strings.TrimSpace(os.Getenv("REPORT_VENDOR_TIMEOUT_SECONDS")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			secs = v + 15
		}
	}
	if secs < 60 {
		secs = 60
	}
	return time.Duration(secs) * time.Second
}

func normalizeExtractionMode(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "json":
		return "json"
	default:
		return "markdown"
	}
}

func combineMarkdown(results []reportfusion.VendorMarkdownResult) string {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		md := strings.TrimSpace(r.Markdown)
		if md == "" {
			continue
		}
		parts = append(parts, "## "+r.VendorID+" ("+r.Model+")\n\n"+md)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n---\n\n"))
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

		order := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("order")))
		if order == "" {
			order = "desc"
		}
		if order != "asc" && order != "desc" {
			http.Error(w, "order must be asc or desc", http.StatusBadRequest)
			return
		}
		reportOrder := "report_date DESC, created_at DESC"
		observationOrder := "health_reports.report_date DESC, report_observations.created_at DESC"
		if order == "asc" {
			reportOrder = "report_date ASC, created_at ASC"
			observationOrder = "health_reports.report_date ASC, report_observations.created_at ASC"
		}

		var reports []models.HealthReport
		if err := db.Where("pet_id = ?", petID).
			Order(reportOrder).
			Find(&reports).Error; err != nil {
			http.Error(w, "failed to load reports: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var observations []models.ReportObservation
		if err := db.Joins("JOIN health_reports ON health_reports.id = report_observations.report_id").
			Where("health_reports.pet_id = ? AND report_observations.is_verified = ?", petID, true).
			Order(observationOrder).
			Find(&observations).Error; err != nil {
			http.Error(w, "failed to load observations: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// "latest_by_metric" must always be the newest by report_date, independent of list order.
		var latestCandidates []models.ReportObservation
		if err := db.Joins("JOIN health_reports ON health_reports.id = report_observations.report_id").
			Where("health_reports.pet_id = ? AND report_observations.is_verified = ?", petID, true).
			Order("health_reports.report_date DESC, report_observations.created_at DESC").
			Find(&latestCandidates).Error; err != nil {
			http.Error(w, "failed to load latest observations: "+err.Error(), http.StatusInternalServerError)
			return
		}

		latestByMetric := map[string]models.ReportObservation{}
		for _, obs := range latestCandidates {
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
			"order":                 order,
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
		f = normalizeFusedField(f)
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
			ReferenceRange:      f.ReferenceRange,
			QualitativeResult:   f.QualitativeResult,
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

func normalizeFusedField(f reportfusion.FusedField) reportfusion.FusedField {
	f.MetricKey = strings.ToLower(strings.TrimSpace(f.MetricKey))
	f.ValueText = strings.TrimSpace(f.ValueText)
	f.ReferenceRange = strings.TrimSpace(f.ReferenceRange)
	f.QualitativeResult = strings.TrimSpace(f.QualitativeResult)
	f.Unit = strings.TrimSpace(f.Unit)

	vtLower := strings.ToLower(f.ValueText)
	if vtLower == "noct" || strings.Contains(vtLower, "no ct") {
		f.ValueText = "NoCt"
		if f.QualitativeResult == "" {
			f.QualitativeResult = "阴性"
		}
	}

	if strings.Contains(f.ValueText, "阴性") && f.QualitativeResult == "" {
		f.QualitativeResult = "阴性"
	}
	if strings.Contains(f.ValueText, "阳性") && f.QualitativeResult == "" {
		f.QualitativeResult = "阳性"
	}
	return f
}
