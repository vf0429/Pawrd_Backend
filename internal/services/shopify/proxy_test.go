package shopify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
)

func TestNewClientPrefersPrivateStorefrontToken(t *testing.T) {
	client, err := NewClient(&config.Config{
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontAPIVersion:   "2026-07",
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
		ShopifyDomain:               "example.myshopify.com",
		ShopifyStorefrontAPIVersion: "2026-07",
		ShopifyStorefrontToken:      "public-token",
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

func TestNewClientUsesConfiguredStorefrontAPIVersion(t *testing.T) {
	client, err := NewClient(&config.Config{
		ShopifyDomain:                 "https://example.myshopify.com/",
		ShopifyStorefrontAPIVersion:   "2026-07",
		ShopifyStorefrontPrivateToken: "private-token",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if got, want := client.endpoint, "https://example.myshopify.com/api/2026-07/graphql.json"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestCatalogQueriesDoNotRequireExactInventoryScope(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Storefront request: %v", err)
			return
		}
		if strings.Contains(request.Query, "quantityAvailable") {
			t.Errorf("catalog query must not depend on exact-inventory Storefront scope")
		}
		switch {
		case strings.Contains(request.Query, "query GetProducts"):
			_, _ = w.Write([]byte(`{"data":{"products":{"edges":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`))
		case strings.Contains(request.Query, "query SearchProducts"):
			_, _ = w.Write([]byte(`{"data":{"products":{"edges":[]}}}`))
		case strings.Contains(request.Query, "query GetProduct"):
			_, _ = w.Write([]byte(`{"data":{"product":null}}`))
		default:
			t.Errorf("unexpected Storefront query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL, storefrontToken: "private-token",
		authHeader: privateStorefrontTokenHeader, httpClient: server.Client(),
	}
	if _, _, _, err := client.FetchProducts(5, ""); err != nil {
		t.Fatalf("FetchProducts() error = %v", err)
	}
	if _, err := client.SearchProducts("cat", 5); err != nil {
		t.Fatalf("SearchProducts() error = %v", err)
	}
	if _, err := client.FetchProductByHandle("missing"); err == nil {
		t.Fatal("FetchProductByHandle() unexpectedly found a missing product")
	}
	if requestCount != 3 {
		t.Fatalf("Storefront request count = %d, want 3", requestCount)
	}
}
