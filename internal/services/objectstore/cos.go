package objectstore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tencentyun/cos-go-sdk-v5"
)

type COSStore struct {
	client     *cos.Client
	secretID   string
	secretKey  string
	bucketName string
	expiresIn  time.Duration
}

func NewCOSStoreFromEnv() (*COSStore, error) {
	secretID := strings.TrimSpace(os.Getenv("COS_SECRET_ID"))
	secretKey := strings.TrimSpace(os.Getenv("COS_SECRET_KEY"))
	bucketURL := strings.TrimSpace(os.Getenv("COS_BUCKET_URL"))
	bucketName := strings.TrimSpace(os.Getenv("COS_BUCKET_NAME"))
	expiresRaw := strings.TrimSpace(os.Getenv("COS_SIGNED_URL_EXPIRE_SECONDS"))
	if expiresRaw == "" {
		expiresRaw = "900"
	}
	expiresIn, err := time.ParseDuration(expiresRaw + "s")
	if err != nil {
		return nil, fmt.Errorf("invalid COS_SIGNED_URL_EXPIRE_SECONDS: %w", err)
	}
	if secretID == "" || secretKey == "" || bucketURL == "" || bucketName == "" {
		return nil, fmt.Errorf("cos env missing, require COS_SECRET_ID/COS_SECRET_KEY/COS_BUCKET_URL/COS_BUCKET_NAME")
	}

	u, err := url.Parse(bucketURL)
	if err != nil {
		return nil, fmt.Errorf("invalid COS_BUCKET_URL: %w", err)
	}
	baseURL := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(baseURL, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  secretID,
			SecretKey: secretKey,
		},
	})

	return &COSStore{
		client:     client,
		secretID:   secretID,
		secretKey:  secretKey,
		bucketName: bucketName,
		expiresIn:  expiresIn,
	}, nil
}

func (s *COSStore) BuildObjectKey(petID, filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".jpg"
	}
	petID = sanitizeSegment(petID)
	if petID == "" {
		petID = "unknown_pet"
	}
	now := time.Now().UTC()
	return fmt.Sprintf("reports/%s/%04d/%02d/%s%s", petID, now.Year(), int(now.Month()), uuid.NewString(), ext)
}

func (s *COSStore) BuildBlogObjectKey(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".jpg"
	}
	now := time.Now().UTC()
	return fmt.Sprintf("blog/%04d/%02d/%s%s", now.Year(), int(now.Month()), uuid.NewString(), ext)
}

func (s *COSStore) PresignUpload(objectKey string) (string, time.Duration, error) {
	u, err := s.client.Object.GetPresignedURL(context.Background(), http.MethodPut, objectKey, s.secretID, s.secretKey, s.expiresIn, nil)
	if err != nil {
		return "", 0, err
	}
	return u.String(), s.expiresIn, nil
}

func (s *COSStore) PresignRead(objectKey string) (string, time.Duration, error) {
	u, err := s.client.Object.GetPresignedURL(context.Background(), http.MethodGet, objectKey, s.secretID, s.secretKey, s.expiresIn, nil)
	if err != nil {
		return "", 0, err
	}
	return u.String(), s.expiresIn, nil
}

func sanitizeSegment(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, " ", "_")
	v = strings.ReplaceAll(v, "/", "_")
	v = strings.ReplaceAll(v, "\\", "_")
	return v
}
