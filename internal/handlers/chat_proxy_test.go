package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
)

func TestChatProxyDefaultsToGoRuntime(t *testing.T) {
	t.Setenv("HK_INSURANCE_RAG_DATA_PATH", "../../assets/rag_normalized/hk_insurance")
	t.Setenv("HK_INSURANCE_RAG_MAX_SOURCES", "6")
	t.Setenv("GO_RAG_INPROCESS_ENABLED", "true")

	llmUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode llm request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) < 2 {
			t.Fatalf("expected router or summarizer messages, got %#v", payload)
		}
		msgMap, _ := messages[1].(map[string]any)
		content, _ := msgMap["content"].(string)
		if strings.Contains(content, "User question:") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"content": `{"route":"rag_query","direct_response_type":"","reason":"insurance query","confidence":0.92}`},
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"type":"answer","answer":"go in-process answer","needs_clarification":false}`},
			}},
		})
	}))
	defer llmUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        "http://127.0.0.1:9",
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "",
		RAGLLMBaseURL:           llmUpstream.URL,
		RAGLLMModel:             "test-model",
		RAGLLMAPIKey:            "test-key",
		RAGLLMTimeoutSeconds:    5,
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"Blue Cross 包唔包獸醫診症？","model":"insurance","provider":"bluecross"}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(stringValueFromAny(resp["answer"])) == "" {
		t.Fatalf("expected non-empty answer, got %#v", resp["answer"])
	}
}

func TestChatProxyUsesPythonWhenConfigured(t *testing.T) {
	pythonUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer": "python answer",
			"sources": []map[string]any{
				{
					"source_name":  "p.md",
					"clauses":      "1.C",
					"section_path": "Benefits > Consult",
				},
			},
		})
	}))
	defer pythonUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        pythonUpstream.URL,
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "python",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"Blue Cross 包唔包獸醫診症？","model":"insurance","provider":"bluecross"}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(stringValueFromAny(resp["answer"])) != "python answer" {
		t.Fatalf("unexpected answer: %#v", resp["answer"])
	}
	sources, _ := resp["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("expected 1 source got %d", len(sources))
	}
}

func TestChatProxyFallsBackToGoWhenPythonUnavailable(t *testing.T) {
	t.Setenv("HK_INSURANCE_RAG_DATA_PATH", "../../assets/rag_normalized/hk_insurance")
	t.Setenv("HK_INSURANCE_RAG_MAX_SOURCES", "6")
	t.Setenv("GO_RAG_INPROCESS_ENABLED", "true")

	llmUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode llm request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) < 2 {
			t.Fatalf("expected router or summarizer messages, got %#v", payload)
		}
		msgMap, _ := messages[1].(map[string]any)
		content, _ := msgMap["content"].(string)
		if strings.Contains(content, "User question:") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"message": map[string]any{"content": `{"route":"rag_query","direct_response_type":"","reason":"insurance query","confidence":0.91}`},
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"type":"answer","answer":"fallback go answer","needs_clarification":false}`},
			}},
		})
	}))
	defer llmUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        "http://127.0.0.1:9",
		PythonRAGTimeoutSeconds: 1,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "python",
		RAGLLMBaseURL:           llmUpstream.URL,
		RAGLLMModel:             "test-model",
		RAGLLMAPIKey:            "test-key",
		RAGLLMTimeoutSeconds:    5,
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"query":"What is Prudential room and board coverage?","model":"insurance","provider":"prudential"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected fallback success status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(stringValueFromAny(resp["answer"])) == "" {
		t.Fatalf("expected non-empty fallback answer, got %#v", resp["answer"])
	}
}

func TestChatProxyReturnsFallbackAnswerInsteadOfHTTPFallback(t *testing.T) {
	t.Setenv("HK_INSURANCE_RAG_DATA_PATH", "../../assets/rag_normalized/hk_insurance")
	t.Setenv("HK_INSURANCE_RAG_MAX_SOURCES", "6")
	t.Setenv("GO_RAG_INPROCESS_ENABLED", "true")

	llmUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream summary timeout", http.StatusGatewayTimeout)
	}))
	defer llmUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        "http://127.0.0.1:9",
		PythonRAGTimeoutSeconds: 1,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "go",
		RAGLLMBaseURL:           llmUpstream.URL,
		RAGLLMModel:             "test-model",
		RAGLLMAPIKey:            "test-key",
		RAGLLMTimeoutSeconds:    5,
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"query":"What is Prudential room and board coverage?","model":"insurance","provider":"prudential"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "127.0.0.1:9/query") {
		t.Fatalf("expected no HTTP fallback loopback error, got body=%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "I couldn't complete the model summary") {
		t.Fatalf("expected fallback answer, got body=%s", rr.Body.String())
	}
}

func TestChatProxyUsesGoRuntimeWhenConfigured(t *testing.T) {
	t.Setenv("GO_RAG_INPROCESS_ENABLED", "false")

	goUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("provider"); got != "prudential" {
			t.Fatalf("expected provider=prudential, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer": "go answer",
			"sources": []map[string]any{
				{
					"source_name":  "g.md",
					"clauses":      "1.B",
					"section_path": "Benefits > Room and Board",
				},
				{
					"snippet": "fallback snippet source",
				},
			},
		})
	}))
	defer goUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        "http://127.0.0.1:9",
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            goUpstream.URL,
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "go",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"What is the annual limit?","model":"insurance","provider":"prudential"}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(stringValueFromAny(resp["answer"])) == "" {
		t.Fatalf("expected non-empty answer: %#v", resp["answer"])
	}
	sources, _ := resp["sources"].([]any)
	if len(sources) == 0 {
		t.Fatalf("expected non-empty sources")
	}
}

