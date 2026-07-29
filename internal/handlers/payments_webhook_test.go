package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v83/webhook"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
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

func (f *recordingFulfiller) Fulfill(request payments.FulfillmentRequest) error {
	f.request = request
	f.called = true
	return nil
}

func TestPaymentsWebhookAcceptsNewerCloverEvent(t *testing.T) {
	const webhookSecret = "whsec_test"
	payload := []byte(`{
		"id": "evt_clover_test",
		"object": "event",
		"api_version": "2026-02-25.clover",
		"type": "payment_intent.succeeded",
		"data": {
			"object": {
				"id": "pi_clover_test",
				"object": "payment_intent",
				"receipt_email": "buyer@example.com",
				"metadata": {
					"customer_name": "Test Buyer",
					"item_1": "source=shopify | handle=test-product | variant=gid://shopify/ProductVariant/1 | qty:1"
				}
			}
		}
	}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  webhookSecret,
	})

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ShopIntegrationEvent{}, &models.ShopOrder{}); err != nil {
		t.Fatal(err)
	}

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

// ── PaymentIntent lifecycle events (failed / canceled / succeeded snapshot) ──

const webhookTestSecret = "whsec_test"

func setupWebhookLifecycleDB(t *testing.T, migrateOrders bool) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	modelsToMigrate := []any{&models.ShopIntegrationEvent{}}
	if migrateOrders {
		modelsToMigrate = append(modelsToMigrate, &models.ShopOrder{}, &models.ShopOrderItem{})
	}
	if err := db.AutoMigrate(modelsToMigrate...); err != nil {
		t.Fatal(err)
	}
	return db
}

func webhookEventPayload(eventID, eventType, intentID string, extraPIFields string) []byte {
	return []byte(`{
		"id": "` + eventID + `",
		"object": "event",
		"api_version": "2026-02-25.clover",
		"type": "` + eventType + `",
		"data": {
			"object": {
				"id": "` + intentID + `",
				"object": "payment_intent",
				"metadata": {"item_1": "source=shopify | handle=test-product | variant=gid://shopify/ProductVariant/1 | qty:1"}` + extraPIFields + `
			}
		}
	}`)
}

func serveWebhookEvent(t *testing.T, db *gorm.DB, fulfiller payments.Fulfiller, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  webhookTestSecret,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/payments/webhook", bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", signed.Header)
	rec := httptest.NewRecorder()
	NewPaymentsWebhookHandler(&config.Config{StripeWebhookSecret: webhookTestSecret}, db, fulfiller).ServeHTTP(rec, req)
	return rec
}

func seedWebhookOrder(t *testing.T, db *gorm.DB, intentID, status, financialStatus string) models.ShopOrder {
	t.Helper()
	order := models.ShopOrder{
		ID:               uuid.NewString(),
		UserID:           "webhook-user",
		PaymentIntentID:  shopOrderStringPointer(intentID),
		Status:           status,
		FinancialStatus:  financialStatus,
		Currency:         "HKD",
		TotalAmountMinor: 1000,
		CustomerName:     "DB Buyer",
		CustomerEmail:    "db-buyer@example.com",
		CustomerPhone:    "+85291112222",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return order
}

func TestPaymentsWebhookPaymentFailedMarksOrder(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	order := seedWebhookOrder(t, db, "pi_fail_1", "pending_payment", "pending")

	payload := webhookEventPayload("evt_fail_1", "payment_intent.payment_failed", "pi_fail_1",
		`, "last_payment_error": {"code": "card_declined", "message": "Your card was declined."}`)

	rec := serveWebhookEvent(t, db, &recordingFulfiller{}, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_failed" {
		t.Fatalf("expected payment_failed, got %q", updated.Status)
	}
	if updated.FinancialStatus != "failed" {
		t.Fatalf("expected financial_status failed, got %q", updated.FinancialStatus)
	}
	if updated.FailureReason != "card_declined: Your card was declined." {
		t.Fatalf("unexpected failure reason: %q", updated.FailureReason)
	}

	// Replay of the same event is a no-op (integration event already completed).
	order.Status = "pending_payment"
	replay := serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_fail_1", "payment_intent.payment_failed", "pi_fail_1",
		`, "last_payment_error": {"code": "card_declined", "message": "Your card was declined."}`))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d", replay.Code)
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_failed" {
		t.Fatalf("replay must not change state, got %q", updated.Status)
	}
}

func TestPaymentsWebhookFailedAfterSucceededDoesNotRegress(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	order := seedWebhookOrder(t, db, "pi_paid_1", "paid", "paid")

	rec := serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_fail_after_paid", "payment_intent.payment_failed", "pi_paid_1",
		`, "last_payment_error": {"code": "card_declined", "message": "late failure"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "paid" {
		t.Fatalf("paid order must not regress, got %q", updated.Status)
	}
	if updated.FinancialStatus != "paid" {
		t.Fatalf("financial_status must not regress, got %q", updated.FinancialStatus)
	}
	if updated.FailureReason == "card_declined: late failure" {
		t.Fatalf("failure reason must not overwrite a paid order")
	}
}

func TestPaymentsWebhookCanceledMarksOrder(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	order := seedWebhookOrder(t, db, "pi_cancel_1", "pending_payment", "pending")

	rec := serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_cancel_1", "payment_intent.canceled", "pi_cancel_1", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "canceled" {
		t.Fatalf("expected canceled, got %q", updated.Status)
	}
	if updated.FinancialStatus != "voided" {
		t.Fatalf("expected financial_status voided, got %q", updated.FinancialStatus)
	}

	// Replay is a no-op.
	replay := serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_cancel_1", "payment_intent.canceled", "pi_cancel_1", ""))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d", replay.Code)
	}
}

