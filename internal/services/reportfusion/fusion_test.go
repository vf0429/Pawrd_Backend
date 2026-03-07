package reportfusion

import "testing"

func floatPtr(v float64) *float64 { return &v }

func TestFuse_HighConsistency_AutoPass(t *testing.T) {
	results := []VendorResult{
		{VendorID: "a", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(100), Confidence: 0.9}}},
		{VendorID: "b", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(101), Confidence: 0.9}}},
		{VendorID: "c", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(99), Confidence: 0.9}}},
	}
	out := Fuse(results, []VendorSetting{
		{VendorID: "a", Reliability: 0.8},
		{VendorID: "b", Reliability: 0.8},
		{VendorID: "c", Reliability: 0.8},
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 fused field, got %d", len(out))
	}
	if out[0].ReviewStatus != AutoPass {
		t.Fatalf("expected auto pass, got %s", out[0].ReviewStatus)
	}
}

func TestFuse_CloseConflict_PendingReview(t *testing.T) {
	results := []VendorResult{
		{VendorID: "a", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(100), Confidence: 0.9}}},
		{VendorID: "b", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(109), Confidence: 0.9}}},
		{VendorID: "c", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(100), Confidence: 0.9}}},
	}
	out := Fuse(results, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 fused field, got %d", len(out))
	}
	if out[0].ReviewStatus != PendingReview {
		t.Fatalf("expected pending review, got %s", out[0].ReviewStatus)
	}
}

func TestFuse_SevereConflict_ManualConfirm(t *testing.T) {
	results := []VendorResult{
		{VendorID: "a", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(100), Confidence: 0.9}}},
		{VendorID: "b", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(160), Confidence: 0.9}}},
		{VendorID: "c", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(200), Confidence: 0.9}}},
	}
	out := Fuse(results, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 fused field, got %d", len(out))
	}
	if out[0].ReviewStatus != ManualConfirmRequired {
		t.Fatalf("expected manual confirm required, got %s", out[0].ReviewStatus)
	}
}

func TestFuse_SingleVendor_AlwaysManualConfirm(t *testing.T) {
	results := []VendorResult{
		{VendorID: "only_one", Fields: []Field{{MetricKey: "ALT", ValueNumber: floatPtr(88), Confidence: 1}}},
	}
	out := Fuse(results, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 fused field, got %d", len(out))
	}
	if out[0].ConsensusScore != 0.5 {
		t.Fatalf("expected consensus 0.5 for single vendor, got %v", out[0].ConsensusScore)
	}
	if out[0].ReviewStatus != ManualConfirmRequired {
		t.Fatalf("expected manual confirm required for single vendor, got %s", out[0].ReviewStatus)
	}
}
