package raggo

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProviderAndLanguage(t *testing.T) {
	if _, err := ValidateProvider("prudential"); err != nil {
		t.Fatalf("expected valid provider: %v", err)
	}
	if _, err := ValidateProvider("bolttech"); err == nil {
		t.Fatalf("expected invalid provider error")
	}
	if _, err := ValidateLanguage("zh"); err != nil {
		t.Fatalf("expected valid language: %v", err)
	}
	if _, err := ValidateLanguage("fr"); err == nil {
		t.Fatalf("expected invalid language error")
	}
}

func TestValidateMaxSources(t *testing.T) {
	got, err := ValidateMaxSources("", 6, 6)
	if err != nil || got != 6 {
		t.Fatalf("expected default max sources 6, got %d err=%v", got, err)
	}
	if _, err := ValidateMaxSources("0", 6, 6); err == nil {
		t.Fatalf("expected validation error for zero")
	}
}

func TestAnswerQueryReturnsFallbackAnswerWhenLLMDisabled(t *testing.T) {
	cfg := LoadConfig()
	cfg.LLMBaseURL = ""
	cfg.LLMModel = ""
	cfg.LLMAPIKey = ""

	result := AnswerQuery(cfg, "What is the meaning of waiting period?", "", "", 3)
	if result.AnswerMode != "go_rag_fallback_summary" {
		t.Fatalf("expected fallback summary mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if strings.TrimSpace(result.Answer) == "" {
		t.Fatalf("expected non-empty fallback answer when llm disabled")
	}
	if len(result.Sources) == 0 {
		t.Fatalf("expected retrieved sources")
	}
	if result.Structured == nil || result.Structured["type"] != "rag_llm_summary_unavailable" {
		t.Fatalf("expected structured no-llm metadata, got %#v", result.Structured)
	}
	if result.Structured["fallback_mode"] != "retrieval_excerpt" {
		t.Fatalf("expected retrieval fallback metadata, got %#v", result.Structured)
	}
}

func TestAnswerQueryUsesRouterGreetingDirectResponse(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount != 1 {
			t.Fatalf("expected only router request, got %d", requestCount)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"route":"direct_response","direct_response_type":"greeting","reason":"simple greeting","confidence":0.99}`},
			}},
		})
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "Hi", "", "en", 3)
	if result.AnswerMode != "direct_response" {
		t.Fatalf("expected direct_response mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if result.Answer != "Hi, how can I assist you today?" {
		t.Fatalf("unexpected direct response answer: %q", result.Answer)
	}
}

func TestAnswerQueryUsesRouterCapabilityDirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"route":"direct_response","direct_response_type":"capability_intro","reason":"asked about assistant capabilities","confidence":0.95}`},
			}},
		})
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "What can you do?", "", "en", 3)
	if result.AnswerMode != "direct_response" {
		t.Fatalf("expected direct_response mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if !strings.Contains(result.Answer, "insurance assistant") {
		t.Fatalf("unexpected capability response: %q", result.Answer)
	}
}

func TestAnswerQueryUsesRouterOutOfScopeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"route":"out_of_scope","direct_response_type":"","reason":"not about pet insurance","confidence":0.97}`},
			}},
		})
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "What is the meaning of least common multiple?", "", "en", 3)
	if result.AnswerMode != "out_of_scope" {
		t.Fatalf("expected out_of_scope mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if !strings.Contains(result.Answer, "outside the scope") {
		t.Fatalf("unexpected out_of_scope response: %q", result.Answer)
	}
}

func TestAnswerQueryUsesLLMSummaryWhenConfigured(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode summarizer request: %v", err)
		}
		if requestCount == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"content": `{"route":"rag_query","direct_response_type":"","reason":"insurance query","confidence":0.92}`},
				}},
			})
			return
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) < 2 {
			t.Fatalf("expected chat messages, got %#v", payload)
		}
		msgMap, _ := messages[1].(map[string]any)
		content, _ := msgMap["content"].(string)
		if !strings.Contains(content, "Evidence snippets") {
			t.Fatalf("expected evidence prompt, got %q", content)
		}
		responseFormat, _ := payload["response_format"].(map[string]any)
		if responseFormat["type"] != "json_object" {
			t.Fatalf("expected json_object response format, got %#v", payload["response_format"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"type":"answer","answer":"LLM grounded summary","needs_clarification":false}`},
			}},
		})
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "What is the meaning of waiting period?", "", "", 3)
	if result.AnswerMode != "go_rag_llm_summary" {
		t.Fatalf("expected llm summary mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if strings.TrimSpace(result.Answer) != "LLM grounded summary" {
		t.Fatalf("unexpected llm answer: %q", result.Answer)
	}
	if len(result.Sources) == 0 {
		t.Fatalf("expected retrieved sources")
	}
	if result.Structured == nil || result.Structured["attempt"] == nil {
		t.Fatalf("expected structured summary attempt metadata, got %#v", result.Structured)
	}
	if requestCount != 2 {
		t.Fatalf("expected router + summarizer requests, got %d", requestCount)
	}
}

