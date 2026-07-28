package raggo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

type Source struct {
	Provider    string  `json:"provider"`
	Language    string  `json:"language"`
	Product     string  `json:"product"`
	SourceName  string  `json:"source_name"`
	SectionPath string  `json:"section_path"`
	Clauses     string  `json:"clauses"`
	UnitTypes   string  `json:"unit_types"`
	TopicTags   string  `json:"topic_tags"`
	Score       float64 `json:"score"`
	Snippet     string  `json:"snippet"`
}

type AnswerResult struct {
	Question       string         `json:"question"`
	Provider       string         `json:"provider,omitempty"`
	Language       string         `json:"language,omitempty"`
	Intent         string         `json:"intent"`
	Answer         string         `json:"answer"`
	AnswerMode     string         `json:"answer_mode"`
	Structured     map[string]any `json:"structured_answer,omitempty"`
	Disclaimer     string         `json:"disclaimer"`
	Sources        []Source       `json:"sources"`
	ProcessingMS   int64          `json:"processing_ms"`
	Implementation string         `json:"implementation"`
}

type rankedChunk struct {
	chunk Chunk
	score float64
}

type llmSummarizer struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type llmRouter struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type reranker struct {
	baseURL string
	apiKey  string
	model   string
	topN    int
	client  *http.Client
}

type summarizeAttempt struct {
	name       string
	sources    []Source
	sourceMode string
}

type llmAnswerEnvelope struct {
	Type                 string           `json:"type"`
	Answer               string           `json:"answer"`
	NeedsClarification   bool             `json:"needs_clarification"`
	ClarificationMessage string           `json:"clarification_message"`
	ClarificationOptions []providerOption `json:"clarification_options"`
}

type providerOption struct {
	Provider string   `json:"provider"`
	Products []string `json:"products"`
}

type routeEnvelope struct {
	Route              string  `json:"route"`
	DirectResponseType string  `json:"direct_response_type"`
	Reason             string  `json:"reason"`
	Confidence         float64 `json:"confidence"`
}

func AnswerQuery(cfg Config, question, provider, language string, maxSources int) AnswerResult {
	started := time.Now()
	question = strings.TrimSpace(question)
	resolvedLanguage := responseLanguage(language, question)
	disclaimer := defaultDisclaimer(resolvedLanguage, question)

	if answer, mode, structured, ok := routeQuestion(cfg, question, resolvedLanguage); ok {
		return AnswerResult{
			Question:       question,
			Provider:       provider,
			Language:       resolvedLanguage,
			Intent:         "retrieval",
			Answer:         answer,
			AnswerMode:     mode,
			Structured:     structured,
			Disclaimer:     disclaimer,
			ProcessingMS:   time.Since(started).Milliseconds(),
			Implementation: "go_rag_llm_summary_v1",
		}
	}

	chunks, err := LoadChunks(cfg)
	if err != nil {
		return AnswerResult{
			Question:       question,
			Provider:       provider,
			Language:       resolvedLanguage,
			Intent:         "retrieval",
			Answer:         "RAG corpus loading failed in Go runtime: " + err.Error(),
			AnswerMode:     "go_error",
			Disclaimer:     disclaimer,
			ProcessingMS:   time.Since(started).Milliseconds(),
			Implementation: "go_rag_llm_summary_v1",
		}
	}
	candidates := rankCandidates(chunks, question, provider, language, maxSources)
	candidates = rerankCandidates(cfg, question, candidates)
	candidates = dedupeCandidates(candidates)
	candidates = collapseStructuredCandidates(candidates)
	candidates = diversifyCandidates(candidates, provider)
	candidates = trimCandidates(candidates, maxSources)
	sources := make([]Source, 0, len(candidates))
	for _, c := range candidates {
		sources = append(sources, buildSourcePayload(c.chunk, c.score))
	}

	answer, mode, structured := summarizeSources(cfg, question, provider, language, sources)
	if strings.TrimSpace(answer) == "" {
		answer = fallbackRetrievalAnswer(question, resolvedLanguage, sources)
		if strings.TrimSpace(answer) != "" {
			mode = "go_rag_fallback_summary"
			if structured == nil {
				structured = map[string]any{}
			}
			structured["type"] = "rag_llm_summary_unavailable"
			structured["fallback_mode"] = "retrieval_excerpt"
			structured["source_count"] = len(sources)
		}
	}

	return AnswerResult{
		Question:       question,
		Provider:       provider,
		Language:       resolvedLanguage,
		Intent:         "retrieval",
		Answer:         answer,
		AnswerMode:     mode,
		Structured:     structured,
		Disclaimer:     disclaimer,
		Sources:        sources,
		ProcessingMS:   time.Since(started).Milliseconds(),
		Implementation: "go_rag_llm_summary_v1",
	}
}

