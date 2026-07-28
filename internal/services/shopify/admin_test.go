package shopify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestAdminClient(server *httptest.Server, provider *adminTokenProvider) *AdminClient {
	provider.endpoint = server.URL + "/admin/oauth/access_token"
	provider.httpClient = server.Client()
	if provider.now == nil {
		provider.now = time.Now
	}
	return &AdminClient{
		endpoint:      server.URL + "/admin/api/2026-07/graphql.json",
		tokenProvider: provider,
		httpClient:    server.Client(),
	}
}

func executeTestQuery(t *testing.T, client *AdminClient) {
	t.Helper()
	var data struct {
		Shop struct {
			ID string `json:"id"`
		} `json:"shop"`
	}
	if err := client.execute(context.Background(), "query { shop { id } }", nil, &data); err != nil {
		t.Fatal(err)
	}
	if data.Shop.ID != "gid://shopify/Shop/1" {
		t.Fatalf("unexpected shop id %q", data.Shop.ID)
	}
}

func TestAdminTokenProviderCachesAcrossConcurrentRequests(t *testing.T) {
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/oauth/access_token":
			tokenCalls.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			assertTokenForm(t, r.Form)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "dynamic-token",
				"expires_in":   86399,
			})
		case "/admin/api/2026-07/graphql.json":
			if got := r.Header.Get("X-Shopify-Access-Token"); got != "dynamic-token" {
				t.Errorf("unexpected Admin token %q", got)
			}
			_, _ = w.Write([]byte(`{"data":{"shop":{"id":"gid://shopify/Shop/1"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{
		clientID: "client-id", clientSecret: "client-secret",
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			executeTestQuery(t, client)
		}()
	}
	wg.Wait()
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("expected one token exchange, got %d", got)
	}
}

func TestAdminTokenProviderRefreshesBeforeExpiry(t *testing.T) {
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/oauth/access_token":
			call := tokenCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "dynamic-token-" + string(rune('0'+call)),
				"expires_in":   86399,
			})
		case "/admin/api/2026-07/graphql.json":
			_, _ = w.Write([]byte(`{"data":{"shop":{"id":"gid://shopify/Shop/1"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := &adminTokenProvider{clientID: "client-id", clientSecret: "client-secret"}
	client := newTestAdminClient(server, provider)
	executeTestQuery(t, client)
	provider.mu.Lock()
	provider.refreshAt = time.Now().Add(-time.Minute)
	provider.mu.Unlock()
	executeTestQuery(t, client)
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("expected token refresh, got %d exchanges", got)
	}
}

func TestAdminClientRefreshesAndRetriesOnceAfterUnauthorized(t *testing.T) {
	var tokenCalls atomic.Int32
	var apiCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/oauth/access_token":
			call := tokenCalls.Add(1)
			token := "expired-token"
			if call > 1 {
				token = "fresh-token"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": token,
				"expires_in":   86399,
			})
		case "/admin/api/2026-07/graphql.json":
			apiCalls.Add(1)
			if r.Header.Get("X-Shopify-Access-Token") == "expired-token" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":{"shop":{"id":"gid://shopify/Shop/1"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{
		clientID: "client-id", clientSecret: "client-secret",
	})
	executeTestQuery(t, client)
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("expected two token exchanges, got %d", got)
	}
	if got := apiCalls.Load(); got != 2 {
		t.Fatalf("expected one API retry, got %d calls", got)
	}
}