func TestChatProxyUsesSessionProviderFallback(t *testing.T) {
	var capturedProvider string
	pythonUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedProvider = r.URL.Query().Get("provider")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer":  "ok",
			"sources": []map[string]any{},
		})
	}))
	defer pythonUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        pythonUpstream.URL,
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "python",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	sessionID := store.create()
	if !store.setProvider(sessionID, "msig") {
		t.Fatalf("failed to set provider in session")
	}

	body := `{"query":"foo","model":"insurance","session_id":"` + sessionID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}
	if capturedProvider != "msig" {
		t.Fatalf("expected fallback provider msig got %q", capturedProvider)
	}
}

func TestChatProxyUsesSelectedProvidersContextWithoutSingleProviderLock(t *testing.T) {
	var capturedProvider string
	var capturedQuery string
	pythonUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedProvider = r.URL.Query().Get("provider")
		capturedQuery = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer":  "ok",
			"sources": []map[string]any{},
		})
	}))
	defer pythonUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        pythonUpstream.URL,
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "python",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"Compare @Blue Cross and @MSIG waiting period","model":"insurance","provider_scope":"selected","selected_providers":["bluecross","msig"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}
	if capturedProvider != "" {
		t.Fatalf("expected no single provider lock got %q", capturedProvider)
	}
	if !strings.Contains(capturedQuery, "Selected providers: Blue Cross, MSIG") {
		t.Fatalf("expected selected providers context in query, got %q", capturedQuery)
	}
}

func TestChatProxyUsesSingleSelectedProviderAsProvider(t *testing.T) {
	var capturedProvider string
	pythonUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedProvider = r.URL.Query().Get("provider")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer":  "ok",
			"sources": []map[string]any{},
		})
	}))
	defer pythonUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        pythonUpstream.URL,
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "python",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"Tell me about @Blue Cross","model":"insurance","provider_scope":"selected","selected_providers":["bluecross"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}
	if capturedProvider != "bluecross" {
		t.Fatalf("expected provider=bluecross got %q", capturedProvider)
	}
}

func TestChatProxyRejectsSelectedScopeWithoutProviders(t *testing.T) {
	cfg := &config.Config{
		PythonRAGBaseURL:        "http://127.0.0.1:9",
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            "http://127.0.0.1:9",
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "python",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"Compare insurance","model":"insurance","provider_scope":"selected","selected_providers":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestChatProxyInvalidProviderMapsToBadRequest(t *testing.T) {
	goUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_provider"}`, http.StatusBadRequest)
	}))
	defer goUpstream.Close()

	cfg := &config.Config{
		PythonRAGBaseURL:        "http://127.0.0.1:9",
		PythonRAGTimeoutSeconds: 5,
		GoRAGBaseURL:            goUpstream.URL,
		GoRAGTimeoutSeconds:     5,
		ChatRAGRuntime:          "go",
	}
	store := NewChatSessionStore()
	handler := NewChatProxyHandler(cfg, store)

	body := `{"query":"foo","model":"insurance","provider":"unknown"}`
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGoRAGClientFormatsSources(t *testing.T) {
	t.Setenv("GO_RAG_INPROCESS_ENABLED", "false")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer": "answer",
			"sources": []map[string]any{
				{
					"source_name":  "a.md",
					"clauses":      "1.A",
					"section_path": "Benefits > A",
				},
				{
					"snippet": "just snippet",
				},
			},
		})
	}))
	defer upstream.Close()

	client := &goRAGClient{
		baseURL: upstream.URL,
		client:  upstream.Client(),
	}
	answer, sources, err := client.queryInsurance("q", "prudential")
	if err != nil {
		t.Fatalf("queryInsurance: %v", err)
	}
	if answer != "answer" {
		t.Fatalf("unexpected answer: %q", answer)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources got %d", len(sources))
	}
	if !strings.Contains(sources[0], "a.md 1.A - Benefits > A") {
		t.Fatalf("unexpected source format: %q", sources[0])
	}
	if strings.TrimSpace(sources[1]) != "just snippet" {
		t.Fatalf("unexpected fallback source: %q", sources[1])
	}
}

func TestPythonClientEncodesQueryParameters(t *testing.T) {
	var captured url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answer":  "ok",
			"sources": []map[string]any{},
		})
	}))
	defer upstream.Close()

	client := &pythonRAGClient{
		baseURL: upstream.URL,
		client:  upstream.Client(),
	}
	_, _, err := client.queryInsurance("x y", "bluecross")
	if err != nil {
		t.Fatalf("queryInsurance: %v", err)
	}
	if captured.Get("q") != "x y" {
		t.Fatalf("expected q preserved, got %q", captured.Get("q"))
	}
	if captured.Get("provider") != "bluecross" {
		t.Fatalf("expected provider bluecross got %q", captured.Get("provider"))
	}
	if captured.Get("max_sources") != "3" {
		t.Fatalf("expected max_sources=3 got %q", captured.Get("max_sources"))
	}
}

func TestWriteChatJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	writeChatJSON(rr, http.StatusAccepted, map[string]string{"x": "y"})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status=202 got=%d", rr.Code)
	}
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `"x":"y"`) {
		t.Fatalf("unexpected json body: %s", string(body))
	}
}

func stringValueFromAny(v any) string {
	s, _ := v.(string)
	return s
}
