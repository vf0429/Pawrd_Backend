package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v83"
	"github.com/stripe/stripe-go/v83/webhook"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type recordingFulfiller struct {
	request payments.FulfillmentRequest
	called  bool
}

func newPaymentsWebhookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ShopOrder{}, &models.ShopRefund{}, &models.ShopIntegrationEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func serveSignedStripeWebhook(
	t *testing.T,
	db *gorm.DB,
	fulfiller payments.Fulfiller,
	secret string,
	payload []byte,
	refundMirrors ...payments.RefundMirrorEnqueuer,
) *httptest.ResponseRecorder {
	t.Helper()
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  secret,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/payments/webhook", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	rec := httptest.NewRecorder()
	NewPaymentsWebhookHandler(
		&config.Config{StripeWebhookSecret: secret},
		db,
		fulfiller,
		refundMirrors...,
	).ServeHTTP(rec, req)
	return rec
}

func (f *recordingFulfiller) Fulfill(request payments.FulfillmentRequest) error {
	f.request = request
	f.called = true
	return nil
}

func newSucceededWebhookFixture(t *testing.T) (*gorm.DB, []byte) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.ShopOrder{},
		&models.ShopCheckoutQuote{},
		&models.ShopRefund{},
		&models.ShopCompensationRefundJob{},
		&models.ShopFulfillmentJob{},
		&models.ShopIntegrationEvent{},
	); err != nil {
		t.Fatal(err)
	}

	const (
		orderID = "11111111-1111-1111-1111-111111111111"
		quoteID = "22222222-2222-2222-2222-222222222222"
		userID  = "user-clover"
		piID    = "pi_clover_test"
	)
	order := models.ShopOrder{
		ID: orderID, UserID: userID, PaymentIntentID: piID,
		Status: "pending_payment", FinancialStatus: "pending",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	quotedAt := time.Unix(1_700_000_000, 0).UTC()
	expiresAt := quotedAt.Add(10 * time.Minute)
	quote := models.ShopCheckoutQuote{ID: quoteID}
	if err := quote.SetSnapshot(models.ShopQuoteSnapshot{
		Version:              models.ShopQuoteSnapshotVersion,
		ShopifyCartID:        "gid://shopify/Cart/clover",
		ShopifyCartUpdatedAt: quotedAt,
		UserID:               userID,
		Status:               models.ShopQuoteStatusReady,
		Currency:             "HKD",
		LineItems: []models.ShopQuoteSnapshotItem{{
			Source: "shopify", Handle: "test-product",
			VariantID: "gid://shopify/ProductVariant/1", Title: "Test product",
			Quantity: 1, UnitAmountMinor: 1000, RequiresShipping: true,
		}},
		Amounts: models.ShopQuoteAmounts{
			SubtotalAmountMinor: 1000,
			TotalAmountMinor:    1000,
		},
		Customer: models.ShopQuoteCustomer{
			Name: "Test Buyer", Email: "buyer@example.com",
		},
		Shipping: models.ShopQuoteShipping{
			RecipientName: "Test Buyer", Phone: "91234567",
			Address1: "1 Test Road", District: "Central and Western",
			Region: "Hong Kong Island", CountryCode: "HK",
		},
		QuotedAt:  quotedAt,
		ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	consumedAt := quotedAt.Add(time.Minute)
	quote.Status = models.ShopQuoteStatusConsumed
	quote.ConsumedAt = &consumedAt
	quote.OrderID = orderID
	quote.PaymentIntentID = piID
	if err := db.Create(&quote).Error; err != nil {
		t.Fatal(err)
	}

	payload := []byte(fmt.Sprintf(`{
		"id": "evt_clover_test",
		"object": "event",
		"created": %d,
		"api_version": "2026-02-25.clover",
		"type": "payment_intent.succeeded",
		"data": {
			"object": {
				"id": %q,
				"object": "payment_intent",
				"status": "succeeded",
				"amount": 1000,
				"amount_received": 1000,
				"currency": "hkd",
				"receipt_email": "buyer@example.com",
				"metadata": {
					"pawrd_order_id": %q,
					"pawrd_quote_id": %q,
					"pawrd_quote_version": %q,
					"pawrd_quote_expires_at": %q,
					"user_id": %q,
					"customer_name": "Test Buyer",
					"item_1": "source=shopify | handle=test-product | variant=gid://shopify/ProductVariant/1 | qty:1"
				}
			}
		}
	}`, quotedAt.Add(2*time.Minute).Unix(), piID, orderID, quoteID, quote.SnapshotSHA256, expiresAt.Format(time.RFC3339Nano), userID))
	return db, payload
}

func TestPaymentsWebhookAcceptsNewerCloverEvent(t *testing.T) {
	const webhookSecret = "whsec_test"
	db, payload := newSucceededWebhookFixture(t)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  webhookSecret,
	})

	fulfiller := &recordingFulfiller{}
	req := httptest.NewRequest(http.MethodPost, "/api/payments/webhook", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	rec := httptest.NewRecorder()

	NewPaymentsWebhookHandler(
		&config.Config{StripeWebhookSecret: webhookSecret},
		db,
		fulfiller,
	).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !fulfiller.called || fulfiller.request.PaymentIntentID != "pi_clover_test" {
		t.Fatalf("Clover event was not fulfilled: %+v", fulfiller.request)
	}
	if len(fulfiller.request.Items) != 1 || fulfiller.request.Items[0].Source != payments.SourceShopify {
		t.Fatalf("unexpected fulfillment items: %+v", fulfiller.request.Items)
	}

	var integrationEvent models.ShopIntegrationEvent
	if err := db.First(&integrationEvent, "external_event_id = ?", "evt_clover_test").Error; err != nil {
		t.Fatal(err)
	}
	if integrationEvent.Status != "completed" {
		t.Fatalf("expected completed event, got %q", integrationEvent.Status)
	}
}

func TestPaymentsWebhookRejectsSucceededIntentThatDoesNotMatchSealedOrder(t *testing.T) {
	const webhookSecret = "whsec_mismatch"
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "amount",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(payload, []byte(`"amount": 1000`), []byte(`"amount": 1`), 1)
			},
		},
		{
			name: "amount received",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(payload, []byte(`"amount_received": 1000`), []byte(`"amount_received": 1`), 1)
			},
		},
		{
			name: "currency",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(payload, []byte(`"currency": "hkd"`), []byte(`"currency": "usd"`), 1)
			},
		},
		{
			name: "order metadata",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(
					payload,
					[]byte(`"pawrd_order_id": "11111111-1111-1111-1111-111111111111"`),
					[]byte(`"pawrd_order_id": "33333333-3333-3333-3333-333333333333"`),
					1,
				)
			},
		},
		{
			name: "quote metadata",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(
					payload,
					[]byte(`"pawrd_quote_id": "22222222-2222-2222-2222-222222222222"`),
					[]byte(`"pawrd_quote_id": "33333333-3333-3333-3333-333333333333"`),
					1,
				)
			},
		},
		{
			name: "quote version metadata",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(
					payload,
					[]byte(`"pawrd_quote_version": "`),
					[]byte(`"pawrd_quote_version": "tampered-`),
					1,
				)
			},
		},
		{
			name: "user metadata",
			mutate: func(payload []byte) []byte {
				return bytes.Replace(payload, []byte(`"user_id": "user-clover"`), []byte(`"user_id": "other-user"`), 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, payload := newSucceededWebhookFixture(t)
			fulfiller := &recordingFulfiller{}
			rec := serveSignedStripeWebhook(
				t,
				db,
				fulfiller,
				webhookSecret,
				test.mutate(payload),
			)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("expected mismatch 500, got %d: %s", rec.Code, rec.Body.String())
			}
			if fulfiller.called {
				t.Fatal("mismatched PaymentIntent reached fulfillment")
			}
			var integrationEvent models.ShopIntegrationEvent
			if err := db.First(&integrationEvent, "external_event_id = ?", "evt_clover_test").Error; err != nil {
				t.Fatal(err)
			}
			if integrationEvent.Status != "failed" {
				t.Fatalf("mismatched event status=%q, want failed", integrationEvent.Status)
			}
		})
	}
}

