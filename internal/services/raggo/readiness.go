package raggo

import (
	"os"
	"strings"
)

type ReadinessReport struct {
	OK                  bool     `json:"ok"`
	LLMConfigured       bool     `json:"llm_configured"`
	EmbeddingEnabled    bool     `json:"embedding_enabled"`
	EmbeddingConfigured bool     `json:"embedding_configured"`
	DataPath            string   `json:"data_path"`
	CorpusAvailable     bool     `json:"corpus_available"`
	ChunkCount          int      `json:"chunk_count"`
	Issues              []string `json:"issues,omitempty"`
}

func BuildReadinessReport(cfg Config) ReadinessReport {
	report := ReadinessReport{
		OK:                  true,
		LLMConfigured:       strings.TrimSpace(cfg.LLMBaseURL) != "" && strings.TrimSpace(cfg.LLMModel) != "" && strings.TrimSpace(cfg.LLMAPIKey) != "",
		EmbeddingEnabled:    cfg.EmbeddingEnabled,
		EmbeddingConfigured: strings.TrimSpace(cfg.EmbeddingBaseURL) != "" && strings.TrimSpace(cfg.EmbeddingModel) != "" && strings.TrimSpace(cfg.EmbeddingAPIKey) != "",
		DataPath:            cfg.DataPath,
	}

	if !report.LLMConfigured {
		report.OK = false
		report.Issues = append(report.Issues, "missing HK_INSURANCE_RAG_LLM_* configuration")
	}
	if report.EmbeddingEnabled && !report.EmbeddingConfigured {
		report.OK = false
		report.Issues = append(report.Issues, "embedding enabled but HK_INSURANCE_RAG_EMBEDDING_* configuration is incomplete")
	}

	if strings.TrimSpace(cfg.DataPath) == "" {
		report.OK = false
		report.Issues = append(report.Issues, "HK insurance RAG data path is empty")
		return report
	}

	if _, err := os.Stat(cfg.DataPath); err != nil {
		report.OK = false
		report.Issues = append(report.Issues, "RAG corpus path is not accessible")
		return report
	}

	chunks, err := LoadChunks(cfg)
	if err != nil {
		report.OK = false
		report.Issues = append(report.Issues, "failed to load RAG chunks: "+err.Error())
		return report
	}

	report.ChunkCount = len(chunks)
	report.CorpusAvailable = len(chunks) > 0
	if !report.CorpusAvailable {
		report.OK = false
		report.Issues = append(report.Issues, "RAG corpus loaded zero chunks")
	}

	return report
}
