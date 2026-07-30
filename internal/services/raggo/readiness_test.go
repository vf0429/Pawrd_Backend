package raggo

import (
	"os"
	"strings"
	"testing"
)

func TestBuildReadinessReportFlagsMissingLLMConfig(t *testing.T) {
	cfg := LoadConfig()
	cfg.LLMBaseURL = ""
	cfg.LLMModel = ""
	cfg.LLMAPIKey = ""

	report := BuildReadinessReport(cfg)
	if report.OK {
		t.Fatalf("expected readiness failure when llm config is missing")
	}
	if report.LLMConfigured {
		t.Fatalf("expected llm_configured=false")
	}
	if len(report.Issues) == 0 || !strings.Contains(report.Issues[0], "HK_INSURANCE_RAG_LLM") {
		t.Fatalf("expected llm issue, got %#v", report.Issues)
	}
}

func TestBuildReadinessReportFlagsMissingCorpusPath(t *testing.T) {
	cfg := LoadConfig()
	cfg.DataPath = "assets/non_existing_corpus_path_for_readiness_test"

	report := BuildReadinessReport(cfg)
	if report.OK {
		t.Fatalf("expected readiness failure when corpus path is missing")
	}
	if report.CorpusAvailable {
		t.Fatalf("expected corpus_available=false")
	}
	if len(report.Issues) == 0 || !strings.Contains(strings.Join(report.Issues, " | "), "RAG corpus path") {
		t.Fatalf("expected corpus path issue, got %#v", report.Issues)
	}
}

func TestBuildReadinessReportFlagsIncompleteEnabledEmbedding(t *testing.T) {
	cfg := LoadConfig()
	cfg.EmbeddingEnabled = true
	cfg.EmbeddingBaseURL = ""
	cfg.EmbeddingModel = ""
	cfg.EmbeddingAPIKey = ""

	report := BuildReadinessReport(cfg)
	if report.OK {
		t.Fatalf("expected readiness failure when enabled embedding config is incomplete")
	}
	if !report.EmbeddingEnabled || report.EmbeddingConfigured {
		t.Fatalf("unexpected embedding readiness state: %#v", report)
	}
	if !strings.Contains(strings.Join(report.Issues, " | "), "EMBEDDING") {
		t.Fatalf("expected embedding issue, got %#v", report.Issues)
	}
}

func TestBuildReadinessReportPassesWhenConfigAndCorpusExist(t *testing.T) {
	cfg := LoadConfig()
	cfg.LLMBaseURL = "https://example.com/v1"
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"

	if _, err := os.Stat(cfg.DataPath); err != nil {
		t.Fatalf("expected corpus path to exist for readiness test: %v", err)
	}

	report := BuildReadinessReport(cfg)
	if !report.OK {
		t.Fatalf("expected readiness success, got issues=%#v", report.Issues)
	}
	if !report.CorpusAvailable || report.ChunkCount == 0 {
		t.Fatalf("expected non-empty corpus availability, got %#v", report)
	}
}
