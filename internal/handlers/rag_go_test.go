package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoRAGReadyzReturnsServiceUnavailableWhenLLMConfigMissing(t *testing.T) {
	t.Setenv("HK_INSURANCE_RAG_LLM_BASE_URL", "")
	t.Setenv("HK_INSURANCE_RAG_LLM_MODEL", "")
	t.Setenv("HK_INSURANCE_RAG_LLM_API_KEY", "")

	handler := NewGoRAGReadyzHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/rag/go/readyz", nil)
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status=503 got=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"llm_configured":false`) {
		t.Fatalf("expected llm_configured=false body=%s", rr.Body.String())
	}
}

func TestGoRAGReadyzReturnsOKWhenCorpusAndLLMConfigExist(t *testing.T) {
	t.Setenv("HK_INSURANCE_RAG_LLM_BASE_URL", "https://example.com/v1")
	t.Setenv("HK_INSURANCE_RAG_LLM_MODEL", "test-model")
	t.Setenv("HK_INSURANCE_RAG_LLM_API_KEY", "test-key")

	handler := NewGoRAGReadyzHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/rag/go/readyz", nil)
	rr := httptest.NewRecorder()

	handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("expected ok=true body=%s", rr.Body.String())
	}
}
