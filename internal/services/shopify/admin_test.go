package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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
	var optionsVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode GraphQL request: %v", err)
			return
		}
		orderVariables, _ = request.Variables["order"].(map[string]any)
		optionsVariables, _ = request.Variables["options"].(map[string]any)
		_, _ = w.Write([]byte(`{"data":{"orderCreate":{"order":{
			"id":"gid://shopify/Order/1",
			"legacyResourceId":"1",
			"name":"#1001",
			"totalPriceSet":{"shopMoney":{"amount":"25.02","currencyCode":"HKD"}},
			"lineItems":{"nodes":[{"id":"gid://shopify/LineItem/1"}]}
		},"userErrors":[]}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	_, err := client.CreateOrder(context.Background(), AdminOrderInput{
		Currency:        "HKD",
		CustomerEmail:   "alice@example.com",
		CustomerPhone:   "+85261234567",
		ShippingName:    "Alice Test",
		ShippingPhone:   "+85261234567",
		ShippingAddress: "1 Test Street",
		ShippingCity:    "Wan Chai",
		ShippingRegion:  "Hong Kong Island",
		Amount:          "25.02",
		PaymentID:       paymentIntentID,
		QuoteID:         "quote-123",
		ShippingTitle:   "Hong Kong Standard",
		ShippingCode:    "STANDARD",
		ShippingAmount:  "5.02",
		DiscountCode:    "PAWRD5",
		DiscountAmount:  "5.00",
		TaxTitle:        "Sales tax",
		TaxAmount:       "1.00",
		TaxRate:         "0.05",
		Lines: []AdminOrderLineInput{{
			VariantID:        "gid://shopify/ProductVariant/1",
			Quantity:         1,
			RequiresShipping: true,
			UnitPrice:        "24.00",
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
	lineItems, ok := orderVariables["lineItems"].([]any)
	if !ok || len(lineItems) != 1 {
		t.Fatalf("unexpected line items payload: %#v", orderVariables["lineItems"])
	}
	lineItem, ok := lineItems[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected line item: %#v", lineItems[0])
	}
	if lineItem["requiresShipping"] != true {
		t.Fatalf("physical line item must require shipping: %#v", lineItem)
	}
	priceSet, ok := lineItem["priceSet"].(map[string]any)
	if !ok {
		t.Fatalf("line item must preserve quoted price: %#v", lineItem)
	}
	shopMoney, ok := priceSet["shopMoney"].(map[string]any)
	if !ok || shopMoney["amount"] != "24.00" || shopMoney["currencyCode"] != "HKD" {
		t.Fatalf("unexpected quoted line price: %#v", priceSet)
	}
	shippingAddress, ok := orderVariables["shippingAddress"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected shipping address payload: %#v", orderVariables["shippingAddress"])
	}
	if shippingAddress["firstName"] != "Alice Test" ||
		shippingAddress["address1"] != "1 Test Street" ||
		shippingAddress["city"] != "Wan Chai" ||
		shippingAddress["provinceCode"] != "HK" ||
		shippingAddress["countryCode"] != "HK" {
		t.Fatalf("unexpected shipping address: %#v", shippingAddress)
	}
	billingAddress, ok := orderVariables["billingAddress"].(map[string]any)
	if !ok || !reflect.DeepEqual(billingAddress, shippingAddress) {
		t.Fatalf(
			"billing address must match the collected shipping address: shipping=%#v billing=%#v",
			shippingAddress,
			orderVariables["billingAddress"],
		)
	}
	customer, ok := orderVariables["customer"].(map[string]any)
	if !ok {
		t.Fatalf("missing Shopify customer association: %#v", orderVariables["customer"])
	}
	toUpsert, ok := customer["toUpsert"].(map[string]any)
	if !ok ||
		toUpsert["email"] != "alice@example.com" ||
		toUpsert["firstName"] != "Alice Test" {
		t.Fatalf("unexpected Shopify customer upsert: %#v", customer)
	}
	if _, hasPhone := toUpsert["phone"]; hasPhone {
		t.Fatalf("orderCreate must not claim a globally unique customer phone: %#v", toUpsert)
	}
	if optionsVariables["inventoryBehaviour"] != "DECREMENT_OBEYING_POLICY" ||
		optionsVariables["sendReceipt"] != true ||
		optionsVariables["sendFulfillmentReceipt"] != false {
		t.Fatalf("unsafe orderCreate options: %#v", optionsVariables)
	}
	shippingLines, ok := orderVariables["shippingLines"].([]any)
	if !ok || len(shippingLines) != 1 {
		t.Fatalf("unexpected shipping lines: %#v", orderVariables["shippingLines"])
	}
	shippingLine, ok := shippingLines[0].(map[string]any)
	if !ok || shippingLine["title"] != "Hong Kong Standard" || shippingLine["code"] != "STANDARD" {
		t.Fatalf("unexpected shipping line: %#v", shippingLines[0])
	}
	discount, ok := orderVariables["discountCode"].(map[string]any)
	if !ok {
		t.Fatalf("missing discount code: %#v", orderVariables["discountCode"])
	}
	fixedDiscount, ok := discount["itemFixedDiscountCode"].(map[string]any)
	if !ok || fixedDiscount["code"] != "PAWRD5" {
		t.Fatalf("unexpected discount code payload: %#v", discount)
	}
	taxLines, ok := orderVariables["taxLines"].([]any)
	if !ok || len(taxLines) != 1 {
		t.Fatalf("unexpected tax lines: %#v", orderVariables["taxLines"])
	}
	taxLine, ok := taxLines[0].(map[string]any)
	if !ok || taxLine["rate"] != "0.05" || taxLine["title"] != "Sales tax" {
		t.Fatalf("unexpected tax line: %#v", taxLines[0])
	}
	customAttributes, ok := orderVariables["customAttributes"].([]any)
	if !ok || len(customAttributes) != 1 {
		t.Fatalf("missing quote attribute: %#v", orderVariables["customAttributes"])
	}
}

func TestCreateOrderClassifiesMutationUserErrorAsDeterministicRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"orderCreate":{"order":null,"userErrors":[{
			"field":["order","lineItems","0","quantity"],
			"message":"Inventory is no longer available"
		}]}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	_, err := client.CreateOrder(context.Background(), AdminOrderInput{
		Currency: "HKD", Amount: "10.00", PaymentID: "pi_inventory_rejected",
		Lines: []AdminOrderLineInput{{
			VariantID: "gid://shopify/ProductVariant/1",
			Quantity:  1, RequiresShipping: true, UnitPrice: "10.00",
		}},
	})
	if !errors.Is(err, ErrOrderCreateRejected) {
		t.Fatalf("orderCreate userError was not classified: %v", err)
	}
}

func TestCreateOrderReplicatesFreeShippingCode(t *testing.T) {
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
			"id":"gid://shopify/Order/1","legacyResourceId":"1","name":"#1001",
			"totalPriceSet":{"shopMoney":{"amount":"20.00","currencyCode":"HKD"}},
			"lineItems":{"nodes":[{"id":"gid://shopify/LineItem/1"}]}
		},"userErrors":[]}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	if _, err := client.CreateOrder(context.Background(), AdminOrderInput{
		Currency: "HKD", Amount: "20.00", PaymentID: "pi_free_shipping",
		ShippingTitle: "Standard", ShippingAmount: "0.00",
		DiscountCode: "SHIPFREE", DiscountTargetType: "SHIPPING_LINE",
		Lines: []AdminOrderLineInput{{
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			RequiresShipping: true, UnitPrice: "20.00",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	discount, ok := orderVariables["discountCode"].(map[string]any)
	if !ok {
		t.Fatalf("missing discount code: %#v", orderVariables["discountCode"])
	}
	freeShipping, ok := discount["freeShippingDiscountCode"].(map[string]any)
	if !ok || freeShipping["code"] != "SHIPFREE" {
		t.Fatalf("unexpected free-shipping code payload: %#v", discount)
	}
}

func TestCreateOrderPreservesPartialShippingDiscountAsFixedAmount(t *testing.T) {
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
			"id":"gid://shopify/Order/2","legacyResourceId":"2","name":"#1002",
			"totalPriceSet":{"shopMoney":{"amount":"23.00","currencyCode":"HKD"}},
			"lineItems":{"nodes":[{"id":"gid://shopify/LineItem/2"}]}
		},"userErrors":[]}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	if _, err := client.CreateOrder(context.Background(), AdminOrderInput{
		Currency: "HKD", Amount: "23.00", PaymentID: "pi_partial_shipping",
		ShippingTitle: "Standard", ShippingAmount: "5.00",
		DiscountCode: "SHIP2", DiscountAmount: "2.00",
		DiscountTargetType: "SHIPPING_LINE",
		Lines: []AdminOrderLineInput{{
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			RequiresShipping: true, UnitPrice: "20.00",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	discount := orderVariables["discountCode"].(map[string]any)
	fixedDiscount, ok := discount["itemFixedDiscountCode"].(map[string]any)
	if !ok || fixedDiscount["code"] != "SHIP2" {
		t.Fatalf("partial shipping discount must preserve exact fixed amount: %#v", discount)
	}
	if _, wronglyFree := discount["freeShippingDiscountCode"]; wronglyFree {
		t.Fatalf("partial shipping discount was incorrectly mapped to free shipping: %#v", discount)
	}
	attributes, ok := orderVariables["customAttributes"].([]any)
	if !ok || len(attributes) != 1 {
		t.Fatalf("partial shipping target audit attribute missing: %#v", orderVariables["customAttributes"])
	}
	attribute, ok := attributes[0].(map[string]any)
	if !ok || attribute["key"] != "Pawrd original discount target" ||
		attribute["value"] != "SHIPPING_LINE" {
		t.Fatalf("unexpected partial shipping audit attribute: %#v", attributes[0])
	}
}

func TestFindOrderBySourceIdentifier(t *testing.T) {
	const paymentIntentID = "pi_3TxoaXCtgcSY1r8p1zCPKqtT"
	var searchQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode GraphQL request: %v", err)
			return
		}
		if !strings.Contains(request.Query, "PawrdOrderBySourceIdentifier") {
			t.Errorf("unexpected GraphQL query: %s", request.Query)
		}
		searchQuery, _ = request.Variables["query"].(string)
		_, _ = w.Write([]byte(`{"data":{"orders":{"nodes":[{
			"id":"gid://shopify/Order/99",
			"legacyResourceId":"99",
			"name":"#1099",
			"totalPriceSet":{"shopMoney":{"amount":"20.00","currencyCode":"HKD"}},
			"lineItems":{"nodes":[
				{"id":"gid://shopify/LineItem/101"},
				{"id":"gid://shopify/LineItem/102"}
			]}
		}]}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "static-token"})
	result, err := client.FindOrderBySourceIdentifier(context.Background(), paymentIntentID)
	if err != nil {
		t.Fatal(err)
	}
	if searchQuery != "source_identifier:"+paymentIntentID {
		t.Fatalf("unexpected source search query %q", searchQuery)
	}
	if result == nil || result.ID != "gid://shopify/Order/99" ||
		result.TotalAmount != "20.00" || result.Currency != "HKD" ||
		!slices.Equal(result.LineItemIDs, []string{
			"gid://shopify/LineItem/101", "gid://shopify/LineItem/102",
		}) {
		t.Fatalf("unexpected lookup result: %#v", result)
	}
}

func TestFindOrderBySourceIdentifierRejectsSearchInjection(t *testing.T) {
	client := &AdminClient{}
	if _, err := client.FindOrderBySourceIdentifier(
		context.Background(),
		`pi_123 OR source_identifier:*`,
	); err == nil {
		t.Fatal("expected an unsafe source identifier to be rejected")
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
