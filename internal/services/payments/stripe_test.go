package payments

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v83"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
)

type stripeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f stripeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCreatePaymentIntentUsesStableIdempotencyKeyAndAuthoritativeAmount(t *testing.T) {
	originalBackend := stripe.GetBackend(stripe.APIBackend)
	originalKey := stripe.Key
	t.Cleanup(func() {
		stripe.SetBackend(stripe.APIBackend, originalBackend)
		stripe.Key = originalKey
	})

	var requestSeen bool
	httpClient := &http.Client{Transport: stripeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestSeen = true
		if req.Method != http.MethodPost || req.URL.Path != "/v1/payment_intents" {
			t.Fatalf("unexpected Stripe request: %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Idempotency-Key"); got != "pawrd-shop-quote:quote-123" {
			t.Fatalf("unexpected Stripe idempotency key %q", got)
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("amount") != "2502" || form.Get("currency") != "hkd" {
			t.Fatalf("unexpected PaymentIntent amount: %s", raw)
		}
		if got := form.Get("payment_method_types[0]"); got != "card" {
			t.Fatalf("PaymentIntent is not restricted to card: %s", raw)
		}
		if form.Get("automatic_payment_methods[enabled]") != "" {
			t.Fatalf("automatic delayed payment methods must not be enabled: %s", raw)
		}
		if form.Get("metadata[pawrd_quote_id]") != "quote-123" {
			t.Fatalf("missing quote metadata: %s", raw)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"pi_test","object":"payment_intent","client_secret":"pi_test_secret"}`,
			)),
		}, nil
	})}
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL:        stripe.String("https://stripe.test"),
		HTTPClient: httpClient,
	})
	stripe.SetBackend(stripe.APIBackend, backend)
	stripe.Key = "sk_test"

	service := &StripeService{publishableKey: "pk_test"}
	result, err := service.CreatePaymentIntent(CreatePaymentIntentRequest{
		Amount: 2502, Currency: "HKD", Description: "Pawrd order",
		IdempotencyKey: "pawrd-shop-quote:quote-123",
		Metadata:       map[string]string{"pawrd_quote_id": "quote-123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !requestSeen || result.PaymentIntentID != "pi_test" ||
		result.ClientSecret != "pi_test_secret" || result.PublishableKey != "pk_test" {
		t.Fatalf("unexpected PaymentIntent result: seen=%v result=%+v", requestSeen, result)
	}
}

func TestCreatePaymentIntentRequiresIdempotencyKey(t *testing.T) {
	service := &StripeService{}
	if _, err := service.CreatePaymentIntent(CreatePaymentIntentRequest{
		Amount: 100, Currency: "hkd",
	}); err == nil {
		t.Fatal("expected a missing payment idempotency key to fail before Stripe")
	}
}

func TestCreateRefundSendsExplicitAmountPaymentIntentAndIdempotencyKey(t *testing.T) {
	originalBackend := stripe.GetBackend(stripe.APIBackend)
	originalKey := stripe.Key
	t.Cleanup(func() {
		stripe.SetBackend(stripe.APIBackend, originalBackend)
		stripe.Key = originalKey
	})

	var requestSeen bool
	httpClient := &http.Client{Transport: stripeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestSeen = true
		if req.Method != http.MethodPost || req.URL.Path != "/v1/refunds" {
			t.Fatalf("unexpected Stripe request: %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Idempotency-Key"); got != "refund-idempotency-key" {
			t.Fatalf("unexpected Stripe idempotency key %q", got)
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if form.Get("payment_intent") != "pi_test" || form.Get("amount") != "450" {
			t.Fatalf("unexpected Stripe refund form: %s", raw)
		}
		if form.Get("reason") != "requested_by_customer" ||
			form.Get("metadata[pawrd_refund_id]") != "refund-local" ||
			form.Get("metadata[pawrd_order_id]") != "order-local" ||
			form.Get("metadata[pawrd_reason]") != models.ShopRefundReasonQuoteExpired {
			t.Fatalf("missing Stripe refund audit metadata: %s", raw)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"re_test","object":"refund","status":"pending"}`,
			)),
		}, nil
	})}
	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL:        stripe.String("https://stripe.test"),
		HTTPClient: httpClient,
	})
	stripe.SetBackend(stripe.APIBackend, backend)
	stripe.Key = "sk_test"

	service := &StripeService{}
	result, err := service.CreateRefund(context.Background(), CreateRefundRequest{
		PaymentIntentID: "pi_test", AmountMinor: 450,
		Reason:         models.ShopRefundReasonQuoteExpired,
		IdempotencyKey: "refund-idempotency-key", PawrdRefundID: "refund-local",
		PawrdOrderID: "order-local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !requestSeen || result.RefundID != "re_test" || result.Status != "pending" {
		t.Fatalf("unexpected refund result: seen=%v result=%+v", requestSeen, result)
	}
}

func TestCreateRefundRejectsInvalidInputBeforeStripe(t *testing.T) {
	service := &StripeService{}
	testCases := []CreateRefundRequest{
		{AmountMinor: 100, Reason: "requested_by_customer", IdempotencyKey: "key"},
		{PaymentIntentID: "pi_1", AmountMinor: 0, Reason: "requested_by_customer", IdempotencyKey: "key"},
		{PaymentIntentID: "pi_1", AmountMinor: 100, Reason: "other", IdempotencyKey: "key"},
		{PaymentIntentID: "pi_1", AmountMinor: 100, Reason: "requested_by_customer"},
	}
	for _, req := range testCases {
		if _, err := service.CreateRefund(context.Background(), req); err == nil {
			t.Fatalf("expected invalid refund request to fail: %+v", req)
		}
	}
}
