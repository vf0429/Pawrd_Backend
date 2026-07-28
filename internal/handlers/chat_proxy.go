package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/raggo"
)

type chatSessionState struct {
	Provider string
}

type chatSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]chatSessionState
}

func NewChatSessionStore() *chatSessionStore {
	return &chatSessionStore{
		sessions: make(map[string]chatSessionState),
	}
}

func (s *chatSessionStore) create() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.sessions[id] = chatSessionState{}
	return id
}

func (s *chatSessionStore) setProvider(sessionID, provider string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[sessionID]
	if !ok {
		return false
	}
	state.Provider = provider
	s.sessions[sessionID] = state
	return true
}

func (s *chatSessionStore) provider(sessionID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[sessionID].Provider
}

type pythonRAGClient struct {
	baseURL string
	client  *http.Client
}

type goRAGClient struct {
	baseURL string
	client  *http.Client
	cfg     raggo.Config
}

func newPythonRAGClient(cfg *config.Config) *pythonRAGClient {
	timeout := time.Duration(cfg.PythonRAGTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return &pythonRAGClient{
		baseURL: strings.TrimRight(cfg.PythonRAGBaseURL, "/"),
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func newGoRAGClient(cfg *config.Config) *goRAGClient {
	timeout := time.Duration(cfg.GoRAGTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ragCfg := raggo.LoadConfig()
	ragCfg.LLMBaseURL = cfg.RAGLLMBaseURL
	ragCfg.LLMModel = cfg.RAGLLMModel
	ragCfg.LLMAPIKey = cfg.RAGLLMAPIKey
	ragCfg.LLMTimeoutSeconds = cfg.RAGLLMTimeoutSeconds
	ragCfg.RerankEnabled = cfg.RAGRerankEnabled
	ragCfg.RerankBaseURL = cfg.RAGRerankBaseURL
	ragCfg.RerankModel = cfg.RAGRerankModel
	ragCfg.RerankAPIKey = cfg.RAGRerankAPIKey
	ragCfg.RerankTopN = cfg.RAGRerankTopN
	ragCfg.RerankTimeoutSeconds = cfg.RAGRerankTimeoutSeconds
	return &goRAGClient{
		baseURL: strings.TrimRight(cfg.GoRAGBaseURL, "/"),
		client: &http.Client{
			Timeout: timeout,
		},
		cfg: ragCfg,
	}
}

type legacyChatRequest struct {
	Query             string   `json:"query"`
	SessionID         string   `json:"session_id"`
	Model             string   `json:"model"`
	Provider          string   `json:"provider"`
	ProviderScope     string   `json:"provider_scope"`
	SelectedProviders []string `json:"selected_providers"`
	Tool              string   `json:"tool"`
}

type legacyProviderSelectionRequest struct {
	Provider string `json:"provider"`
}

type legacyChatResponse struct {
	Answer         string   `json:"answer"`
	Sources        []string `json:"sources"`
	ActiveProvider string   `json:"active_provider,omitempty"`
	SessionID      string   `json:"session_id,omitempty"`
}

type pythonQueryResponse struct {
	Answer  string `json:"answer"`
	Sources []struct {
		SourceName string `json:"source_name"`
		Section    string `json:"section_path"`
		Clauses    string `json:"clauses"`
		Snippet    string `json:"snippet"`
	} `json:"sources"`
}

type goQueryResponse struct {
	Answer  string `json:"answer"`
	Sources []struct {
		SourceName string `json:"source_name"`
		Section    string `json:"section_path"`
		Clauses    string `json:"clauses"`
		Snippet    string `json:"snippet"`
	} `json:"sources"`
}

type errorResponse struct {
	Detail string `json:"detail"`
}

const (
	providerScopeAll      = "all"
	providerScopeSelected = "selected"
)

func NewChatSessionHandler(store *chatSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sessionID := store.create()
		writeChatJSON(w, http.StatusOK, map[string]string{"session_id": sessionID})
	}
}

func NewChatSessionProviderHandler(store *chatSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		sessionID := strings.TrimSpace(r.PathValue("sessionID"))
		if sessionID == "" {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: "missing session id"})
			return
		}

		var req legacyProviderSelectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: "invalid request body"})
			return
		}

		if ok := store.setProvider(sessionID, strings.TrimSpace(req.Provider)); !ok {
			writeChatJSON(w, http.StatusNotFound, errorResponse{Detail: "session not found"})
			return
		}

		writeChatJSON(w, http.StatusOK, map[string]string{"session_id": sessionID, "provider": strings.TrimSpace(req.Provider)})
	}
}