func TestPaymentsWebhookDoesNotFulfillPaymentCompletedAfterQuoteExpiry(t *testing.T) {
	const webhookSecret = "whsec_expired_quote"
	db, payload := newSucceededWebhookFixture(t)
	// The fixture quote expires at 1700000600.
	payload = bytes.Replace(payload, []byte(`"created": 1700000120`), []byte(`"created": 1700000600`), 1)
	fulfiller := &recordingFulfiller{}
	rec := serveSignedStripeWebhook(t, db, fulfiller, webhookSecret, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected expired payment to be acknowledged, got %d: %s", rec.Code, rec.Body.String())
	}
	if fulfiller.called {
		t.Fatal("expired quote payment reached fulfillment")
	}

	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_clover_test").Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "canceled" || order.FinancialStatus != "paid" ||
		order.FulfillmentStatus != "CANCELLED" ||
		!strings.Contains(order.FailureReason, "automatic refund queued") {
		t.Fatalf("expired payment compensation state not recorded: %+v", order)
	}
	var refund models.ShopRefund
	if err := db.First(&refund, "order_id = ?", order.ID).Error; err != nil {
		t.Fatalf("load automatic compensation refund: %v", err)
	}
	if refund.Status != models.ShopRefundStatusPending ||
		refund.Reason != models.ShopRefundReasonQuoteExpired ||
		refund.AmountMinor != order.TotalAmountMinor {
		t.Fatalf("unexpected automatic compensation refund: %+v", refund)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatalf("load automatic compensation job: %v", err)
	}
	if job.Status != models.ShopCompensationRefundJobPending {
		t.Fatalf("compensation job status=%q, want pending", job.Status)
	}
}

