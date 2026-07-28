package shopify

import (
	"net/http"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
)

func TestNewClientPrefersPrivateStorefrontToken(t *testing.T) {
	client, err := NewClient(&config.Config{
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontPrivateToken: "private-token",
		ShopifyStorefrontToken:        "public-token",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.myshopify.com/api/graphql.json", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	client.setStorefrontAuthHeader(req)

	if got := req.Header.Get(privateStorefrontTokenHeader); got != "private-token" {
		t.Fatalf("%s = %q, want private-token", privateStorefrontTokenHeader, got)
	}
	if got := req.Header.Get(publicStorefrontTokenHeader); got != "" {
		t.Fatalf("%s = %q, want empty", publicStorefrontTokenHeader, got)
	}
}

func TestNewClientFallsBackToPublicStorefrontToken(t *testing.T) {
	client, err := NewClient(&config.Config{
		ShopifyDomain:          "example.myshopify.com",
		ShopifyStorefrontToken: "public-token",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.myshopify.com/api/graphql.json", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	client.setStorefrontAuthHeader(req)

	if got := req.Header.Get(publicStorefrontTokenHeader); got != "public-token" {
		t.Fatalf("%s = %q, want public-token", publicStorefrontTokenHeader, got)
	}
	if got := req.Header.Get(privateStorefrontTokenHeader); got != "" {
		t.Fatalf("%s = %q, want empty", privateStorefrontTokenHeader, got)
	}
}