func TestAnswerQueryUsesLLMClarificationWhenConfigured(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"content": `{"route":"rag_query","direct_response_type":"","reason":"insurance comparison query","confidence":0.88}`},
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"type":"clarification_needed","needs_clarification":true,"clarification_message":"Please specify which Blue Cross and Prudential plans you want compared.","clarification_options":[{"provider":"bluecross","products":["Love Pet - Type A","Love Pet - Type B"]},{"provider":"prudential","products":["PRUChoice Furkid Care - A","PRUChoice Furkid Care - B"]}]}`},
			}},
		})
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "Compare Blue Cross and Prudential veterinary consultation limits.", "", "", 3)
	if result.AnswerMode != "clarification_needed" {
		t.Fatalf("expected clarification_needed mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if !strings.Contains(result.Answer, "Please specify") {
		t.Fatalf("unexpected clarification answer: %q", result.Answer)
	}
	if result.Structured == nil || result.Structured["type"] != "clarification_needed" {
		t.Fatalf("expected clarification structured payload, got %#v", result.Structured)
	}
}

func TestAnswerQueryFallsBackWhenJSONModeUnsupported(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode summarizer request: %v", err)
		}
		if requestCount == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"content": `{"route":"rag_query","direct_response_type":"","reason":"insurance query","confidence":0.9}`},
				}},
			})
			return
		}
		if requestCount == 2 {
			http.Error(w, `{"code":20024,"message":"Json mode is not supported for this model.","data":null}`, http.StatusBadRequest)
			return
		}
		if _, ok := payload["response_format"]; ok {
			t.Fatalf("did not expect response_format on fallback request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": "```json\n{\"type\":\"answer\",\"answer\":\"Fallback JSON answer\",\"needs_clarification\":false}\n```"},
			}},
		})
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "What is the meaning of waiting period?", "", "", 3)
	if result.AnswerMode != "go_rag_llm_summary" {
		t.Fatalf("expected llm summary mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if result.Answer != "Fallback JSON answer" {
		t.Fatalf("unexpected fallback answer: %q", result.Answer)
	}
	if requestCount != 3 {
		t.Fatalf("expected router + two summarizer requests, got %d", requestCount)
	}
}

func TestAnswerQueryExposesSummaryFailureReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream summary timeout", http.StatusGatewayTimeout)
	}))
	defer server.Close()

	cfg := LoadConfig()
	cfg.LLMBaseURL = server.URL
	cfg.LLMModel = "test-model"
	cfg.LLMAPIKey = "test-key"
	cfg.LLMTimeoutSeconds = 5

	result := AnswerQuery(cfg, "What is the meaning of waiting period?", "", "", 3)
	if result.AnswerMode != "go_rag_fallback_summary" {
		t.Fatalf("expected fallback summary mode, got %s", result.AnswerMode)
	}
	if strings.TrimSpace(result.Answer) == "" {
		t.Fatalf("expected non-empty fallback answer, got empty")
	}
	if result.Structured == nil {
		t.Fatalf("expected structured failure metadata")
	}
	reason, _ := result.Structured["failure_reason"].(string)
	if !strings.Contains(reason, "status 504") {
		t.Fatalf("expected failure reason to mention status 504, got %q", reason)
	}
}