func routeQuestion(cfg Config, question, language string) (string, string, map[string]any, bool) {
	router := newLLMRouter(cfg)
	if router == nil {
		return "", "", nil, false
	}
	envelope, err := router.route(question, language)
	if err != nil {
		return "", "", nil, false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Route)) {
	case "direct_response":
		answer := directResponseTemplate(envelope.DirectResponseType, language)
		if answer == "" {
			return "", "", nil, false
		}
		return answer, "direct_response", map[string]any{
			"type":                 "direct_response",
			"direct_response_type": strings.TrimSpace(envelope.DirectResponseType),
			"router_reason":        strings.TrimSpace(envelope.Reason),
			"router_confidence":    envelope.Confidence,
		}, true
	case "out_of_scope":
		return outOfScopeResponse(language), "out_of_scope", map[string]any{
			"type":              "out_of_scope",
			"router_reason":     strings.TrimSpace(envelope.Reason),
			"router_confidence": envelope.Confidence,
		}, true
	default:
		return "", "", nil, false
	}
}

func responseLanguage(language, question string) string {
	if strings.ToLower(strings.TrimSpace(language)) == "zh" {
		return "zh"
	}
	if strings.ToLower(strings.TrimSpace(language)) == "en" {
		return "en"
	}
	if containsHan(question) {
		return "zh"
	}
	return "en"
}

func directResponseTemplate(responseType, language string) string {
	switch strings.ToLower(strings.TrimSpace(responseType)) {
	case "greeting":
		if language == "zh" {
			return "你好，请问有什么可以帮你？"
		}
		return "Hi, how can I assist you today?"
	case "capability_intro":
		if language == "zh" {
			return "我是一个保险相关助手，可以协助回答宠物保险保单、保障范围、等待期、保障限额、不保事项及相关保障内容的问题。"
		}
		return "I’m an insurance assistant. I can help answer questions about pet insurance policies, coverage, waiting periods, benefit limits, exclusions, and related policy details."
	default:
		return ""
	}
}

func outOfScopeResponse(language string) string {
	if language == "zh" {
		return "这个问题不属于当前宠物保险知识库的处理范围。我目前主要协助回答宠物保险保单、保障范围、等待期、保障限额、不保事项及相关保障内容的问题。"
	}
	return "That question is outside the scope of this pet insurance knowledge service. I currently focus on pet insurance policies, coverage, waiting periods, benefit limits, exclusions, and related policy details."
}

func summarizeSources(cfg Config, question, provider, language string, sources []Source) (string, string, map[string]any) {
	if len(sources) == 0 {
		return "", "go_no_evidence", nil
	}

	summarizer := newLLMSummarizer(cfg)
	if summarizer == nil {
		return "", "go_no_llm_summary", map[string]any{
			"type":         "rag_llm_summary_unavailable",
			"source_count": len(sources),
		}
	}

	attempts := []summarizeAttempt{
		{name: "primary", sources: budgetSourcesForSummary(sources, 900), sourceMode: "budget_900"},
		{name: "retry", sources: budgetSourcesForSummary(sources, 600), sourceMode: "budget_600"},
	}

	var lastErr error
	for idx, attempt := range attempts {
		envelope, err := summarizer.summarize(question, provider, language, attempt.sources)
		if err == nil {
			answer, mode, structured := buildSummarizerResult(envelope, cfg, len(sources), attempt)
			if strings.TrimSpace(answer) != "" && mode != "" {
				return answer, mode, structured
			}
		}
		if err == nil {
			lastErr = fmt.Errorf("empty_summary_content")
			continue
		}
		lastErr = err
		if !shouldRetrySummaryError(err) || idx == len(attempts)-1 {
			break
		}
	}

	if lastErr != nil {
		return "", "go_no_llm_summary", map[string]any{
			"type":           "rag_llm_summary_unavailable",
			"source_count":   len(sources),
			"failure_reason": compactSnippet(lastErr.Error(), 200),
		}
	}
	return "", "go_no_llm_summary", map[string]any{
		"type":           "rag_llm_summary_unavailable",
		"source_count":   len(sources),
		"failure_reason": "unknown_summary_failure",
	}
}

