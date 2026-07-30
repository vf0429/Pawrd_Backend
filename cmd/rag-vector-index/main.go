package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/raggo"
)

func main() {
	_ = godotenv.Load()
	cfg := raggo.LoadConfig()
	if !cfg.EmbeddingEnabled {
		fmt.Fprintln(os.Stderr, "embedding is disabled; set HK_INSURANCE_RAG_EMBEDDING_ENABLED=true")
		os.Exit(1)
	}
	if strings.TrimSpace(cfg.EmbeddingBaseURL) == "" ||
		strings.TrimSpace(cfg.EmbeddingModel) == "" ||
		strings.TrimSpace(cfg.EmbeddingAPIKey) == "" {
		fmt.Fprintln(os.Stderr, "embedding configuration is incomplete")
		os.Exit(1)
	}

	started := time.Now()
	fmt.Printf("Building or loading Go RAG vector index with %s...\n", cfg.EmbeddingModel)
	if err := raggo.WarmVectorIndex(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "vector index failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Vector index ready in %s\n", time.Since(started).Round(time.Millisecond))
}