func TestPaymentsWebhookTreatsFractionalExpiryUnixSecondAsExpired(t *testing.T) {
	const webhookSecret = "whsec_fractional_expiry"
	db, payload := newSucceededWebhookFixture(t)

	var quote models.ShopCheckoutQuote
	if err := db.First(&quote, "id = ?", "22222222-2222-2222-2222-222222222222").Error; err != nil {
		t.Fatal(err)
	}
	snapshot, err := quote.DecodeAndVerifySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := quote.SnapshotSHA256
	oldExpiry := quote.ExpiresAt.UTC().Format(time.RFC3339Nano)
	fractionalExpiry := time.Unix(1_700_000_600, 500_000_000).UTC()
	snapshot.ExpiresAt = fractionalExpiry
	if err := quote.SetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	quote.Status = models.ShopQuoteStatusConsumed
	if err := db.Model(&models.ShopCheckoutQuote{}).
		Where("id = ?", quote.ID).
		Updates(shopQuoteUpdateColumns(quote)).Error; err != nil {
		t.Fatal(err)
	}

	payload = bytes.Replace(
		payload,
		[]byte(`"created": 1700000120`),
		[]byte(`"created": 1700000600`),
		1,
	)
	payload = bytes.Replace(payload, []byte(oldVersion), []byte(quote.SnapshotSHA256), 1)
	payload = bytes.Replace(
		payload,
		[]byte(oldExpiry),
		[]byte(fractionalExpiry.Format(time.RFC3339Nano)),
		1,
	)

	fulfiller := &recordingFulfiller{}
	rec := serveSignedStripeWebhook(t, db, fulfiller, webhookSecret, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("fractional-boundary event=%d: %s", rec.Code, rec.Body.String())
	}
	if fulfiller.called {
		t.Fatal("event in the fractional expiry Unix second reached fulfillment")
	}
	var refund models.ShopRefund
	if err := db.First(
		&refund,
		"order_id = ? AND reason = ?",
		"11111111-1111-1111-1111-111111111111",
		models.ShopRefundReasonQuoteExpired,
	).Error; err != nil {
		t.Fatalf("load fractional-boundary compensation: %v", err)
	}
	if refund.Status != models.ShopRefundStatusPending || refund.AmountMinor != 1000 {
		t.Fatalf("unexpected fractional-boundary compensation: %+v", refund)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatalf("load fractional-boundary compensation job: %v", err)
	}
	if job.Status != models.ShopCompensationRefundJobPending {
		t.Fatalf("fractional-boundary job status=%q, want pending", job.Status)
	}
}

