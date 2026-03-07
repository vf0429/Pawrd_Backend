package reportfusion

import (
	"math"
	"sort"
	"strings"
)

type Field struct {
	MetricKey         string    `json:"metric_key"`
	ValueNumber       *float64  `json:"value_number,omitempty"`
	ValueText         string    `json:"value_text,omitempty"`
	Unit              string    `json:"unit,omitempty"`
	ReferenceRange    string    `json:"reference_range,omitempty"`
	QualitativeResult string    `json:"qualitative_result,omitempty"`
	Confidence        float64   `json:"confidence"`
	SourcePage        int       `json:"source_page,omitempty"`
	SourceLine        string    `json:"source_line,omitempty"`
	SourceBBox        []float64 `json:"source_bbox,omitempty"`
}

type VendorResult struct {
	VendorID string  `json:"vendor_id"`
	Model    string  `json:"model"`
	Fields   []Field `json:"fields"`
}

type VendorSetting struct {
	VendorID    string
	Reliability float64
}

type ReviewStatus string

const (
	AutoPass              ReviewStatus = "auto_pass"
	PendingReview         ReviewStatus = "pending_review"
	ManualConfirmRequired ReviewStatus = "manual_confirm_required"
)

type FusedField struct {
	MetricKey           string       `json:"metric_key"`
	ValueNumber         *float64     `json:"value_number,omitempty"`
	ValueText           string       `json:"value_text,omitempty"`
	Unit                string       `json:"unit,omitempty"`
	ReferenceRange      string       `json:"reference_range,omitempty"`
	QualitativeResult   string       `json:"qualitative_result,omitempty"`
	FusionConfidence    float64      `json:"fusion_confidence"`
	ConsensusScore      float64      `json:"consensus_score"`
	ReviewStatus        ReviewStatus `json:"review_status"`
	ContributingVendors []string     `json:"contributing_vendors"`
	SourcePage          int          `json:"source_page,omitempty"`
	SourceLine          string       `json:"source_line,omitempty"`
	SourceBBox          []float64    `json:"source_bbox,omitempty"`
}

func Fuse(results []VendorResult, settings []VendorSetting) []FusedField {
	weights := map[string]float64{}
	for _, s := range settings {
		weights[s.VendorID] = clamp01(s.Reliability)
	}

	grouped := map[string][]struct {
		vendorID string
		field    Field
		weight   float64
	}{}
	for _, vr := range results {
		baseWeight, ok := weights[vr.VendorID]
		if !ok {
			baseWeight = 0.8
		}
		for _, f := range vr.Fields {
			key := canonicalMetricKey(f.MetricKey)
			grouped[key] = append(grouped[key], struct {
				vendorID string
				field    Field
				weight   float64
			}{
				vendorID: vr.VendorID,
				field:    f,
				weight:   clamp01(f.Confidence) * max(baseWeight, 0.01),
			})
		}
	}

	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]FusedField, 0, len(keys))
	for _, key := range keys {
		items := grouped[key]
		if len(items) == 0 {
			continue
		}

		consensus := consensus(items)
		vendors := uniqueSortedVendors(items)
		status := decideStatus(consensus, len(vendors))
		picked := pickValue(items)
		confidence := averageWeight(items)
		out = append(out, FusedField{
			MetricKey:           key,
			ValueNumber:         picked.ValueNumber,
			ValueText:           picked.ValueText,
			Unit:                picked.Unit,
			ReferenceRange:      picked.ReferenceRange,
			QualitativeResult:   picked.QualitativeResult,
			FusionConfidence:    confidence,
			ConsensusScore:      consensus,
			ReviewStatus:        status,
			ContributingVendors: vendors,
			SourcePage:          picked.SourcePage,
			SourceLine:          picked.SourceLine,
			SourceBBox:          picked.SourceBBox,
		})
	}

	return out
}

func decideStatus(consensus float64, vendorCount int) ReviewStatus {
	if vendorCount < 2 {
		return ManualConfirmRequired
	}
	switch {
	case consensus >= 0.85:
		return AutoPass
	case consensus >= 0.55:
		return PendingReview
	default:
		return ManualConfirmRequired
	}
}

func consensus(items []struct {
	vendorID string
	field    Field
	weight   float64
}) float64 {
	if len(items) <= 1 {
		return 0.5
	}
	scores := make([]float64, 0, len(items))
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			scores = append(scores, similarity(items[i].field, items[j].field))
		}
	}
	return average(scores)
}

