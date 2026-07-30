package shopify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecordExternalRefundUsesExactIdempotentTransactionInput(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch requests.Add(1) {
		case 1:
			if !strings.Contains(body.Query, "PawrdExternalStripeRefundParent") {
				t.Errorf("first request was not the parent transaction query: %s", body.Query)
			}
			if body.Variables["orderId"] != "gid://shopify/Order/1002" {
				t.Errorf("unexpected orderId variable: %#v", body.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"order":{"id":"gid://shopify/Order/1002","transactions":[{"id":"gid://shopify/OrderTransaction/10","gateway":"Stripe","kind":"SALE","status":"SUCCESS","manualPaymentGateway":true,"amountSet":{"presentmentMoney":{"amount":"85.00","currencyCode":"HKD"}}}]}}}`))
		case 2:
			if !strings.Contains(
				body.Query,
				"refundCreate(input: $input) @idempotent(key: $idempotencyKey)",
			) {
				t.Errorf("refundCreate is missing variable-backed @idempotent: %s", body.Query)
			}
			if body.Variables["idempotencyKey"] != "refund-uuid-1" {
				t.Errorf("unexpected idempotency key: %#v", body.Variables["idempotencyKey"])
			}
			input, ok := body.Variables["input"].(map[string]any)
			if !ok {
				t.Fatalf("input is not an object: %#v", body.Variables["input"])
			}
			if _, exists := input["refundLineItems"]; exists {
				t.Error("external refund mirror must not include refundLineItems")
			}
			if _, exists := input["shipping"]; exists {
				t.Error("external refund mirror must not include shipping")
			}
			if input["currency"] != "HKD" || input["notify"] != false {
				t.Errorf("unexpected refund currency/notify input: %#v", input)
			}
			transactions, ok := input["transactions"].([]any)
			if !ok || len(transactions) != 1 {
				t.Fatalf("unexpected transactions input: %#v", input["transactions"])
			}
			transaction, ok := transactions[0].(map[string]any)
			if !ok {
				t.Fatalf("transaction is not an object: %#v", transactions[0])
			}
			expected := map[string]any{
				"orderId":  "gid://shopify/Order/1002",
				"parentId": "gid://shopify/OrderTransaction/10",
				"kind":     "REFUND",
				"gateway":  "Stripe",
				"amount":   "12.34",
			}
			for key, want := range expected {
				if got := transaction[key]; got != want {
					t.Errorf("transaction %s = %#v, want %#v", key, got, want)
				}
			}
			_, _ = w.Write([]byte(`{"data":{"refundCreate":{"refund":{"id":"gid://shopify/Refund/20","totalRefundedSet":{"presentmentMoney":{"amount":"12.34","currencyCode":"HKD"}},"transactions":{"nodes":[{"id":"gid://shopify/OrderTransaction/21","gateway":"Stripe","kind":"REFUND","status":"SUCCESS","amountSet":{"presentmentMoney":{"amount":"12.34","currencyCode":"HKD"}}}]}},"userErrors":[]}}}`))
		default:
			t.Fatalf("unexpected extra Shopify request")
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "test-token"})
	result, err := client.RecordExternalRefund(context.Background(), AdminExternalRefundInput{
		OrderID: "gid://shopify/Order/1002", StripeRefundID: "re_123",
		AmountMinor: 1234, Currency: "hkd", IdempotencyKey: "refund-uuid-1",
	})
	if err != nil {
		t.Fatalf("RecordExternalRefund: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if result.RefundID != "gid://shopify/Refund/20" ||
		result.TransactionID != "gid://shopify/OrderTransaction/21" ||
		result.AmountMinor != 1234 ||
		result.Currency != "HKD" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRecordExternalRefundRejectsShopifyAmountMismatch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"data":{"order":{"id":"gid://shopify/Order/1002","transactions":[{"id":"gid://shopify/OrderTransaction/10","gateway":"Stripe","kind":"SALE","status":"SUCCESS","manualPaymentGateway":true,"amountSet":{"presentmentMoney":{"amount":"85.00","currencyCode":"HKD"}}}]}}}`))
		case 2:
			_, _ = w.Write([]byte(`{"data":{"refundCreate":{"refund":{"id":"gid://shopify/Refund/20","totalRefundedSet":{"presentmentMoney":{"amount":"12.33","currencyCode":"HKD"}},"transactions":{"nodes":[{"id":"gid://shopify/OrderTransaction/21","gateway":"Stripe","kind":"REFUND","status":"SUCCESS","amountSet":{"presentmentMoney":{"amount":"12.33","currencyCode":"HKD"}}}]}},"userErrors":[]}}}`))
		default:
			t.Fatalf("unexpected extra Shopify request")
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "test-token"})
	_, err := client.RecordExternalRefund(context.Background(), AdminExternalRefundInput{
		OrderID: "gid://shopify/Order/1002", StripeRefundID: "re_123",
		AmountMinor: 1234, Currency: "HKD", IdempotencyKey: "refund-uuid-1",
	})
	if err == nil || !strings.Contains(err.Error(), "refund total mismatch") {
		t.Fatalf("expected exact amount mismatch, got %v", err)
	}
}