func TestPaymentsWebhookExpiredSucceededDoesNotRegressFullyRefundedOrder(t *testing.T) {
	const webhookSecret = "whsec_expired_already_refunded"
	db, payload := newSucceededWebhookFixture(t)
	payload = bytes.Replace(payload, []byte(`"created": 1700000120`), []byte(`"created": 1700000601`), 1)
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_clover_test").
		Updates(map[string]any{
			"status": "refunded", "financial_status": "refunded",
			"refunded_amount_minor": 1000,
			"fulfillment_status":    "CANCELLED",
			"failure_reason":        "refund completed",
		}).Error; err != nil {
		t.Fatal(err)
	}
	rec := serveSignedStripeWebhook(
		t, db, &recordingFulfiller{}, webhookSecret, payload,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("already-refunded expired event=%d: %s", rec.Code, rec.Body.String())
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_clover_test").Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refunded" ||
		order.FinancialStatus != "refunded" ||
		order.RefundedAmountMinor != 1000 ||
		order.FailureReason != "refund completed" {
		t.Fatalf("expired duplicate regressed refunded order: %+v", order)
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("fully refunded order created %d extra refunds", refunds)
	}
}

func TestPaymentsWebhookExpiredSucceededCompensatesOnlyPartialRefundRemainder(t *testing.T) {
	const webhookSecret = "whsec_expired_partially_refunded"
	db, payload := newSucceededWebhookFixture(t)
	payload = bytes.Replace(payload, []byte(`"created": 1700000120`), []byte(`"created": 1700000601`), 1)
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_clover_test").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&order).Updates(map[string]any{
		"status": "canceled", "financial_status": "partially_refunded",
		"refunded_amount_minor": 400, "fulfillment_status": "CANCELLED",
	}).Error; err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	existingStripeRefundID := "re_existing_partial"
	if err := db.Create(&models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID,
		PaymentIntentID: order.PaymentIntentID,
		StripeRefundID:  &existingStripeRefundID,
		IdempotencyKey:  "existing-partial",
		AmountMinor:     400, Currency: "HKD",
		Reason: "requested_by_customer",
		Status: models.ShopRefundStatusSucceeded, StripeStatus: "succeeded",
		RequestedBy: "operator", CompletedAt: &completedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	rec := serveSignedStripeWebhook(
		t, db, &recordingFulfiller{}, webhookSecret, payload,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("partially-refunded expired event=%d: %s", rec.Code, rec.Body.String())
	}
	var compensation models.ShopRefund
	if err := db.First(
		&compensation,
		"order_id = ? AND reason = ?",
		order.ID,
		models.ShopRefundReasonQuoteExpired,
	).Error; err != nil {
		t.Fatal(err)
	}
	if compensation.AmountMinor != 600 ||
		compensation.Status != models.ShopRefundStatusPending {
		t.Fatalf("partial remainder compensation is wrong: %+v", compensation)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FinancialStatus != "partially_refunded" {
		t.Fatalf("partial financial state regressed: %+v", order)
	}
}

func TestPaymentsWebhookExpiredSucceededAcknowledgesActiveOrLostDisputeWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name      string
		dispute   string
		financial string
		status    string
	}{
		{
			name: "active", dispute: "needs_response",
			financial: "disputed", status: "payment_disputed",
		},
		{
			name: "lost", dispute: "lost",
			financial: "dispute_lost", status: "payment_dispute_lost",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, payload := newSucceededWebhookFixture(t)
			payload = bytes.Replace(
				payload,
				[]byte(`"created": 1700000120`),
				[]byte(`"created": 1700000601`),
				1,
			)
			if err := db.Model(&models.ShopOrder{}).
				Where("payment_intent_id = ?", "pi_clover_test").
				Updates(map[string]any{
					"status": test.status, "financial_status": test.financial,
					"dispute_id": "dp_expired", "dispute_status": test.dispute,
					"failure_reason": "dispute owns money lifecycle",
				}).Error; err != nil {
				t.Fatal(err)
			}
			rec := serveSignedStripeWebhook(
				t,
				db,
				&recordingFulfiller{},
				"whsec_expired_dispute_"+test.name,
				payload,
			)
			if rec.Code != http.StatusOK {
				t.Fatalf("expired disputed event=%d: %s", rec.Code, rec.Body.String())
			}
			var order models.ShopOrder
			if err := db.First(&order, "payment_intent_id = ?", "pi_clover_test").Error; err != nil {
				t.Fatal(err)
			}
			if order.Status != test.status ||
				order.FinancialStatus != test.financial ||
				order.DisputeStatus != test.dispute ||
				order.FailureReason != "dispute owns money lifecycle" {
				t.Fatalf("expired event mutated dispute state: %+v", order)
			}
			var refunds int64
			if err := db.Model(&models.ShopRefund{}).Count(&refunds).Error; err != nil {
				t.Fatal(err)
			}
			if refunds != 0 {
				t.Fatalf("disputed expired payment created %d refunds", refunds)
			}
		})
	}
}

