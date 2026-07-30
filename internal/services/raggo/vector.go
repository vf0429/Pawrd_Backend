package raggo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const vectorIndexVersion = 1

type embeddingClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type persistedVectorIndex struct {
	Version           int         `json:"version"`
	Model             string      `json:"model"`
	CorpusFingerprint string      `json:"corpus_fingerprint"`
	Dimension         int         `json:"dimension"`
	Vectors           [][]float32 `json:"vectors"`
}

type vectorIndexState struct {
	mu      sync.Mutex
	loaded  bool
	key     string
	index   persistedVectorIndex
	lastErr error
}

var globalVectorIndex vectorIndexState

// WarmVectorIndex prepares and caches all corpus vectors. It is safe to call in
// the background during server startup; concurrent queries wait for the same
// build instead of creating duplicate embedding requests.
func WarmVectorIndex(cfg Config) error {
	client := newEmbeddingClient(cfg)
	if client == nil {
		return nil
	}
	chunks, err := LoadChunks(cfg)
	if err != nil {
		return err
	}
	_, err = loadOrBuildVectorIndex(cfg, client, chunks)
	return err
}

func newEmbeddingClient(cfg Config) *embeddingClient {
	if !cfg.EmbeddingEnabled ||
		strings.TrimSpace(cfg.EmbeddingBaseURL) == "" ||
		strings.TrimSpace(cfg.EmbeddingModel) == "" ||
		strings.TrimSpace(cfg.EmbeddingAPIKey) == "" {
		return nil
	}
	timeout := time.Duration(cfg.EmbeddingTimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &embeddingClient{
		baseURL: strings.TrimRight(cfg.EmbeddingBaseURL, "/"),
		apiKey:  cfg.EmbeddingAPIKey,
		model:   cfg.EmbeddingModel,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *embeddingClient) embed(inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	payload := map[string]any{
		"model": c.model,
		"input": inputs,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.client.Do(req)
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
		return nil, fmt.Errorf("embedding status %d body=%s", resp.StatusCode, msg)
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding count mismatch: got %d want %d", len(parsed.Data), len(inputs))
	}
	out := make([][]float32, len(inputs))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(out) || len(item.Embedding) == 0 {
			return nil, fmt.Errorf("invalid embedding item index=%d", item.Index)
		}
		out[item.Index] = normalizeVector(item.Embedding)
	}
	for i := range out {
		if len(out[i]) == 0 {
			return nil, fmt.Errorf("missing embedding at index %d", i)
		}
	}
	return out, nil
}

func vectorRankCandidates(cfg Config, chunks []Chunk, question, provider, language string, maxSources int) ([]rankedChunk, error) {
	client := newEmbeddingClient(cfg)
	if client == nil {
		return nil, nil
	}
	index, err := loadOrBuildVectorIndex(cfg, client, chunks)
	if err != nil {
		return nil, err
	}
	queryVectors, err := client.embed([]string{question})
	if err != nil {
		return nil, err
	}
	query := queryVectors[0]
	out := make([]rankedChunk, 0, len(chunks))
	for i, ch := range chunks {
		if i >= len(index.Vectors) {
			break
		}
		if provider != "" && ch.Metadata["provider"] != provider {
			continue
		}
		if language != "" && ch.Metadata["language"] != language {
			continue
		}
		score := cosineSimilarity(query, index.Vectors[i])
		if score <= 0 {
			continue
		}
		out = append(out, rankedChunk{chunk: ch, score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	limit := candidatePoolLimit(maxSources)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func loadOrBuildVectorIndex(cfg Config, client *embeddingClient, chunks []Chunk) (persistedVectorIndex, error) {
	fingerprint := chunkFingerprint(chunks)
	cacheKey := strings.Join([]string{cfg.PersistDir, client.model, fingerprint}, "|")

	globalVectorIndex.mu.Lock()
	defer globalVectorIndex.mu.Unlock()
	if globalVectorIndex.loaded && globalVectorIndex.key == cacheKey {
		return globalVectorIndex.index, globalVectorIndex.lastErr
	}

	indexPath := filepath.Join(cfg.PersistDir, "go_vector_index.json")
	if index, err := readVectorIndex(indexPath); err == nil &&
		index.Version == vectorIndexVersion &&
		index.Model == client.model &&
		index.CorpusFingerprint == fingerprint &&
		len(index.Vectors) == len(chunks) {
		globalVectorIndex.loaded = true
		globalVectorIndex.key = cacheKey
		globalVectorIndex.index = index
		globalVectorIndex.lastErr = nil
		return index, nil
	}

	index, err := buildVectorIndex(cfg, client, chunks, fingerprint)
	if err != nil {
		globalVectorIndex.loaded = false
		globalVectorIndex.key = ""
		globalVectorIndex.index = persistedVectorIndex{}
		globalVectorIndex.lastErr = nil
		return persistedVectorIndex{}, err
	}
	if err := writeVectorIndex(indexPath, index); err != nil {
		// A read-only or ephemeral filesystem must not disable in-memory vector retrieval.
		globalVectorIndex.loaded = true
		globalVectorIndex.key = cacheKey
		globalVectorIndex.index = index
		globalVectorIndex.lastErr = nil
		return index, nil
	}
	globalVectorIndex.loaded = true
	globalVectorIndex.key = cacheKey
	globalVectorIndex.index = index
	globalVectorIndex.lastErr = nil
	return index, nil
}

func buildVectorIndex(cfg Config, client *embeddingClient, chunks []Chunk, fingerprint string) (persistedVectorIndex, error) {
	batchSize := cfg.EmbeddingBatchSize
	if batchSize <= 0 {
		batchSize = 16
	}
	vectors := make([][]float32, 0, len(chunks))
	for start := 0; start < len(chunks); start += batchSize {
		end := start + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		inputs := make([]string, 0, end-start)
		for _, ch := range chunks[start:end] {
			inputs = append(inputs, embeddingDocument(ch))
		}
		batch, err := client.embed(inputs)
		if err != nil {
			return persistedVectorIndex{}, fmt.Errorf("build vector index batch %d-%d: %w", start, end, err)
		}
		vectors = append(vectors, batch...)
	}
	dimension := 0
	if len(vectors) > 0 {
		dimension = len(vectors[0])
	}
	return persistedVectorIndex{
		Version:           vectorIndexVersion,
		Model:             client.model,
		CorpusFingerprint: fingerprint,
		Dimension:         dimension,
		Vectors:           vectors,
	}, nil
}

func readVectorIndex(path string) (persistedVectorIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return persistedVectorIndex{}, err
	}
	var index persistedVectorIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return persistedVectorIndex{}, err
	}
	return index, nil
}

func writeVectorIndex(path string, index persistedVectorIndex) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".go-vector-index-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func chunkFingerprint(chunks []Chunk) string {
	h := sha256.New()
	for _, ch := range chunks {
		io.WriteString(h, embeddingDocument(ch))
		io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func embeddingDocument(ch Chunk) string {
	return rerankDocument(ch)
}

func normalizeVector(vector []float32) []float32 {
	var norm float64
	for _, value := range vector {
		norm += float64(value * value)
	}
	if norm <= 0 {
		return vector
	}
	scale := float32(1 / math.Sqrt(norm))
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = value * scale
	}
	return out
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i] * b[i])
	}
	return dot
}