func TestAnswerQueryReturnsFallbackForMedicalLikeInsurancePrompt(t *testing.T) {
	cfg := LoadConfig()
	cfg.LLMBaseURL = ""
	cfg.LLMModel = ""
	cfg.LLMAPIKey = ""

	result := AnswerQuery(cfg, "Pet medical consultation\n\nUser Question: Hi", "", "", 3)
	if result.AnswerMode != "go_rag_fallback_summary" {
		t.Fatalf("expected fallback summary mode, got %s answer=%q", result.AnswerMode, result.Answer)
	}
	if strings.TrimSpace(result.Answer) == "" {
		t.Fatalf("expected non-empty fallback answer")
	}
	if strings.Contains(strings.ToLower(result.Answer), "service unavailable") {
		t.Fatalf("fallback answer should not surface service-unavailable text, got %q", result.Answer)
	}
}

func TestShouldRetrySummaryError(t *testing.T) {
	if shouldRetrySummaryError(nil) != true {
		t.Fatalf("nil error should be retryable")
	}
	if shouldRetrySummaryError(errors.New("empty_summary_content")) != true {
		t.Fatalf("empty content should be retryable")
	}
	if shouldRetrySummaryError(errors.New("status 504 body=timeout")) != false {
		t.Fatalf("504 should not be retryable")
	}
	if shouldRetrySummaryError(errors.New("context deadline exceeded")) != false {
		t.Fatalf("timeout should not be retryable")
	}
}

func TestDefaultDisclaimerFollowsRequestedLanguage(t *testing.T) {
	if got := defaultDisclaimer("zh", "Compare plans"); !strings.Contains(got, "仅供参考") {
		t.Fatalf("expected zh disclaimer, got %q", got)
	}
	if got := defaultDisclaimer("en", "Compare plans"); !strings.Contains(got, "For reference only") {
		t.Fatalf("expected en disclaimer, got %q", got)
	}
}

func TestDefaultDisclaimerFallsBackToQuestionLanguage(t *testing.T) {
	if got := defaultDisclaimer("", "Blue Cross 包唔包獸醫診症？"); !strings.Contains(got, "仅供参考") {
		t.Fatalf("expected zh disclaimer from han question, got %q", got)
	}
	if got := defaultDisclaimer("", "What is the meaning of waiting period?"); !strings.Contains(got, "For reference only") {
		t.Fatalf("expected en disclaimer from english question, got %q", got)
	}
}

func TestRerankerReordersCandidates(t *testing.T) {
	rerankServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode rerank request: %v", err)
		}
		if payload["query"] != "room and board" {
			t.Fatalf("unexpected rerank query: %#v", payload["query"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"index": 1, "relevance_score": 0.99},
				{"index": 0, "relevance_score": 0.10},
			},
		})
	}))
	defer rerankServer.Close()

	r := newReranker(Config{
		RerankEnabled:        true,
		RerankBaseURL:        rerankServer.URL,
		RerankModel:          "rerank-model",
		RerankAPIKey:         "rerank-key",
		RerankTopN:           2,
		RerankTimeoutSeconds: 5,
	})
	if r == nil {
		t.Fatalf("expected reranker")
	}

	candidates := []rankedChunk{
		{
			chunk: Chunk{
				Text: "first candidate",
				Metadata: map[string]string{
					"provider":     "prudential",
					"source_name":  "a.md",
					"section_path": "Section A",
				},
			},
			score: 1,
		},
		{
			chunk: Chunk{
				Text: "second candidate",
				Metadata: map[string]string{
					"provider":     "prudential",
					"source_name":  "b.md",
					"section_path": "Section B",
				},
			},
			score: 1,
		},
	}
	reordered, err := r.rerank("room and board", candidates)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(reordered) < 2 {
		t.Fatalf("expected reranked candidates")
	}
	if reordered[0].chunk.Metadata["source_name"] != "b.md" {
		t.Fatalf("expected reranked first source b.md, got %s", reordered[0].chunk.Metadata["source_name"])
	}
}

func TestLoadChunks_FromNormalizedCorpus(t *testing.T) {
	cfg := LoadConfig()
	chunks, err := LoadChunks(cfg)
	if err != nil {
		t.Fatalf("expected corpus to load, got error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected non-empty chunk set from %s", cfg.DataPath)
	}
}

