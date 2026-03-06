package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// NewMediaUploadHandler handles POST /media/upload — saves the uploaded file to
// assets/uploads/ and returns a JSON response compatible with iOS MediaUploadResponse.
func NewMediaUploadHandler(baseURL string) http.HandlerFunc {
	uploadsDir := "assets/uploads"
	_ = os.MkdirAll(uploadsDir, 0755)

	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Accept up to 32 MB
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "Failed to parse form: "+err.Error(), http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "No file provided", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Preserve extension; default to .jpg
		ext := strings.ToLower(filepath.Ext(header.Filename))
		if ext == "" {
			ext = ".jpg"
		}

		id := uuid.New().String()
		filename := id + ext
		dst, err := os.Create(filepath.Join(uploadsDir, filename))
		if err != nil {
			http.Error(w, "Failed to create file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, "Failed to write file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		fileURL := fmt.Sprintf("%s/uploads/%s", baseURL, filename)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"id":            id,
				"url":           fileURL,
				"thumbnail_url": nil,
			},
		})
	}
}
