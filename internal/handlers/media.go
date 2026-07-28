package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
)

func resolvePublicBaseURL(r *http.Request, fallback string) string {
	if r == nil {
		return strings.TrimRight(fallback, "/")
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}

	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}

	if host == "" {
		return strings.TrimRight(fallback, "/")
	}

	return strings.TrimRight(fmt.Sprintf("%s://%s", proto, host), "/")
}

const thumbnailMaxWidth = 400

func generateThumbnail(payload []byte, thumbPath string) error {
	src, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		return err
	}

	bounds := src.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	if origW <= thumbnailMaxWidth {
		// Already small enough — just write original as thumbnail
		return os.WriteFile(thumbPath, payload, 0644)
	}

	newW := thumbnailMaxWidth
	newH := origH * newW / origW

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	f, err := os.Create(thumbPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return jpeg.Encode(f, dst, &jpeg.Options{Quality: 80})
}

// NewMediaUploadHandler handles POST /media/upload — saves the uploaded file to
// assets/uploads/ and returns a JSON response compatible with iOS MediaUploadResponse.
func NewMediaUploadHandler(baseURL string) http.HandlerFunc {
	uploadsDir := "assets/uploads"
	thumbsDir := "assets/uploads/thumbs"
	_ = os.MkdirAll(uploadsDir, 0755)
	_ = os.MkdirAll(thumbsDir, 0755)

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
		payload, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "Failed to read file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		dst, err := os.Create(filepath.Join(uploadsDir, filename))
		if err != nil {
			http.Error(w, "Failed to create file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer dst.Close()

		if _, err := dst.Write(payload); err != nil {
			http.Error(w, "Failed to write file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		width := 0
		height := 0
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(payload)); err == nil {
			width = cfg.Width
			height = cfg.Height
		}

		publicBaseURL := resolvePublicBaseURL(r, baseURL)
		fileURL := fmt.Sprintf("%s/uploads/%s", publicBaseURL, filename)

		// Generate thumbnail
		thumbFilename := id + "_thumb.jpg"
		var thumbnailURL *string
		if err := generateThumbnail(payload, filepath.Join(thumbsDir, thumbFilename)); err == nil {
			u := fmt.Sprintf("%s/uploads/thumbs/%s", publicBaseURL, thumbFilename)
			thumbnailURL = &u
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"id":            id,
				"url":           fileURL,
				"width":         width,
				"height":        height,
				"thumbnail_url": thumbnailURL,
			},
		})
	}
}