func TestLoadChunks_DoesNotIncludeAggregatedSummaryChunks(t *testing.T) {
	cfg := LoadConfig()
	chunks, err := LoadChunks(cfg)
	if err != nil {
		t.Fatalf("load chunks: %v", err)
	}

	for _, ch := range chunks {
		switch ch.Metadata["source_name"] {
		case "structured_waiting_period_summary", "structured_sub_coverage_summary", "concept_merged_evidence":
			t.Fatalf("unexpected deterministic summary chunk still present: %s", ch.Metadata["source_name"])
		}
	}
}

func TestLoadChunks_UsesStableInProcessCache(t *testing.T) {
	cfg := LoadConfig()
	first, err := LoadChunks(cfg)
	if err != nil {
		t.Fatalf("first load chunks: %v", err)
	}
	second, err := LoadChunks(cfg)
	if err != nil {
		t.Fatalf("second load chunks: %v", err)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected cached chunk results")
	}
	if len(first) != len(second) {
		t.Fatalf("expected cached chunk count to match, got %d vs %d", len(first), len(second))
	}
}

func TestLoadConfig_FallsBackToNormalizedCorpusWhenEnvPathMissing(t *testing.T) {
	original := os.Getenv("HK_INSURANCE_RAG_DATA_PATH")
	t.Cleanup(func() {
		_ = os.Setenv("HK_INSURANCE_RAG_DATA_PATH", original)
	})

	_ = os.Setenv("HK_INSURANCE_RAG_DATA_PATH", "assets/non_existing_corpus_path_for_test")
	cfg := LoadConfig()
	if !strings.Contains(filepath.ToSlash(cfg.DataPath), "assets/rag_normalized/hk_insurance") {
		t.Fatalf("expected fallback to normalized corpus path, got %s", cfg.DataPath)
	}
}

func TestRankCandidatesUsesPlainLexicalRetrieval(t *testing.T) {
	chunks := []Chunk{
		{Text: "Blue Cross waiting period is 90 days for illness.", Metadata: map[string]string{"provider": "bluecross", "language": "en", "source_name": "a.md", "section_path": "Definitions"}},
		{Text: "Prudential covers room and board.", Metadata: map[string]string{"provider": "prudential", "language": "en", "source_name": "b.md", "section_path": "Benefits"}},
	}

	got := rankCandidates(chunks, "Blue Cross waiting period", "", "", 2)
	if len(got) == 0 {
		t.Fatalf("expected candidates")
	}
	if got[0].chunk.Metadata["provider"] != "bluecross" {
		t.Fatalf("expected lexical top hit to be bluecross, got %s", got[0].chunk.Metadata["provider"])
	}
}

func TestRankCandidatesPrefersBenefitOverExclusionForChineseConsultQuery(t *testing.T) {
	chunks := []Chunk{
		{
			Text: "本保單不承保一般不保事項，但未說明是否包含獸醫診症保障。",
			Metadata: map[string]string{
				"provider":     "bluecross",
				"language":     "zh",
				"source_name":  "exclusion.md",
				"section_path": "一般不保事項",
				"unit_types":   "exclusion",
				"topic_tags":   "exclusion",
			},
		},
		{
			Text: "於受保期內因疾病或受傷而接受獸醫診症時的所有獸醫費用均受保障。",
			Metadata: map[string]string{
				"provider":     "bluecross",
				"language":     "zh",
				"source_name":  "consult.md",
				"section_path": "第一部分 > C) 獸醫診症",
				"unit_types":   "benefit",
				"topic_tags":   "benefit,consult",
			},
		},
	}

	got := rankCandidates(chunks, "Blue Cross 包唔包獸醫診症？", "bluecross", "zh", 3)
	if len(got) < 2 {
		t.Fatalf("expected ranked candidates")
	}
	if got[0].chunk.Metadata["source_name"] != "consult.md" {
		t.Fatalf("expected consult benefit first, got %s", got[0].chunk.Metadata["source_name"])
	}
}