func TestPaymentsWebhookDifferentSucceededEventsReuseOneExpiredPaymentCompensation(t *testing.T) {
	const webhookSecret = "whsec_expired_quote_duplicate"
	db, payload := newSucceededWebhookFixture(t)
	payload = bytes.Replace(payload, []byte(`"created": 1700000120`), []byte(`"created": 1700000601`), 1)
	fulfiller := &recordingFulfiller{}

	first := serveSignedStripeWebhook(t, db, fulfiller, webhookSecret, payload)
	if first.Code != http.StatusOK {
		t.Fatalf("first expired event=%d: %s", first.Code, first.Body.String())
	}
	secondPayload := bytes.Replace(
		payload,
		[]byte(`"id": "evt_clover_test"`),
		[]byte(`"id": "evt_clover_test_duplicate"`),
		1,
	)
	second := serveSignedStripeWebhook(t, db, fulfiller, webhookSecret, secondPayload)
	if second.Code != http.StatusOK {
		t.Fatalf("duplicate expired event=%d: %s", second.Code, second.Body.String())
	}
	if fulfiller.called {
		t.Fatal("expired duplicate event reached fulfillment")
	}
	var refunds, jobs int64
	if err := db.Model(&models.ShopRefund{}).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ShopCompensationRefundJob{}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || jobs != 1 {
		t.Fatalf("duplicate events created refunds/jobs=%d/%d, want 1/1", refunds, jobs)
	}
}