func buildSummarizerResult(envelope llmAnswerEnvelope, cfg Config, sourceCount int, attempt summarizeAttempt) (string, string, map[string]any) {
	if envelope.NeedsClarification || strings.EqualFold(strings.TrimSpace(envelope.Type), "clarification_needed") {
		msg := strings.TrimSpace(envelope.ClarificationMessage)
		if msg == "" {
			return "", "", nil
		}
		options := make([]map[string]any, 0, len(envelope.ClarificationOptions))
		for _, opt := range envelope.ClarificationOptions {
			options = append(options, map[string]any{
				"provider": opt.Provider,
				"products": opt.Products,
			})
		}
		return msg, "clarification_needed", map[string]any{
			"type":               "clarification_needed",
			"source_count":       sourceCount,
			"summarizer_model":   cfg.LLMModel,
			"attempt":            attempt.name,
			"source_mode":        attempt.sourceMode,
			"clarification_opts": options,
		}
	}
	answer := strings.TrimSpace(envelope.Answer)
	if answer == "" {
		return "", "", nil
	}
	return answer, "go_rag_llm_summary", map[string]any{
		"type":             "rag_llm_summary",
		"source_count":     sourceCount,
		"summarizer_model": cfg.LLMModel,
		"attempt":          attempt.name,
		"source_mode":      attempt.sourceMode,
	}
}

