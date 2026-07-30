package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/services/raggo"
)

type goRAGQueryRequest struct {
	Question   string `json:"question"`
	Provider   string `json:"provider"`
	Language   string `json:"language"`
	MaxSources string `json:"max_sources"`
}

func NewGoRAGQueryHandler() http.HandlerFunc {
	cfg := raggo.LoadConfig()
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req goRAGQueryRequest
		switch r.Method {
		case http.MethodGet:
			q := r.URL.Query()
			req.Question = strings.TrimSpace(q.Get("q"))
			req.Provider = q.Get("provider")
			req.Language = q.Get("language")
			req.MaxSources = q.Get("max_sources")
		case http.MethodPost:
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondGoRAGJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_json"})
				return
			}
		}

		if strings.TrimSpace(req.Question) == "" {
			respondGoRAGJSON(w, http.StatusBadRequest, map[string]any{"error": "missing_question"})
			return
		}

		provider, err := raggo.ValidateProvider(req.Provider)
		if err != nil {
			validationErr := err.(*raggo.ValidationError)
			respondGoRAGJSON(w, http.StatusBadRequest, raggo.BuildValidationErrorPayload(validationErr, cfg.MaxAllowedSources))
			return
		}
		language, err := raggo.ValidateLanguage(req.Language)
		if err != nil {
			validationErr := err.(*raggo.ValidationError)
			respondGoRAGJSON(w, http.StatusBadRequest, raggo.BuildValidationErrorPayload(validationErr, cfg.MaxAllowedSources))
			return
		}
		maxSources, err := raggo.ValidateMaxSources(req.MaxSources, cfg.DefaultMaxSources, cfg.MaxAllowedSources)
		if err != nil {
			validationErr := err.(*raggo.ValidationError)
			respondGoRAGJSON(w, http.StatusBadRequest, raggo.BuildValidationErrorPayload(validationErr, cfg.MaxAllowedSources))
			return
		}

		result := raggo.AnswerQuery(cfg, req.Question, provider, language, maxSources)
		respondGoRAGJSON(w, http.StatusOK, result)
	}
}

func NewGoRAGCapabilitiesHandler() http.HandlerFunc {
	cfg := raggo.LoadConfig()
	payload := raggo.BuildCapabilities(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		respondGoRAGJSON(w, http.StatusOK, payload)
	}
}

func NewGoRAGHealthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respondGoRAGJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"service": "go_rag_translation_skeleton",
		})
	}
}

func NewGoRAGReadyzHandler() http.HandlerFunc {
	cfg := raggo.LoadConfig()
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		report := raggo.BuildReadinessReport(cfg)
		status := http.StatusOK
		runtime := "ready"
		if !report.OK {
			status = http.StatusServiceUnavailable
			runtime = "degraded"
		}
		respondGoRAGJSON(w, status, map[string]any{
			"ok":                   report.OK,
			"service":              "go_rag_translation_skeleton",
			"runtime":              runtime,
			"implementation":       "go",
			"llm_configured":       report.LLMConfigured,
			"embedding_enabled":    report.EmbeddingEnabled,
			"embedding_configured": report.EmbeddingConfigured,
			"data_path":            report.DataPath,
			"corpus_available":     report.CorpusAvailable,
			"chunk_count":          report.ChunkCount,
			"issues":               report.Issues,
		})
	}
}

func respondGoRAGJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
