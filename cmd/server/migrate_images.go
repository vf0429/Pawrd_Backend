package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"golang.org/x/image/draw"
	"gorm.io/gorm"
)

func MigrateImageThumbnails(db *gorm.DB, publicBaseURL string) {
	publicBaseURL = strings.TrimRight(publicBaseURL, "/")
	uploadsDir := "assets/uploads"
	thumbsDir := "assets/uploads/thumbs"
	_ = os.MkdirAll(thumbsDir, 0755)

	// --- Phase 1: Fix any localhost URLs in existing records ---
	var allImages []models.PostImage
	if err := db.Where("thumbnail_url LIKE '%localhost%' OR image_url LIKE '%localhost%'").Find(&allImages).Error; err != nil {
		log.Printf("migrate_images: failed to query localhost images: %v", err)
	} else if len(allImages) > 0 {
		fixed := 0
		for _, img := range allImages {
			updates := map[string]interface{}{}

			if strings.Contains(img.ImageURL, "localhost") {
				filename := extractFilename(img.ImageURL)
				if filename != "" {
					updates["image_url"] = fmt.Sprintf("%s/uploads/%s", publicBaseURL, filename)
				}
			}

			if strings.Contains(img.ThumbnailURL, "localhost") {
				thumbFilename := extractFilename(img.ThumbnailURL)
				if thumbFilename != "" {
					updates["thumbnail_url"] = fmt.Sprintf("%s/uploads/thumbs/%s", publicBaseURL, thumbFilename)
				}
			}

			if len(updates) > 0 {
				if err := db.Model(&models.PostImage{}).Where("id = ?", img.ID).Updates(updates).Error; err != nil {
					log.Printf("migrate_images: failed to fix localhost URL for %s: %v", img.ID, err)
				} else {
					fixed++
				}
			}
		}
		if fixed > 0 {
			log.Printf("migrate_images: fixed %d localhost URLs", fixed)
		}
	}

	// --- Phase 2: Generate thumbnails for images that have none ---
	var images []models.PostImage
	if err := db.Where("thumbnail_url = '' OR thumbnail_url IS NULL").Find(&images).Error; err != nil {
		log.Printf("migrate_images: failed to query post_images: %v", err)
		return
	}
	if len(images) == 0 {
		return
	}

	migrated := 0

	for _, img := range images {
		filename := extractFilename(img.ImageURL)
		if filename == "" {
			continue
		}

		srcPath := filepath.Join(uploadsDir, filename)
		payload, err := os.ReadFile(srcPath)
		if err != nil {
			log.Printf("migrate_images: cannot read %s: %v", srcPath, err)
			continue
		}

		updates := map[string]interface{}{}

		// Fix hardcoded localhost URLs
		if strings.Contains(img.ImageURL, "localhost") {
			updates["image_url"] = fmt.Sprintf("%s/uploads/%s", publicBaseURL, filename)
		}

		// Decode dimensions if missing
		if img.Width == 0 || img.Height == 0 {
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(payload)); err == nil {
				updates["width"] = cfg.Width
				updates["height"] = cfg.Height
			}
		}

		// Generate thumbnail
		ext := strings.TrimSuffix(filename, filepath.Ext(filename))
		thumbFilename := ext + "_thumb.jpg"
		thumbPath := filepath.Join(thumbsDir, thumbFilename)

		if err := generateThumb(payload, thumbPath); err == nil {
			thumbURL := fmt.Sprintf("%s/uploads/thumbs/%s", publicBaseURL, thumbFilename)
			updates["thumbnail_url"] = thumbURL
		} else {
			log.Printf("migrate_images: thumbnail generation failed for %s: %v", filename, err)
		}

		if len(updates) > 0 {
			if err := db.Model(&models.PostImage{}).Where("id = ?", img.ID).Updates(updates).Error; err != nil {
				log.Printf("migrate_images: failed to update %s: %v", img.ID, err)
			} else {
				migrated++
			}
		}
	}

	if migrated > 0 {
		log.Printf("migrate_images: updated %d image records", migrated)
	}
}

func extractFilename(imageURL string) string {
	u, err := url.Parse(imageURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

const thumbMaxWidth = 400

func generateThumb(payload []byte, thumbPath string) error {
	src, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		return err
	}

	bounds := src.Bounds()
	origW := bounds.Dx()
	origH := bounds.Dy()

	if origW <= thumbMaxWidth {
		return os.WriteFile(thumbPath, payload, 0644)
	}

	newW := thumbMaxWidth
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