func pickValue(items []struct {
	vendorID string
	field    Field
	weight   float64
}) Field {
	totalWeight := 0.0
	weightedSum := 0.0
	unitBuckets := map[string]float64{}
	sourceField := items[0].field
	numericCount := 0
	for _, it := range items {
		if it.field.ValueNumber == nil {
			continue
		}
		numericCount++
		w := max(it.weight, 0.01)
		totalWeight += w
		weightedSum += (*it.field.ValueNumber) * w
		if it.field.Unit != "" {
			unitBuckets[it.field.Unit] += w
		}
		if it.field.Confidence > sourceField.Confidence {
			sourceField = it.field
		}
	}
	if numericCount > 0 && totalWeight > 0 {
		v := weightedSum / totalWeight
		return Field{
			MetricKey:         sourceField.MetricKey,
			ValueNumber:       &v,
			Unit:              topText(unitBuckets),
			ReferenceRange:    majorityFieldText(items, func(f Field) string { return f.ReferenceRange }),
			QualitativeResult: majorityFieldText(items, func(f Field) string { return f.QualitativeResult }),
			Confidence:        sourceField.Confidence,
			SourcePage:        sourceField.SourcePage,
			SourceLine:        sourceField.SourceLine,
			SourceBBox:        sourceField.SourceBBox,
		}
	}

	textBuckets := map[string]float64{}
	for _, it := range items {
		txt := strings.TrimSpace(strings.ToLower(it.field.ValueText))
		if txt == "" {
			continue
		}
		textBuckets[txt] += max(it.weight, 0.01)
		if it.field.Confidence > sourceField.Confidence {
			sourceField = it.field
		}
		if it.field.Unit != "" {
			unitBuckets[it.field.Unit] += max(it.weight, 0.01)
		}
	}

	return Field{
		MetricKey:         sourceField.MetricKey,
		ValueText:         topText(textBuckets),
		Unit:              topText(unitBuckets),
		ReferenceRange:    majorityFieldText(items, func(f Field) string { return f.ReferenceRange }),
		QualitativeResult: majorityFieldText(items, func(f Field) string { return f.QualitativeResult }),
		Confidence:        sourceField.Confidence,
		SourcePage:        sourceField.SourcePage,
		SourceLine:        sourceField.SourceLine,
		SourceBBox:        sourceField.SourceBBox,
	}
}

func similarity(a, b Field) float64 {
	if a.ValueNumber != nil && b.ValueNumber != nil {
		av := *a.ValueNumber
		bv := *b.ValueNumber
		maxV := max(math.Abs(av), math.Abs(bv), 0.0001)
		relativeDiff := math.Abs(av-bv) / maxV
		switch {
		case relativeDiff <= 0.03:
			return 1.0
		case relativeDiff <= 0.10:
			return 0.65
		default:
			return 0.2
		}
	}

	at := strings.TrimSpace(strings.ToLower(a.ValueText))
	bt := strings.TrimSpace(strings.ToLower(b.ValueText))
	if at == "" || bt == "" {
		return 0.2
	}
	if at == bt {
		return 1.0
	}
	if strings.Contains(at, bt) || strings.Contains(bt, at) {
		return 0.65
	}
	return 0.2
}

func canonicalMetricKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func topText(buckets map[string]float64) string {
	bestK := ""
	bestV := -1.0
	for k, v := range buckets {
		if v > bestV {
			bestV = v
			bestK = k
		}
	}
	return bestK
}

func majorityFieldText(items []struct {
	vendorID string
	field    Field
	weight   float64
}, selector func(Field) string) string {
	buckets := map[string]float64{}
	for _, it := range items {
		s := strings.TrimSpace(selector(it.field))
		if s == "" {
			continue
		}
		buckets[s] += max(it.weight, 0.01)
	}
	return topText(buckets)
}

func averageWeight(items []struct {
	vendorID string
	field    Field
	weight   float64
}) float64 {
	if len(items) == 0 {
		return 0
	}
	sum := 0.0
	for _, it := range items {
		sum += it.weight
	}
	return sum / float64(len(items))
}

func uniqueSortedVendors(items []struct {
	vendorID string
	field    Field
	weight   float64
}) []string {
	m := map[string]struct{}{}
	for _, it := range items {
		m[it.vendorID] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func max(values ...float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