func TestPaymentsWebhookTracksDelayedPaymentLifecycleWithoutRegressingPaidOrder(t *testing.T) {
	const secret = "whsec_lifecycle"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_delayed",
		Status: "pending_payment", FinancialStatus: "pending", Currency: "HKD",
		TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	processing := []byte(`{
		"id":"evt_processing","object":"event","api_version":"2026-02-25.clover",
		"type":"payment_intent.processing",
		"data":{"object":{"id":"pi_delayed","object":"payment_intent","status":"processing"}}
	}`)
	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, processing)
	if rec.Code != http.StatusOK {
		t.Fatalf("processing event failed: %d %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_processing" || updated.FinancialStatus != "pending" {
		t.Fatalf("processing state not applied: %+v", updated)
	}

	failed := []byte(`{
		"id":"evt_failed","object":"event","api_version":"2026-02-25.clover",
		"type":"payment_intent.payment_failed",
		"data":{"object":{"id":"pi_delayed","object":"payment_intent","status":"requires_payment_method",
			"last_payment_error":{"message":"card declined"}}}
	}`)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, failed)
	if rec.Code != http.StatusOK {
		t.Fatalf("failed event failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_failed" || updated.FailureReason != "card declined" {
		t.Fatalf("payment failure state not applied: %+v", updated)
	}

	canceled := []byte(`{
		"id":"evt_canceled","object":"event","api_version":"2026-02-25.clover",
		"type":"payment_intent.canceled",
		"data":{"object":{"id":"pi_delayed","object":"payment_intent","status":"canceled",
			"cancellation_reason":"abandoned"}}
	}`)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, canceled)
	if rec.Code != http.StatusOK {
		t.Fatalf("canceled event failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_canceled" || updated.FinancialStatus != "voided" {
		t.Fatalf("payment cancel state not applied: %+v", updated)
	}

	if err := db.Model(&updated).Updates(map[string]any{
		"status": "processing", "financial_status": "paid", "failure_reason": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	lateProcessing := bytes.Replace(processing, []byte("evt_processing"), []byte("evt_processing_late"), 1)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, lateProcessing)
	if rec.Code != http.StatusOK {
		t.Fatalf("late processing event failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "processing" || updated.FinancialStatus != "paid" {
		t.Fatalf("late processing event regressed paid order: %+v", updated)
	}
}

func TestPaymentsWebhookSucceededRecoversEarlierPaymentFailureIntoDurableFulfillment(t *testing.T) {
	const secret = "whsec_failed_then_succeeded"
	db, payload := newSucceededWebhookFixture(t)
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_clover_test").
		Updates(map[string]any{
			"status": "payment_failed", "financial_status": "pending",
			"failure_reason": "card was retried",
		}).Error; err != nil {
		t.Fatal(err)
	}
	downstream := &recordingFulfiller{}
	queue := payments.NewDurableFulfillmentQueue(db, downstream)
	rec := serveSignedStripeWebhook(t, db, queue, secret, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("succeeded recovery failed: %d %s", rec.Code, rec.Body.String())
	}
	if downstream.called {
		t.Fatal("Stripe webhook synchronously called downstream fulfillment")
	}
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("process recovered fulfillment=%d err=%v", processed, err)
	}
	if !downstream.called {
		t.Fatal("payment_failed -> succeeded did not reach durable fulfillment")
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_clover_test").Error; err != nil {
		t.Fatal(err)
	}
	if order.FinancialStatus != "paid" ||
		order.Status != "fulfillment_pending" {
		t.Fatalf("failed payment state was not recovered: %+v", order)
	}
}

func TestPaymentsWebhookTracksDisputeAndRestoresWonPayment(t *testing.T) {
	const secret = "whsec_dispute"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_dispute",
		Status: "processing", FinancialStatus: "paid", Currency: "HKD",
		TotalAmountMinor: 2500,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	created := []byte(`{
		"id":"evt_dispute_created","object":"event","api_version":"2026-02-25.clover",
		"type":"charge.dispute.created",
		"data":{"object":{"id":"dp_1","object":"dispute","amount":2500,"currency":"hkd",
			"payment_intent":"pi_dispute","reason":"unrecognized","status":"needs_response"}}
	}`)
	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, created)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispute created failed: %d %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.FinancialStatus != "disputed" || updated.Status != "payment_disputed" ||
		updated.DisputeID != "dp_1" || updated.DisputedAmountMinor != 2500 {
		t.Fatalf("dispute state not applied: %+v", updated)
	}

	lateProcessing := []byte(`{
		"id":"evt_dispute_late_processing","object":"event","api_version":"2026-02-25.clover",
		"type":"payment_intent.processing",
		"data":{"object":{"id":"pi_dispute","object":"payment_intent","status":"processing"}}
	}`)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, lateProcessing)
	if rec.Code != http.StatusOK {
		t.Fatalf("late processing after dispute failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.FinancialStatus != "disputed" ||
		updated.Status != "payment_disputed" ||
		updated.DisputeStatus != "needs_response" {
		t.Fatalf("late processing regressed disputed order: %+v", updated)
	}

	won := []byte(`{
		"id":"evt_dispute_won","object":"event","api_version":"2026-02-25.clover",
		"type":"charge.dispute.closed",
		"data":{"object":{"id":"dp_1","object":"dispute","amount":2500,"currency":"hkd",
			"payment_intent":"pi_dispute","reason":"unrecognized","status":"won"}}
	}`)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, won)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispute won failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.FinancialStatus != "paid" || updated.Status != "processing" ||
		updated.DisputeStatus != "won" {
		t.Fatalf("won dispute did not restore payment: %+v", updated)
	}
}

func TestPaymentsWebhookRestoresPartialRefundAfterWonDispute(t *testing.T) {
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_partial_dispute",
		Status: "payment_disputed", FinancialStatus: "disputed", Currency: "HKD",
		TotalAmountMinor: 1000, RefundedAmountMinor: 400,
		DisputeID: "dp_partial", DisputeStatus: "needs_response",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	dispute := stripe.Dispute{
		ID: "dp_partial", Amount: 600, Currency: stripe.CurrencyHKD,
		Status:        stripe.DisputeStatusWon,
		PaymentIntent: &stripe.PaymentIntent{ID: order.PaymentIntentID},
	}
	if err := applyStripeDisputeWebhook(db, dispute, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FinancialStatus != "partially_refunded" || order.Status != "processing" {
		t.Fatalf("won dispute lost partial refund state: %+v", order)
	}
}

func TestStripeDisputeTerminalStateDoesNotRegressOnOlderActiveEvent(t *testing.T) {
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1",
		PaymentIntentID: "pi_dispute_monotonic",
		Status:          "payment_disputed", FinancialStatus: "disputed",
		Currency: "HKD", TotalAmountMinor: 1000,
		DisputeID: "dp_monotonic", DisputeStatus: "needs_response",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	won := stripe.Dispute{
		ID: "dp_monotonic", Amount: 1000, Currency: stripe.CurrencyHKD,
		Status:        stripe.DisputeStatusWon,
		PaymentIntent: &stripe.PaymentIntent{ID: order.PaymentIntentID},
	}
	if err := applyStripeDisputeWebhook(db, won, 200); err != nil {
		t.Fatal(err)
	}
	olderActive := won
	olderActive.Status = stripe.DisputeStatusNeedsResponse
	if err := applyStripeDisputeWebhook(db, olderActive, 100); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.DisputeStatus != "won" ||
		order.FinancialStatus != "paid" ||
		order.Status != "processing" ||
		order.DisputeEventCreated != 200 {
		t.Fatalf("older dispute event regressed terminal state: %+v", order)
	}
}

func TestStripeDisputeTerminalStateRejectsConflictingTerminalSnapshots(t *testing.T) {
	for _, test := range []struct {
		name            string
		existingCreated int64
		incomingCreated int64
	}{
		{name: "legacy timestamps missing"},
		{name: "same timestamp", existingCreated: 200, incomingCreated: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newPaymentsWebhookTestDB(t)
			order := models.ShopOrder{
				ID: uuid.NewString(), UserID: "user-1",
				PaymentIntentID: "pi_dispute_terminal_" + test.name,
				Status:          "processing", FinancialStatus: "paid",
				Currency: "HKD", TotalAmountMinor: 1000,
				DisputeID: "dp_terminal", DisputeStatus: "won",
				DisputeEventCreated: test.existingCreated,
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			conflicting := stripe.Dispute{
				ID: "dp_terminal", Amount: 1000, Currency: stripe.CurrencyHKD,
				Status:        stripe.DisputeStatusLost,
				PaymentIntent: &stripe.PaymentIntent{ID: order.PaymentIntentID},
			}
			if err := applyStripeDisputeWebhook(
				db,
				conflicting,
				test.incomingCreated,
			); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if order.DisputeStatus != "won" ||
				order.FinancialStatus != "paid" ||
				order.Status != "processing" {
				t.Fatalf("conflicting terminal dispute regressed state: %+v", order)
			}
		})
	}
}

func TestPaymentsWebhookAppliesPartialAndFailedRefundByRefundAndPaymentIntentID(t *testing.T) {
	const secret = "whsec_refund"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_refund",
		Status: "return_closed", FinancialStatus: "paid", Currency: "HKD",
		TotalAmountMinor: 1000, ReturnStatus: "CLOSED",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	pending := models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID, PaymentIntentID: order.PaymentIntentID,
		IdempotencyKey: "refund-one", AmountMinor: 400, Currency: "HKD",
		Reason: "requested_by_customer", Status: models.ShopRefundStatusPending,
		RequestedBy: "operator",
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}

	succeeded := []byte(`{
		"id":"evt_refund_succeeded","object":"event","api_version":"2026-02-25.clover",
		"type":"refund.created",
		"data":{"object":{"id":"re_partial","object":"refund","amount":400,"currency":"hkd",
			"payment_intent":"pi_refund","reason":"requested_by_customer","status":"succeeded",
			"metadata":{"pawrd_refund_id":"` + pending.ID + `"}}}
	}`)
	mirror := &recordingRefundMirrorEnqueuer{}
	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, succeeded, mirror)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund succeeded event failed: %d %s", rec.Code, rec.Body.String())
	}
	var updatedOrder models.ShopOrder
	if err := db.First(&updatedOrder, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedOrder.RefundedAmountMinor != 400 ||
		updatedOrder.FinancialStatus != "partially_refunded" ||
		updatedOrder.Status != "return_closed" {
		t.Fatalf("partial refund misclassified the order: %+v", updatedOrder)
	}
	var updatedRefund models.ShopRefund
	if err := db.First(&updatedRefund, "id = ?", pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedRefund.Status != models.ShopRefundStatusSucceeded ||
		updatedRefund.StripeRefundID == nil || *updatedRefund.StripeRefundID != "re_partial" {
		t.Fatalf("refund record not reconciled: %+v", updatedRefund)
	}
	if len(mirror.refundIDs) != 1 || mirror.refundIDs[0] != pending.ID {
		t.Fatalf("succeeded refund was not queued for Shopify mirror: %#v", mirror.refundIDs)
	}

	failing := models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID, PaymentIntentID: order.PaymentIntentID,
		IdempotencyKey: "refund-two", AmountMinor: 300, Currency: "HKD",
		Reason: "requested_by_customer", Status: models.ShopRefundStatusPending,
		RequestedBy: "operator",
	}
	if err := db.Create(&failing).Error; err != nil {
		t.Fatal(err)
	}
	failed := []byte(`{
		"id":"evt_refund_failed","object":"event","api_version":"2026-02-25.clover",
		"type":"refund.failed",
		"data":{"object":{"id":"re_failed","object":"refund","amount":300,"currency":"hkd",
			"payment_intent":"pi_refund","reason":"requested_by_customer","status":"failed",
			"failure_reason":"declined","metadata":{"pawrd_refund_id":"` + failing.ID + `"}}}
	}`)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, failed)
	if rec.Code != http.StatusOK {
		t.Fatalf("refund failed event failed: %d %s", rec.Code, rec.Body.String())
	}
	updatedRefund = models.ShopRefund{}
	if err := db.First(&updatedRefund, "id = ?", failing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedRefund.Status != models.ShopRefundStatusFailed || updatedRefund.FailureReason != "declined" {
		t.Fatalf("failed refund not persisted: %+v", updatedRefund)
	}
	if err := db.First(&updatedOrder, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedOrder.RefundedAmountMinor != 400 || updatedOrder.FinancialStatus != "partially_refunded" {
		t.Fatalf("failed refund changed money state: %+v", updatedOrder)
	}
}

func TestChargeRefundedUsesCumulativeAmountAndDoesNotMarkPartialAsFull(t *testing.T) {
	const secret = "whsec_charge_refund"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_charge_partial",
		Status: "return_closed", FinancialStatus: "paid", Currency: "HKD",
		TotalAmountMinor: 1000, ReturnStatus: "CLOSED",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	partial := []byte(`{
		"id":"evt_charge_partial","object":"event","api_version":"2026-02-25.clover",
		"type":"charge.refunded",
		"data":{"object":{"id":"ch_partial","object":"charge","amount":1000,
			"amount_refunded":400,"currency":"hkd","payment_intent":"pi_charge_partial",
			"refunded":false}}
	}`)
	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, partial)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial charge refund failed: %d %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.RefundedAmountMinor != 400 ||
		updated.FinancialStatus != "partially_refunded" ||
		updated.Status != "return_closed" {
		t.Fatalf("charge partial refund was marked as full: %+v", updated)
	}

	full := bytes.Replace(partial, []byte("evt_charge_partial"), []byte("evt_charge_full"), 1)
	full = bytes.Replace(full, []byte(`"amount_refunded":400`), []byte(`"amount_refunded":1000`), 1)
	full = bytes.Replace(full, []byte(`"refunded":false`), []byte(`"refunded":true`), 1)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, full)
	if rec.Code != http.StatusOK {
		t.Fatalf("full charge refund failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.RefundedAmountMinor != 1000 ||
		updated.FinancialStatus != "refunded" ||
		updated.Status != "refunded" {
		t.Fatalf("charge full refund not applied: %+v", updated)
	}

	olderPartial := bytes.Replace(
		partial,
		[]byte("evt_charge_partial"),
		[]byte("evt_charge_partial_late"),
		1,
	)
	rec = serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, olderPartial)
	if rec.Code != http.StatusOK {
		t.Fatalf("late partial charge refund failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.RefundedAmountMinor != 1000 ||
		updated.FinancialStatus != "refunded" ||
		updated.Status != "refunded" {
		t.Fatalf("older partial charge refund regressed full refund: %+v", updated)
	}
}

func TestStripeRefundTerminalStateDoesNotRegressOnOlderEvents(t *testing.T) {
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1",
		PaymentIntentID: "pi_refund_monotonic",
		Status:          "return_closed", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000, ReturnStatus: "CLOSED",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	refund := models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID,
		PaymentIntentID: order.PaymentIntentID,
		IdempotencyKey:  "refund-monotonic", AmountMinor: 400,
		Currency: "HKD", Reason: "requested_by_customer",
		Status: models.ShopRefundStatusPending, RequestedBy: "operator",
	}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatal(err)
	}
	stripeRefund := stripe.Refund{
		ID: "re_monotonic", Amount: 400, Currency: stripe.CurrencyHKD,
		PaymentIntent: &stripe.PaymentIntent{ID: order.PaymentIntentID},
		Status:        stripe.RefundStatusFailed,
		Metadata:      map[string]string{"pawrd_refund_id": refund.ID},
	}
	if err := applyStripeRefundWebhook(
		db, nil, stripe.EventTypeRefundFailed, stripeRefund, "", 200,
	); err != nil {
		t.Fatal(err)
	}
	stripeRefund.Status = stripe.RefundStatusPending
	if err := applyStripeRefundWebhook(
		db, nil, stripe.EventTypeRefundUpdated, stripeRefund, "", 100,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusFailed ||
		refund.StripeEventCreated != 200 {
		t.Fatalf("older pending event regressed failed refund: %+v", refund)
	}

	stripeRefund.Status = stripe.RefundStatusSucceeded
	if err := applyStripeRefundWebhook(
		db, nil, stripe.EventTypeRefundUpdated, stripeRefund, "", 300,
	); err != nil {
		t.Fatal(err)
	}
	stripeRefund.Status = stripe.RefundStatusFailed
	if err := applyStripeRefundWebhook(
		db, nil, stripe.EventTypeRefundFailed, stripeRefund, "", 250,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusSucceeded ||
		refund.StripeEventCreated != 300 {
		t.Fatalf("older failed event regressed succeeded refund: %+v", refund)
	}
}