func TestRankCandidatesPenalizesGeneralExclusionNoiseForCoverageQuestion(t *testing.T) {
	chunks := []Chunk{
		{
			Text: "This policy excludes some situations but does not describe vet consultation coverage.",
			Metadata: map[string]string{
				"provider":     "bluecross",
				"language":     "en",
				"source_name":  "general-exclusions.md",
				"section_path": "General Exclusions",
				"unit_types":   "exclusion",
				"topic_tags":   "exclusion",
			},
		},
		{
			Text: "All Vet Expenses made for the consultation carried out by a Vet during the Period of Insurance for Illness or Injury are covered.",
			Metadata: map[string]string{
				"provider":     "bluecross",
				"language":     "en",
				"source_name":  "consult-benefit.md",
				"section_path": "Benefits > C) Vet Consultation",
				"unit_types":   "benefit",
				"topic_tags":   "benefit,consult",
			},
		},
	}

	got := rankCandidates(chunks, "Does Blue Cross cover vet consultation?", "bluecross", "en", 3)
	if len(got) < 2 {
		t.Fatalf("expected ranked candidates")
	}
	if got[0].chunk.Metadata["source_name"] != "consult-benefit.md" {
		t.Fatalf("expected consult benefit first, got %s", got[0].chunk.Metadata["source_name"])
	}
}

func TestDedupeCandidatesRemovesDuplicateEvidenceWindows(t *testing.T) {
	candidates := []rankedChunk{
		{
			chunk: Chunk{
				Text: "Blue Cross consultation limit HK$8,000 per year.",
				Metadata: map[string]string{
					"provider":     "bluecross",
					"source_name":  "structured_sub_coverage_limit",
					"section_path": "Structured Product Data > Coverage Limits > Veterinary Consultation",
				},
			},
			score: 10,
		},
		{
			chunk: Chunk{
				Text: "Blue Cross consultation limit HK$8,000 per year.",
				Metadata: map[string]string{
					"provider":     "bluecross",
					"source_name":  "structured_sub_coverage_limit",
					"section_path": "Structured Product Data > Coverage Limits > Veterinary Consultation",
				},
			},
			score: 9,
		},
	}

	got := dedupeCandidates(candidates)
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped candidate, got %d", len(got))
	}
}

func TestCollapseStructuredCandidatesKeepsOnlyStrongestStructuredSectionDuplicate(t *testing.T) {
	candidates := []rankedChunk{
		{
			chunk: Chunk{
				Text: "Blue Cross consultation limit HK$12,000 per year.",
				Metadata: map[string]string{
					"provider":     "bluecross",
					"language":     "en",
					"source_name":  "structured_sub_coverage_limit",
					"section_path": "Structured Product Data > Coverage Limits > Veterinary Consultation",
				},
			},
			score: 12,
		},
		{
			chunk: Chunk{
				Text: "Blue Cross consultation limit HK$8,000 per year.",
				Metadata: map[string]string{
					"provider":     "bluecross",
					"language":     "en",
					"source_name":  "structured_sub_coverage_limit",
					"section_path": "Structured Product Data > Coverage Limits > Veterinary Consultation",
				},
			},
			score: 11,
		},
		{
			chunk: Chunk{
				Text: "Blue Cross room and board limit HK$5,000 per year.",
				Metadata: map[string]string{
					"provider":     "bluecross",
					"language":     "en",
					"source_name":  "structured_sub_coverage_limit",
					"section_path": "Structured Product Data > Coverage Limits > Room and Board",
				},
			},
			score: 10,
		},
	}

	got := collapseStructuredCandidates(candidates)
	if len(got) != 2 {
		t.Fatalf("expected 2 structured candidates after collapse, got %d", len(got))
	}
	if got[0].chunk.Text != "Blue Cross consultation limit HK$12,000 per year." {
		t.Fatalf("expected strongest consultation row kept first, got %q", got[0].chunk.Text)
	}
}