func TestAdminTokenProviderFallsBackToStaticToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/oauth/access_token":
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		case "/admin/api/2026-07/graphql.json":
			if got := r.Header.Get("X-Shopify-Access-Token"); got != "cutover-token" {
				t.Errorf("unexpected fallback token %q", got)
			}
			_, _ = w.Write([]byte(`{"data":{"shop":{"id":"gid://shopify/Shop/1"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{
		clientID: "client-id", clientSecret: "client-secret", staticToken: "cutover-token",
	})
	executeTestQuery(t, client)
}

func TestCreateOrderUsesSafeTagsAndSourceIdentifier(t *testing.T) {
	const paymentIntentID = "pi_3TxoaXCtgcSY1r8p1zCPKqtT"
	var orderVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode GraphQL request: %v", err)
			return
		}
		orderVariables, _ = request.Variables["order"].(map[string]any)
		_, _ = w.Write([]byte(`{"data":{"orderCreate":{"order":{
			"id":"gid://shopify/Order/1",
			"legacyResourceId":"1",
			"name":"#1001",
			"lineItems":{"nodes":[{"id":"gid://shopify/LineItem/1"}]}
		},"userErrors":[]}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	_, err := client.CreateOrder(context.Background(), AdminOrderInput{
		Currency:  "HKD",
		Amount:    "25.02",
		PaymentID: paymentIntentID,
		Lines: []AdminOrderLineInput{{
			VariantID: "gid://shopify/ProductVariant/1",
			Quantity:  1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if orderVariables["sourceIdentifier"] != paymentIntentID {
		t.Fatalf("unexpected source identifier: %#v", orderVariables["sourceIdentifier"])
	}
	tags, ok := orderVariables["tags"].([]any)
	if !ok {
		t.Fatalf("unexpected tags payload: %#v", orderVariables["tags"])
	}
	if !slices.Equal(tags, []any{"Pawrd", "Stripe"}) {
		t.Fatalf("unexpected tags: %#v", tags)
	}
}

func TestEnsureWebhookSubscriptionsCreatesOnlyMissingForCallback(t *testing.T) {
	const callbackURL = "https://api.pawrd.top/api/shop/webhooks/shopify"
	var createdTopics []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Shopify-Access-Token"); got != "static-token" {
			t.Errorf("unexpected Admin token %q", got)
		}
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode GraphQL request: %v", err)
			return
		}
		switch {
		case strings.Contains(request.Query, "PawrdWebhookSubscriptions"):
			_, _ = w.Write([]byte(`{"data":{"webhookSubscriptions":{"nodes":[
				{"topic":"FULFILLMENTS_CREATE","uri":"https://api.pawrd.top/api/shop/webhooks/shopify"},
				{"topic":"RETURNS_REQUEST","uri":"https://old.example.com/webhook"}
			]}}}`))
		case strings.Contains(request.Query, "CreatePawrdWebhook"):
			topic, _ := request.Variables["topic"].(string)
			createdTopics = append(createdTopics, topic)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"webhookSubscriptionCreate": map[string]any{
						"webhookSubscription": map[string]any{
							"id": "gid://shopify/WebhookSubscription/" + topic, "topic": topic, "uri": callbackURL,
						},
						"userErrors": []any{},
					},
				},
			})
		default:
			t.Errorf("unexpected GraphQL query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	created, err := client.ensureWebhookSubscriptions(context.Background(), callbackURL, []string{
		"FULFILLMENTS_CREATE", "RETURNS_REQUEST", "REFUNDS_CREATE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 2 {
		t.Fatalf("expected two subscriptions to be created, got %d", created)
	}
	if !slices.Equal(createdTopics, []string{"RETURNS_REQUEST", "REFUNDS_CREATE"}) {
		t.Fatalf("unexpected created topics: %#v", createdTopics)
	}
}

func TestEnsureWebhookSubscriptionsRequiresHTTPSCallback(t *testing.T) {
	client := &AdminClient{}
	if _, err := client.ensureWebhookSubscriptions(context.Background(), "http://example.com/webhook", nil); err == nil {
		t.Fatal("expected non-HTTPS callback to be rejected")
	}
}

func assertTokenForm(t *testing.T, form url.Values) {
	t.Helper()
	if form.Get("grant_type") != "client_credentials" ||
		form.Get("client_id") != "client-id" ||
		form.Get("client_secret") != "client-secret" {
		t.Fatalf("unexpected token form: %#v", form)
	}
}
