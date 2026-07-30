package handlers

// Phase 4 safety guarantees re-ported onto the mainline quote-based checkout
// stack: durable-order-before-Stripe, terminal failure states, metadata
// whitelist, server-authoritative customer, placeholder-phone quarantine and
// consumed-quote recovery.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

var errPhase4StripeDown = errors.New("fake stripe: service unavailable")

type phase4FakePayments struct {
	requests []payments.CreatePaymentIntentRequest
	onCreate func(payments.CreatePaymentIntentRequest)
	failWith error
}

func (f *phase4FakePayments) CreatePaymentIntent(
	req payments.CreatePaymentIntentRequest,
) (*payments.CreatePaymentIntentResponse, error) {
	f.requests = append(f.requests, req)
	if f.onCreate != nil {
		f.onCreate(req)
	}
	if f.failWith != nil {
		return nil, f.failWith
	}
	return &payments.CreatePaymentIntentResponse{
		ClientSecret:    "pi_p4_secret",
		PaymentIntentID: "pi_phase4",
		PublishableKey:  "pk_test",
	}, nil
}

func (f *phase4FakePayments) CancelPaymentIntent(string) error { return nil }

func phase4PaymentSheetHandler(
	cfg *config.Config,
	db *gorm.DB,
	fake *phase4FakePayments,
) http.HandlerFunc {
	return newShopPaymentSheetHandler(cfg, db, func(*config.Config) (checkoutPaymentService, error) {
		return fake, nil
	}, time.Now, currentShopAccountEmail)
}