func TestDiversifyCandidatesPrefersProviderCoverageWithoutFilter(t *testing.T) {
	candidates := []rankedChunk{
		{chunk: Chunk{Text: "p1", Metadata: map[string]string{"provider": "prudential", "source_name": "a", "section_path": "A"}}, score: 10},
		{chunk: Chunk{Text: "p2", Metadata: map[string]string{"provider": "prudential", "source_name": "b", "section_path": "B"}}, score: 9},
		{chunk: Chunk{Text: "b1", Metadata: map[string]string{"provider": "bluecross", "source_name": "c", "section_path": "C"}}, score: 8},
	}

	got := diversifyCandidates(candidates, "")
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	if got[0].chunk.Metadata["provider"] != "prudential" {
		t.Fatalf("expected first provider prudential, got %s", got[0].chunk.Metadata["provider"])
	}
	if got[1].chunk.Metadata["provider"] != "bluecross" {
		t.Fatalf("expected second provider bluecross, got %s", got[1].chunk.Metadata["provider"])
	}
}

func TestRankCandidatesPrefersDefinitionOverStructuredWaitingPeriodRows(t *testing.T) {
	chunks := []Chunk{
		{
			Text: "Waiting period means the period from the policy start date before benefits become available.",
			Metadata: map[string]string{
				"provider":     "one_degree",
				"language":     "en",
				"source_name":  "policy.md",
				"section_path": "Definitions > Definition: Waiting Period",
				"unit_types":   "definition",
				"topic_tags":   "definition,waiting_period",
			},
		},
		{
			Text: "Plan A: waiting period 30 days for illness, 7 days for injury.",
			Metadata: map[string]string{
				"provider":     "prudential",
				"language":     "en",
				"source_name":  "structured_product_waiting_period",
				"section_path": "Structured Product Data > Waiting Period",
				"unit_types":   "waiting_period",
				"topic_tags":   "structured_product,waiting_period",
			},
		},
	}

	got := rankCandidates(chunks, "What is the meaning of waiting period?", "", "en", 3)
	if len(got) < 2 {
		t.Fatalf("expected ranked candidates")
	}
	if got[0].chunk.Metadata["unit_types"] != "definition" {
		t.Fatalf("expected definition chunk first, got %s", got[0].chunk.Metadata["unit_types"])
	}
}

func TestBudgetSourcesForSummaryCompactsSnippetsToSharedBudget(t *testing.T) {
	sources := []Source{
		{Snippet: strings.Repeat("a", 800)},
		{Snippet: strings.Repeat("b", 800)},
		{Snippet: strings.Repeat("c", 800)},
	}

	got := budgetSourcesForSummary(sources, 900)
	if len(got) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(got))
	}
	for i, src := range got {
		if len(src.Snippet) > 520 {
			t.Fatalf("source %d snippet should be compacted, len=%d", i, len(src.Snippet))
		}
	}
}

func TestBudgetSourcesForSummaryCompressesStructuredRowsMoreAggressively(t *testing.T) {
	sources := []Source{
		{SourceName: "policy.md", Snippet: strings.Repeat("a", 800)},
		{SourceName: "structured_sub_coverage_limit", Snippet: strings.Repeat("b", 800)},
	}

	got := budgetSourcesForSummary(sources, 500)
	if len(got) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(got))
	}
	if len(got[1].Snippet) >= len(got[0].Snippet) {
		t.Fatalf("expected structured snippet to be compressed more aggressively, got %d vs %d", len(got[1].Snippet), len(got[0].Snippet))
	}
}

func TestBuildSummarizerResultMapsClarificationEnvelope(t *testing.T) {
	envelope := llmAnswerEnvelope{
		Type:                 "clarification_needed",
		NeedsClarification:   true,
		ClarificationMessage: "Please specify the plan.",
		ClarificationOptions: []providerOption{
			{Provider: "bluecross", Products: []string{"Love Pet - Type A", "Love Pet - Type B"}},
		},
	}

	answer, mode, structured := buildSummarizerResult(envelope, Config{LLMModel: "test-model"}, 3, summarizeAttempt{name: "primary", sourceMode: "budget_900"})
	if mode != "clarification_needed" {
		t.Fatalf("expected clarification_needed mode, got %s", mode)
	}
	if answer != "Please specify the plan." {
		t.Fatalf("unexpected clarification answer: %q", answer)
	}
	if structured["type"] != "clarification_needed" {
		t.Fatalf("expected clarification structured payload, got %#v", structured)
	}
}
