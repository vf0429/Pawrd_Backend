package handlers

import "net/http"

// NewRAGTestPageHandler serves a tiny local browser UI for manually testing
// the insurance RAG endpoints.
func NewRAGTestPageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "assets/rag_test.html")
	}
}
