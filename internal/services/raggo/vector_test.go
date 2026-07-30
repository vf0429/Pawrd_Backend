package raggo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveHybridRAG(t *testing.T) {
	if os.Getenv("RAG_LIVE_TEST") != "1" {
		t.Skip("set RAG_LIVE_TEST=1 to call configured embedding, rerank, and LLM services")
	}
	cfg := LoadConfig()
	if !cfg.EmbeddingEnabled {
		t.Fatal("live hybrid test requires embedding enabled")
	}
	result := AnswerQuery(
		cfg,
		"Compare Blue Cross and Prudential veterinary consultation limits.",
		"",
		"en",
		6,
	)
	if strings.TrimSpace(result.Answer) == "" {
		t.Fatalf("expected non-empty answer: %#v", result)
	}
	if result.AnswerMode == "go_rag_fallback_summary" || result.AnswerMode == "go_error" {
		t.Fatalf("expected generated hybrid answer, got mode=%s answer=%q", result.AnswerMode, result.Answer)
	}
	if len(result.Sources) < 2 {
		t.Fatalf("expected multiple grounded sources, got %#v", result.Sources)
	}
}

func TestVectorRankCandidatesBuildsIndexAndRanksSemanticMatch(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode embedding request: %v", err)
		}
		data := make([]map[string]any, 0, len(req.Input))
		for i, input := range req.Input {
			vector := []float32{1, 0}
			if strings.Contains(input, "unexpected veterinary expense") ||
				strings.Contains(input, "waiting period applies") {
				vector = []float32{0, 1}
			}
			data = append(data, map[string]any{
				"index":     i,
				"embedding": vector,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()

	resetVectorIndexForTest()
	t.Cleanup(resetVectorIndexForTest)
	cfg := Config{
		PersistDir:           t.TempDir(),
		DefaultMaxSources:    3,
		MaxAllowedSources:    6,
		EmbeddingEnabled:     true,
		EmbeddingBaseURL:     server.URL,
		EmbeddingModel:       "test-embedding",
		EmbeddingAPIKey:      "test-key",
		EmbeddingBatchSize:   8,
		EmbeddingTimeoutSecs: 5,
	}
	chunks := []Chunk{
		{
			Text: "Claims are submitted online.",
			Metadata: map[string]string{
				"provider": "bluecross",
				"language": "en",
			},
		},
		{
			Text: "A waiting period applies before unexpected veterinary expense coverage begins.",
			Metadata: map[string]string{
				"provider": "prudential",
				"language": "en",
			},
		},
	}

	got, err := vectorRankCandidates(cfg, chunks, "unexpected veterinary expense", "", "en", 3)
	if err != nil {
		t.Fatalf("vector rank: %v", err)
	}
	if len(got) != 1 || got[0].chunk.Metadata["provider"] != "prudential" {
		t.Fatalf("unexpected vector ranking: %#v", got)
	}
	if requests != 2 {
		t.Fatalf("expected one index batch and one query embedding request, got %d", requests)
	}
	if _, err := readVectorIndex(filepath.Join(cfg.PersistDir, "go_vector_index.json")); err != nil {
		t.Fatalf("expected persisted vector index: %v", err)
	}
}

func TestFuseCandidatesCombinesLexicalAndVectorRanks(t *testing.T) {
	a := Chunk{Text: "alpha", Metadata: map[string]string{"provider": "bluecross"}}
	b := Chunk{Text: "beta", Metadata: map[string]string{"provider": "prudential"}}
	c := Chunk{Text: "gamma", Metadata: map[string]string{"provider": "msig"}}

	got := fuseCandidates(
		[]rankedChunk{{chunk: a, score: 9}, {chunk: b, score: 8}},
		[]rankedChunk{{chunk: b, score: 0.9}, {chunk: c, score: 0.8}},
	)
	if len(got) != 3 {
		t.Fatalf("expected three fused candidates, got %d", len(got))
	}
	if got[0].chunk.Metadata["provider"] != "prudential" {
		t.Fatalf("candidate present in both rankings should lead, got %#v", got)
	}
}

func resetVectorIndexForTest() {
	globalVectorIndex.mu.Lock()
	defer globalVectorIndex.mu.Unlock()
	globalVectorIndex.loaded = false
	globalVectorIndex.key = ""
	globalVectorIndex.index = persistedVectorIndex{}
	globalVectorIndex.lastErr = nil
}