func TestPaymentsWebhookCanceledAfterPaidDoesNotRegress(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	order := seedWebhookOrder(t, db, "pi_paid_2", "processing", "paid")

	rec := serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_cancel_after_paid", "payment_intent.canceled", "pi_paid_2", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "processing" {
		t.Fatalf("processing order must not regress, got %q", updated.Status)
	}
	if updated.FinancialStatus != "paid" {
		t.Fatalf("financial_status must not regress, got %q", updated.FinancialStatus)
	}
}

func TestPaymentsWebhookSucceededUsesOrderSnapshot(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	seedWebhookOrder(t, db, "pi_snap_1", "pending_payment", "pending")

	// Metadata carries conflicting client-era PII; the order snapshot must win.
	fulfiller := &recordingFulfiller{}
	rec := serveWebhookEvent(t, db, fulfiller, []byte(`{
		"id": "evt_snap_1",
		"object": "event",
		"api_version": "2026-02-25.clover",
		"type": "payment_intent.succeeded",
		"data": {
			"object": {
				"id": "pi_snap_1",
				"object": "payment_intent",
				"receipt_email": "receipt@example.com",
				"metadata": {
					"customer_name": "Metadata Name",
					"customer_phone": "0000",
					"item_1": "source=shopify | handle=test-product | variant=gid://shopify/ProductVariant/1 | qty:1"
				}
			}
		}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !fulfiller.called {
		t.Fatalf("fulfillment was not dispatched")
	}
	if fulfiller.request.CustomerName != "DB Buyer" ||
		fulfiller.request.CustomerEmail != "db-buyer@example.com" ||
		fulfiller.request.CustomerPhone != "+85291112222" {
		t.Fatalf("fulfillment must use the order snapshot, got %+v", fulfiller.request)
	}
}

func TestPaymentsWebhookOrderLookupDBErrorReturns500(t *testing.T) {
	// No shop_orders table → the order lookup hits a REAL DB error → 500 so
	// Stripe retries.
	db := setupWebhookLifecycleDB(t, false)

	rec := serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_dberr_1", "payment_intent.succeeded", "pi_dberr_1", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPaymentsWebhookFindsOrderByMetadataWhenBackfillMissing(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	// Order without a back-filled intent id — only the pawrd_order_id metadata
	// links the intent to the order.
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "webhook-user", PaymentIntentID: nil,
		Status: "pending_payment", Currency: "HKD", TotalAmountMinor: 1000,
		CustomerName: "Reconciled Buyer", CustomerEmail: "reconciled@example.com",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	fulfiller := &recordingFulfiller{}
	rec := serveWebhookEvent(t, db, fulfiller, []byte(`{
		"id": "evt_reconcile_1",
		"object": "event",
		"api_version": "2026-02-25.clover",
		"type": "payment_intent.succeeded",
		"data": {
			"object": {
				"id": "pi_reconcile_1",
				"object": "payment_intent",
				"metadata": {
					"pawrd_order_id": "`+order.ID+`",
					"item_1": "source=shopify | handle=test-product | variant=gid://shopify/ProductVariant/1 | qty:1"
				}
			}
		}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !fulfiller.called || fulfiller.request.CustomerName != "Reconciled Buyer" {
		t.Fatalf("expected order found via metadata, got %+v", fulfiller.request)
	}

	// The reconciliation gap is closed: the intent id is back-filled.
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.PaymentIntentIDValue() != "pi_reconcile_1" {
		t.Fatalf("expected back-filled intent id, got %q", updated.PaymentIntentIDValue())
	}
}

func TestTerminalOrderStatesAreConsistentInOrdersAPI(t *testing.T) {
	db := setupWebhookLifecycleDB(t, true)
	failed := seedWebhookOrder(t, db, "pi_api_fail", "pending_payment", "pending")
	canceled := seedWebhookOrder(t, db, "pi_api_cancel", "pending_payment", "pending")

	serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_api_fail", "payment_intent.payment_failed", "pi_api_fail",
		`, "last_payment_error": {"code": "card_declined", "message": "declined"}`))
	serveWebhookEvent(t, db, &recordingFulfiller{}, webhookEventPayload("evt_api_cancel", "payment_intent.canceled", "pi_api_cancel", ""))

	token, err := auth.GenerateToken("webhook-user", "webhook@example.com", "Webhook User")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/shop/orders", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	NewShopOrdersHandler(db).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Orders []struct {
			ID              string `json:"id"`
			Status          string `json:"status"`
			FinancialStatus string `json:"financialStatus"`
		} `json:"orders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	expected := map[string][2]string{
		failed.ID:   {"payment_failed", "failed"},
		canceled.ID: {"canceled", "voided"},
	}
	seen := 0
	for _, o := range payload.Orders {
		want, ok := expected[o.ID]
		if !ok {
			continue
		}
		seen++
		if o.Status != want[0] || o.FinancialStatus != want[1] {
			t.Fatalf("order %s: expected (%s, %s), got (%s, %s)", o.ID, want[0], want[1], o.Status, o.FinancialStatus)
		}
	}
	if seen != 2 {
		t.Fatalf("expected both terminal orders in API payload, saw %d", seen)
	}
}