func NewChatProxyHandler(cfg *config.Config, store *chatSessionStore) http.HandlerFunc {
	pythonClient := newPythonRAGClient(cfg)
	goClient := newGoRAGClient(cfg)
	runtimeName := strings.ToLower(strings.TrimSpace(cfg.ChatRAGRuntime))
	if runtimeName != "go" {
		runtimeName = "python"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req legacyChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: "invalid request body"})
			return
		}

		if strings.TrimSpace(req.Query) == "" {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: "query is required"})
			return
		}
		if strings.TrimSpace(req.Model) != "" && strings.TrimSpace(req.Model) != "insurance" {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: "only insurance model is currently supported"})
			return
		}
		if strings.TrimSpace(req.Tool) != "" && strings.TrimSpace(req.Tool) != "general_assistant" {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: "medical tool flow is not currently supported by this proxy"})
			return
		}

		selectedProviders, err := normalizeSelectedProviders(req.SelectedProviders)
		if err != nil {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: err.Error()})
			return
		}

		providerScope, err := normalizeProviderScope(req.ProviderScope, selectedProviders)
		if err != nil {
			writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: err.Error()})
			return
		}

		provider := strings.TrimSpace(req.Provider)
		if provider != "" {
			provider, err = raggo.ValidateProvider(provider)
			if err != nil {
				writeChatJSON(w, http.StatusBadRequest, errorResponse{Detail: err.Error()})
				return
			}
		}
		if provider == "" && len(selectedProviders) == 1 && providerScope == providerScopeSelected {
			provider = selectedProviders[0]
		}
		if provider == "" && strings.TrimSpace(req.SessionID) != "" && providerScope != providerScopeSelected {
			provider = store.provider(strings.TrimSpace(req.SessionID))
		}

		query := buildEffectiveInsuranceQuery(req.Query, providerScope, selectedProviders)

		var answer string
		var sources []string
		if runtimeName == "go" {
			answer, sources, err = goClient.queryInsurance(query, provider)
		} else {
			answer, sources, err = pythonClient.queryInsurance(query, provider)
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "service unavailable") {
				// Automatic fallback: keep /api/chat alive even if Python sidecar is down.
				answer, sources, err = goClient.queryInsurance(query, provider)
			}
		}
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, errInvalidProvider) {
				status = http.StatusBadRequest
			}
			writeChatJSON(w, status, errorResponse{Detail: err.Error()})
			return
		}

		writeChatJSON(w, http.StatusOK, legacyChatResponse{
			Answer:         answer,
			Sources:        sources,
			ActiveProvider: provider,
			SessionID:      strings.TrimSpace(req.SessionID),
		})
	}
}

var errInvalidProvider = errors.New("python rag rejected provider")

func (c *pythonRAGClient) queryInsurance(question, provider string) (string, []string, error) {
	params := url.Values{}
	params.Set("q", question)
	params.Set("max_sources", "3")
	if provider != "" {
		params.Set("provider", provider)
	}

	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/query?"+params.Encode(), nil)
	if err != nil {
		return "", nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("python rag service unavailable: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if strings.Contains(string(respBody), "invalid_provider") {
			return "", nil, errInvalidProvider
		}
		message := strings.TrimSpace(string(respBody))
		if message == "" {
			message = fmt.Sprintf("python rag returned HTTP %d", resp.StatusCode)
		}
		return "", nil, errors.New(message)
	}

	var parsed pythonQueryResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", nil, fmt.Errorf("invalid python rag response: %w", err)
	}

	sources := make([]string, 0, len(parsed.Sources))
	for _, item := range parsed.Sources {
		source := strings.TrimSpace(item.SourceName)
		if clause := strings.TrimSpace(item.Clauses); clause != "" {
			source = strings.TrimSpace(source + " " + clause)
		}
		if section := strings.TrimSpace(item.Section); section != "" {
			source = strings.TrimSpace(source + " - " + section)
		}
		if source == "" {
			source = strings.TrimSpace(item.Snippet)
		}
		if source != "" {
			sources = append(sources, source)
		}
	}

	return strings.TrimSpace(parsed.Answer), sources, nil
}

func (c *goRAGClient) queryInsurance(question, provider string) (string, []string, error) {
	// Prefer in-process Go RAG runtime to avoid deploy-time loopback dependency.
	if inProcessEnabled() {
		answer, sources, err := c.queryInsuranceInProcess(question, provider)
		if err != nil {
			return "", nil, err
		}
		return answer, sources, nil
	}

	// Compatibility mode for environments that explicitly disable in-process RAG.
	return c.queryInsuranceViaHTTP(question, provider)
}

func inProcessEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("GO_RAG_INPROCESS_ENABLED")))
	return raw == "" || raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func (c *goRAGClient) queryInsuranceInProcess(question, provider string) (string, []string, error) {
	validProvider, err := raggo.ValidateProvider(provider)
	if err != nil {
		return "", nil, errInvalidProvider
	}
	maxSources, err := raggo.ValidateMaxSources("3", c.cfg.DefaultMaxSources, c.cfg.MaxAllowedSources)
	if err != nil {
		maxSources = 3
	}

	result := raggo.AnswerQuery(c.cfg, question, validProvider, "", maxSources)
	if strings.TrimSpace(result.Answer) == "" {
		detail := strings.TrimSpace(result.AnswerMode)
		if structuredReason, ok := result.Structured["failure_reason"].(string); ok && strings.TrimSpace(structuredReason) != "" {
			detail = strings.TrimSpace(detail + ": " + structuredReason)
		}
		if detail == "" {
			detail = "empty answer"
		}
		return "", nil, fmt.Errorf("go rag in-process failed: %s", detail)
	}
	sources := make([]string, 0, len(result.Sources))
	for _, item := range result.Sources {
		source := strings.TrimSpace(item.SourceName)
		if clause := strings.TrimSpace(item.Clauses); clause != "" {
			source = strings.TrimSpace(source + " " + clause)
		}
		if section := strings.TrimSpace(item.SectionPath); section != "" {
			source = strings.TrimSpace(source + " - " + section)
		}
		if source == "" {
			source = strings.TrimSpace(item.Snippet)
		}
		if source != "" {
			sources = append(sources, source)
		}
	}
	return strings.TrimSpace(result.Answer), sources, nil
}

func (c *goRAGClient) queryInsuranceViaHTTP(question, provider string) (string, []string, error) {
	params := url.Values{}
	params.Set("q", question)
	params.Set("max_sources", "3")
	if provider != "" {
		params.Set("provider", provider)
	}

	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/query?"+params.Encode(), nil)
	if err != nil {
		return "", nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("go rag service unavailable: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if strings.Contains(string(respBody), "invalid_provider") {
			return "", nil, errInvalidProvider
		}
		message := strings.TrimSpace(string(respBody))
		if message == "" {
			message = fmt.Sprintf("go rag returned HTTP %d", resp.StatusCode)
		}
		return "", nil, errors.New(message)
	}

	var parsed goQueryResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", nil, fmt.Errorf("invalid go rag response: %w", err)
	}

	sources := make([]string, 0, len(parsed.Sources))
	for _, item := range parsed.Sources {
		source := strings.TrimSpace(item.SourceName)
		if clause := strings.TrimSpace(item.Clauses); clause != "" {
			source = strings.TrimSpace(source + " " + clause)
		}
		if section := strings.TrimSpace(item.Section); section != "" {
			source = strings.TrimSpace(source + " - " + section)
		}
		if source == "" {
			source = strings.TrimSpace(item.Snippet)
		}
		if source != "" {
			sources = append(sources, source)
		}
	}

	return strings.TrimSpace(parsed.Answer), sources, nil
}

func writeChatJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func normalizeSelectedProviders(raw []string) ([]string, error) {
	normalized := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		value, err := raggo.ValidateProvider(item)
		if err != nil {
			return nil, err
		}
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func normalizeProviderScope(raw string, selectedProviders []string) (string, error) {
	scope := strings.ToLower(strings.TrimSpace(raw))
	if scope == "" {
		if len(selectedProviders) > 0 {
			return providerScopeSelected, nil
		}
		return providerScopeAll, nil
	}
	switch scope {
	case providerScopeAll:
		return providerScopeAll, nil
	case providerScopeSelected:
		if len(selectedProviders) == 0 {
			return "", errors.New("selected_providers is required when provider_scope is selected")
		}
		return providerScopeSelected, nil
	default:
		return "", errors.New("provider_scope must be one of: all, selected")
	}
}

func buildEffectiveInsuranceQuery(query, providerScope string, selectedProviders []string) string {
	trimmed := strings.TrimSpace(query)
	if providerScope != providerScopeSelected || len(selectedProviders) == 0 {
		return trimmed
	}

	displayNames := make([]string, 0, len(selectedProviders))
	for _, provider := range selectedProviders {
		displayNames = append(displayNames, providerDisplayName(provider))
	}
	contextLine := "Selected providers: " + strings.Join(displayNames, ", ")
	if trimmed == "" {
		return contextLine
	}
	return contextLine + "\n\nUser question: " + trimmed
}

func providerDisplayName(provider string) string {
	switch provider {
	case "bluecross":
		return "Blue Cross"
	case "one_degree":
		return "One Degree"
	case "prudential":
		return "Prudential"
	case "msig":
		return "MSIG"
	default:
		return provider
	}
}