// Order (with the immutable shipping snapshot) exists BEFORE the Stripe call.
func TestPhase4OrderExistsBeforeStripeCall(t *testing.T) {
	db := newShopFlowTestDB(t, true)
	token, userID, cfg := shopFlowAuth(t, db, "alice@example.com")
	record := persistReadyHandlerQuote(t, db, "quote-p4-first", userID, time.Now().Add(10*time.Minute))
	fake := &phase4FakePayments{}

	var found *models.ShopOrder
	fake.onCreate = func(req payments.CreatePaymentIntentRequest) {
		var order models.ShopOrder
		if err := db.Preload("Items").First(&order, "id = ?", req.Metadata["pawrd_order_id"]).Error; err == nil {
			found = &order
		}
	}

	rec := performShopFlowRequest(t, phase4PaymentSheetHandler(cfg, db, fake), token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if found == nil {
		t.Fatalf("order must exist before the Stripe call")
	}
	if found.Status != "pending_payment" || found.PaymentIntentID != nil {
		t.Fatalf("expected pending_payment with NULL intent id at Stripe time: %+v", found)
	}
	if found.ShippingAddress1 != "1 Test Street" || found.ShippingDistrict != "Wan Chai" ||
		found.ShippingRegion != "Hong Kong Island" || len(found.Items) != 1 {
		t.Fatalf("shipping snapshot must be complete before the Stripe call: %+v", found)
	}
}

// Stripe creation failure → durable payment_failed (+financial failed) order;
// retrying the same consumed quote resumes and recovers — no stuck state.
func TestPhase4StripeFailureThenResumeRecovery(t *testing.T) {
	db := newShopFlowTestDB(t, true)
	token, userID, cfg := shopFlowAuth(t, db, "alice@example.com")
	record := persistReadyHandlerQuote(t, db, "quote-p4-resume", userID, time.Now().Add(10*time.Minute))
	fake := &phase4FakePayments{failWith: errPhase4StripeDown}
	handler := phase4PaymentSheetHandler(cfg, db, fake)

	rec := performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}

	var order models.ShopOrder
	if err := db.First(&order, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("durable order must exist after Stripe failure: %v", err)
	}
	if order.Status != "payment_failed" || order.FinancialStatus != "failed" {
		t.Fatalf("expected payment_failed/failed, got %q/%q", order.Status, order.FinancialStatus)
	}
	if order.PaymentIntentID != nil {
		t.Fatalf("intent id must stay NULL after creation failure")
	}
	if !strings.Contains(order.FailureReason, "stripe payment intent creation failed") {
		t.Fatalf("expected failure reason, got %q", order.FailureReason)
	}
	var quote models.ShopCheckoutQuote
	if err := db.First(&quote, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if quote.Status != models.ShopQuoteStatusConsumed || quote.OrderID != order.ID {
		t.Fatalf("quote must be consumed and bound to the order: %+v", quote)
	}

	// Recovery: retry after Stripe is healthy again.
	fake.failWith = nil
	rec = performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "pending_payment" || order.PaymentIntentIDValue() != "pi_phase4" || order.FinancialStatus != "pending" {
		t.Fatalf("resume must reopen the order with the intent attached: %+v", order)
	}
	if err := db.First(&quote, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if quote.PaymentIntentID != "pi_phase4" {
		t.Fatalf("quote intent id must be back-filled on resume, got %q", quote.PaymentIntentID)
	}
	var orderCount int64
	if err := db.Model(&models.ShopOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 {
		t.Fatalf("resume must not create a second order, got %d", orderCount)
	}
}

// Back-fill failure → 200 with the sheet, order reconcilable via metadata, and
// the NEXT retry resumes the back-fill (self-healing).
func TestPhase4BackfillFailureRespondsAndSelfHeals(t *testing.T) {
	db := newShopFlowTestDB(t, true)
	token, userID, cfg := shopFlowAuth(t, db, "alice@example.com")
	record := persistReadyHandlerQuote(t, db, "quote-p4-backfill", userID, time.Now().Add(10*time.Minute))
	fake := &phase4FakePayments{}
	handler := phase4PaymentSheetHandler(cfg, db, fake)

	if err := db.Exec(`CREATE TRIGGER fail_backfill_p4 BEFORE UPDATE OF payment_intent_id ON shop_orders
		BEGIN SELECT RAISE(ABORT, 'injected backfill failure'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rec := performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (reconcilable), got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ShopPaymentSheetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var order models.ShopOrder
	if err := db.First(&order, "id = ?", resp.OrderID).Error; err != nil {
		t.Fatalf("order must exist: %v", err)
	}
	if order.PaymentIntentID != nil {
		t.Fatalf("back-fill must have failed (injected), got %v", *order.PaymentIntentID)
	}
	if fake.requests[0].Metadata["pawrd_order_id"] != resp.OrderID {
		t.Fatalf("intent must carry pawrd_order_id for reconciliation")
	}

	// Self-heal: once the database cooperates, a plain retry completes the back-fill.
	if err := db.Exec("DROP TRIGGER fail_backfill_p4").Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	rec = performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("self-heal retry: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if err := db.First(&order, "id = ?", resp.OrderID).Error; err != nil {
		t.Fatal(err)
	}
	if order.PaymentIntentIDValue() != "pi_phase4" {
		t.Fatalf("self-heal must back-fill the intent id, got %q", order.PaymentIntentIDValue())
	}
}

// Metadata whitelist: exactly the no-PII reconciliation fields.
func TestPhase4PaymentIntentMetadataWhitelist(t *testing.T) {
	db := newShopFlowTestDB(t, true)
	token, userID, cfg := shopFlowAuth(t, db, "alice@example.com")
	record := persistReadyHandlerQuote(t, db, "quote-p4-meta", userID, time.Now().Add(10*time.Minute))
	fake := &phase4FakePayments{}

	rec := performShopFlowRequest(t, phase4PaymentSheetHandler(cfg, db, fake), token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	metadata := fake.requests[0].Metadata
	for key := range metadata {
		allowed := strings.HasPrefix(key, "item_") ||
			key == "total_items" ||
			key == "pawrd_order_id" ||
			key == "pawrd_quote_id" ||
			key == "pawrd_quote_version" ||
			key == "pawrd_quote_expires_at"
		if !allowed {
			t.Fatalf("unexpected metadata key %q", key)
		}
	}
	joined := strings.ToLower(strings.Join(func() []string {
		values := make([]string, 0, len(metadata))
		for _, v := range metadata {
			values = append(values, v)
		}
		return values
	}(), " | "))
	for _, pii := range []string{"alice@example.com", "test user", "61234567", "1 test street", "user_id", "customer_"} {
		if strings.Contains(joined, pii) {
			t.Fatalf("PII %q found in metadata: %v", pii, metadata)
		}
	}
	if metadata["pawrd_quote_id"] != record.ID || metadata["pawrd_order_id"] == "" {
		t.Fatalf("expected quote/order linkage: %v", metadata)
	}
}

// The quote handler derives the customer from the AuthUser account — a forged
// client customer object is ignored, and a placeholder phone never leaves the
// server (Shopify request, sealed snapshot).
func TestPhase4QuoteCustomerServerAuthoritativeAndPhoneQuarantined(t *testing.T) {
	db := newShopFlowTestDB(t, true)
	token, _, cfg := shopFlowAuth(t, db, "p4-customer@example.com")
	initial, _ := handlerTestStorefrontQuotes()
	client := &fakeStorefrontQuoteClient{initial: initial}
	handler := newShopQuoteHandler(
		cfg,
		db,
		func(*config.Config) (shopify.StorefrontQuoteClient, error) { return client, nil },
		time.Now,
		currentShopAccountEmail,
	)

	rec := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
		LineItems: []ShopCheckoutLineItemRequest{{
			Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		// Forged customer — must be ignored entirely.
		Customer: ShopCheckoutCustomerRequest{
			Name: "Forged Name", Email: "bogus@evil.example", Phone: "+85200000000",
		},
		Shipping: ShopCheckoutShippingRequest{
			RecipientName: "Alice Test", Phone: "61234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if client.createRequest.Email != "p4-customer@example.com" {
		t.Fatalf("storefront email must come from the account, got %q", client.createRequest.Email)
	}
	if client.createRequest.Phone != "" {
		t.Fatalf("placeholder phone must be quarantined, got %q", client.createRequest.Phone)
	}

	var record models.ShopCheckoutQuote
	if err := db.First(&record, "user_id <> ?", "").Error; err != nil {
		t.Fatalf("load quote: %v", err)
	}
	snapshot, err := record.DecodeAndVerifySnapshot()
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Customer.Email != "p4-customer@example.com" || snapshot.Customer.Name != "Test User" {
		t.Fatalf("snapshot customer must come from the account: %+v", snapshot.Customer)
	}
	if snapshot.Customer.Phone != "" {
		t.Fatalf("placeholder phone leaked into snapshot: %+v", snapshot.Customer)
	}
	if strings.Contains(record.SnapshotJSON, "phone-not-set-") || strings.Contains(record.SnapshotJSON, "bogus@evil.example") {
		t.Fatalf("quarantine breach in sealed snapshot: %s", record.SnapshotJSON)
	}
	// The shipping phone is the per-order override, preserved in canonical form.
	if snapshot.Shipping.Phone != "+85261234567" {
		t.Fatalf("shipping phone must be preserved in canonical form, got %q", snapshot.Shipping.Phone)
	}
}

// Phone normalization matrix: raw variants all normalize to the canonical
// +852XXXXXXXX in the Shopify request, the sealed quote snapshot AND the
// persisted ShopOrder.
func TestPhase4ShippingPhoneNormalizedAcrossPipeline(t *testing.T) {
	inputs := []string{"61234567", "6123 4567", "+852 6123 4567"}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			db := newShopFlowTestDB(t, true)
			token, _, cfg := shopFlowAuth(t, db, "alice@example.com")
			initial, selected := handlerTestStorefrontQuotes()
			client := &fakeStorefrontQuoteClient{initial: initial, selected: selected}
			quoteHandler := newShopQuoteHandler(
				cfg,
				db,
				func(*config.Config) (shopify.StorefrontQuoteClient, error) { return client, nil },
				time.Now,
				currentShopAccountEmail,
			)

			createRec := performShopFlowRequest(t, quoteHandler, token, ShopQuoteRequest{
				LineItems: []ShopCheckoutLineItemRequest{{
					Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
				}},
				Customer: ShopCheckoutCustomerRequest{Name: "Alice", Email: "alice@example.com"},
				Shipping: ShopCheckoutShippingRequest{
					RecipientName: "Alice Test", Phone: input,
					Address1: "1 Test Street", District: "Wan Chai",
					Region: "Hong Kong Island",
				},
			})
			if createRec.Code != http.StatusOK {
				t.Fatalf("create quote status=%d body=%s", createRec.Code, createRec.Body.String())
			}
			var created ShopQuoteResponse
			if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if client.createRequest.Shipping.Phone != "+85261234567" {
				t.Fatalf("Shopify request phone not normalized, got %q", client.createRequest.Shipping.Phone)
			}

			selectRec := performShopFlowRequest(t, quoteHandler, token, ShopQuoteRequest{
				QuoteID: created.QuoteID, Version: created.Version,
				SelectedDeliveryOptionHandle: "standard-hk",
			})
			if selectRec.Code != http.StatusOK {
				t.Fatalf("select delivery status=%d body=%s", selectRec.Code, selectRec.Body.String())
			}

			var quote models.ShopCheckoutQuote
			if err := db.First(&quote, "id = ?", created.QuoteID).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := quote.DecodeAndVerifySnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Shipping.Phone != "+85261234567" {
				t.Fatalf("sealed snapshot phone not normalized, got %q", snapshot.Shipping.Phone)
			}

			paymentService := &fakeCheckoutPayments{}
			sheetHandler := newShopPaymentSheetHandler(
				cfg,
				db,
				func(*config.Config) (checkoutPaymentService, error) { return paymentService, nil },
				time.Now,
				currentShopAccountEmail,
			)
			sheetRec := performShopFlowRequest(t, sheetHandler, token, ShopPaymentSheetRequest{QuoteID: created.QuoteID})
			if sheetRec.Code != http.StatusOK {
				t.Fatalf("payment sheet status=%d body=%s", sheetRec.Code, sheetRec.Body.String())
			}
			var sheetResp ShopPaymentSheetResponse
			if err := json.Unmarshal(sheetRec.Body.Bytes(), &sheetResp); err != nil {
				t.Fatal(err)
			}
			var order models.ShopOrder
			if err := db.First(&order, "id = ?", sheetResp.OrderID).Error; err != nil {
				t.Fatal(err)
			}
			if order.CustomerPhone != "+85261234567" {
				t.Fatalf("order phone not normalized, got %q", order.CustomerPhone)
			}
		})
	}
}