func newLLMRouter(cfg Config) *llmRouter {
	if strings.TrimSpace(cfg.LLMBaseURL) == "" || strings.TrimSpace(cfg.LLMModel) == "" || strings.TrimSpace(cfg.LLMAPIKey) == "" {
		return nil
	}
	timeout := time.Duration(cfg.LLMTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &llmRouter{
		baseURL: strings.TrimRight(cfg.LLMBaseURL, "/"),
		apiKey:  cfg.LLMAPIKey,
		model:   cfg.LLMModel,
		client:  &http.Client{Timeout: timeout},
	}
}

func (r *llmRouter) route(question, language string) (routeEnvelope, error) {
	payload := map[string]any{
		"model":       r.model,
		"temperature": 0,
		"messages": []map[string]any{
			{
				"role": "system",
				"content": strings.Join([]string{
					"You are a request router for a pet insurance assistant.",
					"Classify the user query into exactly one route.",
					`Return JSON only with this schema: {"route":"direct_response"|"rag_query"|"out_of_scope","direct_response_type":"greeting"|"capability_intro"|"","reason":"...","confidence":0.0}.`,
					"Use direct_response only for lightweight conversational requests such as greetings or asking what the assistant does.",
					"Use rag_query for pet insurance questions that should be answered from policy/corpus evidence.",
					"Use out_of_scope for questions unrelated to pet insurance knowledge coverage.",
					"Do not answer the user question. Only classify it.",
				}, " "),
			},
			{
				"role": "user",
				"content": strings.Join([]string{
					"Requested language: " + language,
					"User question: " + question,
				}, "\n"),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return routeEnvelope{}, err
	}
	req, err := http.NewRequest(http.MethodPost, r.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return routeEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return routeEnvelope{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return routeEnvelope{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return routeEnvelope{}, fmt.Errorf("router status %d body=%s", resp.StatusCode, msg)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return routeEnvelope{}, err
	}
	if len(parsed.Choices) == 0 {
		return routeEnvelope{}, fmt.Errorf("empty router choices")
	}
	content := stripCodeFence(strings.TrimSpace(parsed.Choices[0].Message.Content))
	if content == "" {
		return routeEnvelope{}, fmt.Errorf("empty router content")
	}
	var envelope routeEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return routeEnvelope{}, fmt.Errorf("invalid router json: %w", err)
	}
	return envelope, nil
}

func (s *llmSummarizer) summarize(question, provider, language string, sources []Source) (llmAnswerEnvelope, error) {
	envelope, err := s.summarizeWithJSONMode(question, provider, language, sources)
	if err == nil {
		return envelope, nil
	}
	if supportsJSONModeError(err) {
		return s.summarizeWithPromptJSON(question, provider, language, sources)
	}
	return llmAnswerEnvelope{}, err
}

func (s *llmSummarizer) summarizeWithJSONMode(question, provider, language string, sources []Source) (llmAnswerEnvelope, error) {
	payload := map[string]any{
		"model":       s.model,
		"temperature": 0.1,
		"response_format": map[string]any{
			"type": "json_object",
		},
		"messages": []map[string]any{
			{
				"role": "system",
				"content": strings.Join([]string{
					"You are a grounded insurance RAG summarizer.",
					"Answer only from the provided evidence snippets.",
					"Do not invent benefits, limits, waiting periods, exclusions, provider names, or plan names not supported by the evidence.",
					"If the evidence is insufficient or the comparison target is ambiguous, ask for clarification instead of guessing.",
					"Use the same language as the user question when possible.",
					"Return valid JSON only.",
					`Use this schema: {"type":"answer"|"clarification_needed","answer":"...","needs_clarification":true|false,"clarification_message":"...","clarification_options":[{"provider":"...","products":["..."]}]}.`,
					`When type="answer", fill "answer" and set needs_clarification=false.`,
					`When type="clarification_needed", fill clarification_message, set needs_clarification=true, and include only provider/product options explicitly supported by the evidence.`,
				}, " "),
			},
			{
				"role":    "user",
				"content": buildSummaryPrompt(question, provider, language, sources),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	req, err := http.NewRequest(http.MethodPost, s.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return llmAnswerEnvelope{}, fmt.Errorf("summary status %d body=%s", resp.StatusCode, msg)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return llmAnswerEnvelope{}, err
	}
	if len(parsed.Choices) == 0 {
		return llmAnswerEnvelope{}, fmt.Errorf("empty summary choices")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return llmAnswerEnvelope{}, nil
	}
	var envelope llmAnswerEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return llmAnswerEnvelope{}, fmt.Errorf("invalid summary json: %w", err)
	}
	return envelope, nil
}

func (s *llmSummarizer) summarizeWithPromptJSON(question, provider, language string, sources []Source) (llmAnswerEnvelope, error) {
	payload := map[string]any{
		"model":       s.model,
		"temperature": 0.1,
		"messages": []map[string]any{
			{
				"role": "system",
				"content": strings.Join([]string{
					"You are a grounded insurance RAG summarizer.",
					"Answer only from the provided evidence snippets.",
					"Do not invent benefits, limits, waiting periods, exclusions, provider names, or plan names not supported by the evidence.",
					"If the evidence is insufficient or the comparison target is ambiguous, ask for clarification instead of guessing.",
					"Use the same language as the user question when possible.",
					"Return JSON only with no markdown fences or extra commentary.",
					`Use this schema: {"type":"answer"|"clarification_needed","answer":"...","needs_clarification":true|false,"clarification_message":"...","clarification_options":[{"provider":"...","products":["..."]}]}.`,
				}, " "),
			},
			{
				"role":    "user",
				"content": buildSummaryPrompt(question, provider, language, sources),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	req, err := http.NewRequest(http.MethodPost, s.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return llmAnswerEnvelope{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return llmAnswerEnvelope{}, fmt.Errorf("summary status %d body=%s", resp.StatusCode, msg)
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return llmAnswerEnvelope{}, err
	}
	if len(parsed.Choices) == 0 {
		return llmAnswerEnvelope{}, fmt.Errorf("empty summary choices")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return llmAnswerEnvelope{}, nil
	}
	content = stripCodeFence(content)
	var envelope llmAnswerEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return llmAnswerEnvelope{}, fmt.Errorf("invalid summary json: %w", err)
	}
	return envelope, nil
}

func supportsJSONModeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "json mode is not supported")
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func shouldRetrySummaryError(err error) bool {
	if err == nil {
		return true
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "client.timeout exceeded") ||
		strings.Contains(msg, "status 502") ||
		strings.Contains(msg, "status 503") ||
		strings.Contains(msg, "status 504") {
		return false
	}
	return true
}

func rerankCandidates(cfg Config, question string, candidates []rankedChunk) []rankedChunk {
	r := newReranker(cfg)
	if r == nil || len(candidates) < 2 {
		return candidates
	}
	reordered, err := r.rerank(question, candidates)
	if err != nil || len(reordered) == 0 {
		return candidates
	}
	return reordered
}

func newLLMSummarizer(cfg Config) *llmSummarizer {
	if strings.TrimSpace(cfg.LLMBaseURL) == "" || strings.TrimSpace(cfg.LLMModel) == "" || strings.TrimSpace(cfg.LLMAPIKey) == "" {
		return nil
	}
	timeout := time.Duration(cfg.LLMTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &llmSummarizer{
		baseURL: strings.TrimRight(cfg.LLMBaseURL, "/"),
		apiKey:  cfg.LLMAPIKey,
		model:   cfg.LLMModel,
		client:  &http.Client{Timeout: timeout},
	}
}

func newReranker(cfg Config) *reranker {
	if !cfg.RerankEnabled || strings.TrimSpace(cfg.RerankBaseURL) == "" || strings.TrimSpace(cfg.RerankModel) == "" || strings.TrimSpace(cfg.RerankAPIKey) == "" {
		return nil
	}
	timeout := time.Duration(cfg.RerankTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	topN := cfg.RerankTopN
	if topN <= 0 {
		topN = cfg.DefaultMaxSources
	}
	return &reranker{
		baseURL: strings.TrimRight(cfg.RerankBaseURL, "/"),
		apiKey:  cfg.RerankAPIKey,
		model:   cfg.RerankModel,
		topN:    topN,
		client:  &http.Client{Timeout: timeout},
	}
}

func (r *reranker) rerank(question string, candidates []rankedChunk) ([]rankedChunk, error) {
	documents := make([]string, 0, len(candidates))
	for _, c := range candidates {
		documents = append(documents, rerankDocument(c.chunk))
	}
	topN := r.topN
	if topN <= 0 || topN > len(documents) {
		topN = len(documents)
	}
	payload := map[string]any{
		"model":     r.model,
		"query":     question,
		"documents": documents,
		"top_n":     topN,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, r.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return nil, fmt.Errorf("rerank status %d body=%s", resp.StatusCode, msg)
	}

	var parsed struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Results) == 0 {
		return nil, fmt.Errorf("empty rerank results")
	}

	used := map[int]bool{}
	out := make([]rankedChunk, 0, len(candidates))
	for _, item := range parsed.Results {
		if item.Index < 0 || item.Index >= len(candidates) || used[item.Index] {
			continue
		}
		used[item.Index] = true
		candidate := candidates[item.Index]
		candidate.score = candidate.score + item.RelevanceScore*10
		out = append(out, candidate)
	}
	for i, candidate := range candidates {
		if used[i] {
			continue
		}
		out = append(out, candidate)
	}
	return out, nil
}

func buildSummaryPrompt(question, provider, language string, sources []Source) string {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n")
	if provider = strings.TrimSpace(provider); provider != "" {
		b.WriteString("Requested provider filter: ")
		b.WriteString(provider)
		b.WriteString("\n")
	}
	if language = strings.TrimSpace(language); language != "" {
		b.WriteString("Requested language: ")
		b.WriteString(language)
		b.WriteString("\n")
	}
	b.WriteString("Evidence snippets:\n")
	for i, src := range sources {
		b.WriteString(fmt.Sprintf("[%d] provider=%s source=%s", i+1, valueOr(src.Provider, "unknown"), valueOr(src.SourceName, "unknown")))
		if src.Clauses != "" {
			b.WriteString(" clause=")
			b.WriteString(src.Clauses)
		}
		if src.SectionPath != "" {
			b.WriteString(" section=")
			b.WriteString(src.SectionPath)
		}
		b.WriteString("\n")
		b.WriteString(src.Snippet)
		b.WriteString("\n\n")
	}
	b.WriteString("Write a concise answer grounded only in the evidence above. If the evidence does not answer the question, say that explicitly.")
	return b.String()
}

func rerankDocument(ch Chunk) string {
	parts := []string{
		ch.Metadata["provider"],
		ch.Metadata["product"],
		ch.Metadata["source_name"],
		ch.Metadata["section_path"],
		ch.Metadata["clauses"],
		ch.Metadata["unit_types"],
		ch.Metadata["topic_tags"],
		ch.Text,
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func rankCandidates(chunks []Chunk, question, provider, language string, maxSources int) []rankedChunk {
	tokens := tokenize(question)
	out := make([]rankedChunk, 0, 64)
	for _, ch := range chunks {
		if provider != "" && ch.Metadata["provider"] != provider {
			continue
		}
		if language != "" && ch.Metadata["language"] != language {
			continue
		}
		searchText := strings.ToLower(strings.Join([]string{
			ch.Text,
			ch.Metadata["provider"],
			ch.Metadata["product"],
			ch.Metadata["source_name"],
			ch.Metadata["section_path"],
			ch.Metadata["clauses"],
			ch.Metadata["topic_tags"],
		}, "\n"))
		score := lexicalScore(searchText, tokens)
		score += metadataScore(ch, tokens)
		if score <= 0 {
			continue
		}
		out = append(out, rankedChunk{chunk: ch, score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if math.Abs(out[i].score-out[j].score) < 1e-6 {
			iSource := out[i].chunk.Metadata["source_name"] + "|" + out[i].chunk.Metadata["section_path"]
			jSource := out[j].chunk.Metadata["source_name"] + "|" + out[j].chunk.Metadata["section_path"]
			return iSource < jSource
		}
		return out[i].score > out[j].score
	})

	limit := 12
	if maxSources > 0 {
		limit = maxSources * 4
		if limit < 12 {
			limit = 12
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func trimCandidates(candidates []rankedChunk, maxSources int) []rankedChunk {
	if len(candidates) == 0 {
		return candidates
	}
	limit := maxSources
	if limit <= 0 || limit > len(candidates) {
		limit = len(candidates)
	}
	return candidates[:limit]
}

func compactPromptSources(sources []Source, snippetLimit int) []Source {
	if len(sources) == 0 {
		return nil
	}
	out := make([]Source, 0, len(sources))
	for _, src := range sources {
		src.Snippet = compactSnippet(src.Snippet, snippetLimit)
		out = append(out, src)
	}
	return out
}

func budgetSourcesForSummary(sources []Source, totalSnippetBudget int) []Source {
	if len(sources) == 0 {
		return nil
	}
	if totalSnippetBudget <= 0 {
		return compactPromptSourcesByType(sources, 220, 160)
	}
	perSource := totalSnippetBudget / len(sources)
	if perSource < 180 {
		perSource = 180
	}
	if perSource > 520 {
		perSource = 520
	}
	structuredLimit := perSource - 80
	if structuredLimit < 140 {
		structuredLimit = 140
	}
	return compactPromptSourcesByType(sources, perSource, structuredLimit)
}

func compactPromptSourcesByType(sources []Source, normalLimit, structuredLimit int) []Source {
	if len(sources) == 0 {
		return nil
	}
	out := make([]Source, 0, len(sources))
	for _, src := range sources {
		limit := normalLimit
		if strings.HasPrefix(strings.TrimSpace(src.SourceName), "structured_") {
			limit = structuredLimit
		}
		if limit <= 0 {
			limit = 140
		}
		src.Snippet = compactSnippet(src.Snippet, limit)
		out = append(out, src)
	}
	return out
}

func dedupeCandidates(candidates []rankedChunk) []rankedChunk {
	if len(candidates) < 2 {
		return candidates
	}
	out := make([]rankedChunk, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		key := strings.Join([]string{
			candidate.chunk.Metadata["provider"],
			candidate.chunk.Metadata["source_name"],
			candidate.chunk.Metadata["section_path"],
			compactSnippet(candidate.chunk.Text, 220),
		}, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, candidate)
	}
	return out
}

func collapseStructuredCandidates(candidates []rankedChunk) []rankedChunk {
	if len(candidates) < 2 {
		return candidates
	}
	out := make([]rankedChunk, 0, len(candidates))
	seenStructured := map[string]bool{}
	for _, candidate := range candidates {
		sourceName := strings.TrimSpace(candidate.chunk.Metadata["source_name"])
		if !strings.HasPrefix(sourceName, "structured_") {
			out = append(out, candidate)
			continue
		}
		key := strings.Join([]string{
			candidate.chunk.Metadata["provider"],
			sourceName,
			candidate.chunk.Metadata["section_path"],
			candidate.chunk.Metadata["language"],
		}, "|")
		if seenStructured[key] {
			continue
		}
		seenStructured[key] = true
		out = append(out, candidate)
	}
	return out
}

func diversifyCandidates(candidates []rankedChunk, providerFilter string) []rankedChunk {
	if len(candidates) < 3 || strings.TrimSpace(providerFilter) != "" {
		return candidates
	}

	selected := make([]rankedChunk, 0, len(candidates))
	used := make([]bool, len(candidates))
	seenProviders := map[string]bool{}

	for i, candidate := range candidates {
		provider := strings.TrimSpace(candidate.chunk.Metadata["provider"])
		if provider == "" || seenProviders[provider] {
			continue
		}
		selected = append(selected, candidate)
		used[i] = true
		seenProviders[provider] = true
	}

	for i, candidate := range candidates {
		if used[i] {
			continue
		}
		selected = append(selected, candidate)
	}

	return selected
}

func tokenize(question string) []string {
	normalized := strings.ToLower(strings.TrimSpace(question))
	out := make([]string, 0, 16)
	seen := map[string]bool{}
	current := strings.Builder{}
	flush := func() {
		if current.Len() == 0 {
			return
		}
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token == "" || seen[token] {
			return
		}
		if len(token) == 1 && !containsHan(token) {
			return
		}
		seen[token] = true
		out = append(out, token)
	}
	for _, r := range normalized {
		if isTokenRune(r) {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	for _, run := range hanRuns(normalized) {
		if len([]rune(run)) < 2 {
			if !seen[run] {
				seen[run] = true
				out = append(out, run)
			}
			continue
		}
		for _, token := range hanNGrams(run, 2, 4) {
			if seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
		}
	}
	return out
}

func isTokenRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsNumber(r) {
		return true
	}
	if r >= 0x4E00 && r <= 0x9FFF {
		return true
	}
	return r == '_' || r == '-' || r == '\'' || r == '/'
}

func lexicalScore(text string, tokens []string) float64 {
	if len(tokens) == 0 || strings.TrimSpace(text) == "" {
		return 0
	}
	score := 0.0
	for _, token := range tokens {
		count := strings.Count(text, token)
		if count == 0 {
			continue
		}
		score += 1 + math.Min(float64(count-1), 2)*0.25
	}
	return score
}

func metadataScore(ch Chunk, tokens []string) float64 {
	sectionPath := strings.ToLower(ch.Metadata["section_path"])
	unitTypes := strings.ToLower(ch.Metadata["unit_types"])
	topicTags := strings.ToLower(ch.Metadata["topic_tags"])
	sourceName := strings.ToLower(ch.Metadata["source_name"])

	score := 0.0
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(sectionPath, token) {
			score += 0.8
		}
		if strings.Contains(topicTags, token) {
			score += 0.6
		}
		if strings.Contains(unitTypes, token) {
			score += 0.4
		}
	}

	switch unitTypes {
	case "benefit":
		score += 0.35
	case "definition":
		score += 1.15
	case "waiting_period":
		score += 0.1
	case "exclusion":
		score -= 1.1
	}

	if containsAny(sectionPath, "definitions", "definition:", "定義", "釋義") {
		score += 1.0
	}
	if strings.Contains(sourceName, "structured_product_waiting_period") {
		score -= 0.45
	}
	if strings.Contains(sourceName, "structured_sub_coverage_limit") {
		score -= 0.15
	}

	if containsAny(sectionPath,
		"一般不保事項",
		"不保事項",
		"general exclusions",
		"general conditions",
		"一般條款",
		"sanction limitation and exclusion",
		"制裁限制及不保條款",
		"what your policy does not cover",
	) {
		score -= 1.4
	}

	return score
}

func buildSourcePayload(ch Chunk, score float64) Source {
	snippet := compactSnippet(ch.Text, 800)
	return Source{
		Provider:    ch.Metadata["provider"],
		Language:    ch.Metadata["language"],
		Product:     ch.Metadata["product"],
		SourceName:  ch.Metadata["source_name"],
		SectionPath: ch.Metadata["section_path"],
		Clauses:     ch.Metadata["clauses"],
		UnitTypes:   ch.Metadata["unit_types"],
		TopicTags:   ch.Metadata["topic_tags"],
		Score:       score,
		Snippet:     snippet,
	}
}

func fallbackRetrievalAnswer(question, language string, sources []Source) string {
	if len(sources) == 0 {
		return ""
	}

	lead := firstMeaningfulSnippetSentence(sources[0].Snippet)
	if lead == "" {
		lead = compactSnippet(sources[0].Snippet, 220)
	}
	if lead == "" {
		return ""
	}

	if strings.ToLower(strings.TrimSpace(language)) == "zh" || containsHan(question) {
		return "我目前无法完成模型总结，但根据当前检索到的保单证据，最相关内容是：" + lead
	}
	return "I couldn't complete the model summary, but the most relevant policy evidence I found is: " + lead
}

func firstMeaningfulSnippetSentence(snippet string) string {
	snippet = compactSnippet(snippet, 320)
	snippet = strings.TrimSpace(snippet)
	if snippet == "" {
		return ""
	}
	lines := strings.FieldsFunc(snippet, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	cleanedParts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "> Source:") || strings.HasPrefix(line, "> Clause:") || strings.HasPrefix(line, "> Unit:") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		if line != "" {
			cleanedParts = append(cleanedParts, line)
		}
	}
	if len(cleanedParts) == 0 {
		return ""
	}
	text := strings.Join(cleanedParts, " ")
	if idx := strings.IndexAny(text, ".!?。；;"); idx >= 0 {
		text = strings.TrimSpace(text[:idx+1])
	}
	return compactSnippet(text, 220)
}

func compactSnippet(snippet string, limit int) string {
	snippet = strings.Join(strings.Fields(strings.TrimSpace(snippet)), " ")
	if limit > 0 && len(snippet) > limit {
		return snippet[:limit] + "..."
	}
	return snippet
}

func defaultDisclaimer(language, question string) string {
	if normalized := strings.ToLower(strings.TrimSpace(language)); normalized == "zh" {
		return "仅供参考，不保证 100% 准确、完整或最新。最终以保险公司官网、正式保单、承保表、批单及最新书面说明为准。"
	}
	if containsHan(question) {
		return "仅供参考，不保证 100% 准确、完整或最新。最终以保险公司官网、正式保单、承保表、批单及最新书面说明为准。"
	}
	return "For reference only. Accuracy, completeness, and recency are not guaranteed. Always rely on the insurer's official website, policy wording, schedule, endorsement, and latest written confirmation."
}

func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func hanRuns(s string) []string {
	runs := make([]string, 0, 4)
	buf := make([]rune, 0, 16)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		runs = append(runs, string(buf))
		buf = buf[:0]
	}
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			buf = append(buf, r)
			continue
		}
		flush()
	}
	flush()
	return runs
}

func hanNGrams(s string, minN, maxN int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}
	if minN < 1 {
		minN = 1
	}
	if maxN < minN {
		maxN = minN
	}
	out := make([]string, 0, len(runes)*2)
	for n := minN; n <= maxN; n++ {
		if n > len(runes) {
			break
		}
		for i := 0; i+n <= len(runes); i++ {
			out = append(out, string(runes[i:i+n]))
		}
	}
	return out
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func containsAny(s string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(s, term) {
			return true
		}
	}
	return false
}

func BuildCapabilities(cfg Config) map[string]any {
	meta := readIndexMetadata(cfg.PersistDir)
	return map[string]any{
		"ok":                  true,
		"service":             "go_rag_translation_skeleton",
		"supported_providers": SupportedProviders,
		"supported_languages": SupportedLanguages,
		"default_max_sources": cfg.DefaultMaxSources,
		"max_allowed_sources": cfg.MaxAllowedSources,
		"query_methods":       []string{"GET", "POST"},
		"query_routes": map[string]string{
			"healthz":      "/api/rag/go/healthz",
			"readyz":       "/api/rag/go/readyz",
			"capabilities": "/api/rag/go/capabilities",
			"query_get":    "/api/rag/go/query?q=...&provider=...&language=...",
			"query_post":   "/api/rag/go/query",
		},
		"summarization": map[string]any{
			"mode":           "retrieval_plus_llm_summary",
			"llm_configured": strings.TrimSpace(cfg.LLMBaseURL) != "" && strings.TrimSpace(cfg.LLMModel) != "" && strings.TrimSpace(cfg.LLMAPIKey) != "",
		},
		"index": map[string]any{
			"persist_dir":                cfg.PersistDir,
			"chunker_version":            stringValue(meta, "chunker_version"),
			"document_count":             intValue(meta, "document_count"),
			"chunk_size":                 intValue(meta, "chunk_size"),
			"chunk_overlap":              intValue(meta, "chunk_overlap"),
			"data_path":                  stringValueWithDefault(meta, "data_path", cfg.DataPath),
			"built_at_utc":               stringValue(meta, "built_at_utc"),
			"source_markdown_file_count": intValue(meta, "source_markdown_file_count"),
			"supported_provider_count":   intValueWithDefault(meta, "supported_provider_count", len(SupportedProviders)),
			"chunker_version_go":         ChunkerVersion,
		},
	}
}

func readIndexMetadata(persistDir string) map[string]any {
	path := filepath.Join(persistDir, "prototype_index_meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}

func stringValue(meta map[string]any, key string) string {
	v, ok := meta[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func stringValueWithDefault(meta map[string]any, key, def string) string {
	if v := stringValue(meta, key); v != "" {
		return v
	}
	return def
}

func intValue(meta map[string]any, key string) int {
	v, ok := meta[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	default:
		return 0
	}
}

func intValueWithDefault(meta map[string]any, key string, def int) int {
	v := intValue(meta, key)
	if v == 0 {
		return def
	}
	return v
}
