package handlers

// Phase 4 webhook guarantees re-ported onto the mainline quote stack:
// metadata-fallback order lookup with intent-id back-fill, terminal failure
// states (payment_failed → failed, canceled → voided) with no-regression,
// order-snapshot-authoritative customer data, and DB-error → 500 retry.

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func phase4LifecycleEvent(eventID, eventType, intentID, orderID, extra string) []byte {
	metadata := `"pawrd_order_id": "` + orderID + `"`
	return []byte(`{
		"id": "` + eventID + `",
		"object": "event",
		"api_version": "2026-02-25.clover",
		"type": "` + eventType + `",
		"data": {
			"object": {
				"id": "` + intentID + `",
				"object": "payment_intent",
				"metadata": {` + metadata + `}` + extra + `
			}
		}
	}`)
}

// A consumed quote whose order never got its intent id back-filled is still
// reconciled: the terminal event finds the order via pawrd_order_id metadata,
// back-fills the intent id and applies the terminal state atomically.
func TestPhase4WebhookTerminalViaMetadataFallback(t *testing.T) {
	const secret = "whsec_p4_fallback"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-p4", PaymentIntentID: nil,
		Status: "pending_payment", FinancialStatus: "pending",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	payload := phase4LifecycleEvent("evt_p4_fail", "payment_intent.payment_failed", "pi_p4_fallback", order.ID,
		`, "last_payment_error": {"code": "card_declined", "message": "Your card was declined."}`)
	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_failed" || updated.FinancialStatus != "failed" {
		t.Fatalf("expected payment_failed/failed, got %q/%q", updated.Status, updated.FinancialStatus)
	}
	if updated.PaymentIntentIDValue() != "pi_p4_fallback" {
		t.Fatalf("intent id must be back-filled via the metadata fallback, got %q", updated.PaymentIntentIDValue())
	}
	if updated.FailureReason != "card_declined: Your card was declined." {
		t.Fatalf("unexpected failure reason: %q", updated.FailureReason)
	}

	// Replay is a no-op.
	replay := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, payload)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: expected 200, got %d", replay.Code)
	}
}

// A failed event arriving after success must not regress a paid order —
// neither status nor financial_status.
func TestPhase4WebhookFailedAfterPaidDoesNotRegress(t *testing.T) {
	const secret = "whsec_p4_noregress"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-p4", PaymentIntentID: shopOrderStringPointer("pi_p4_paid"),
		Status: "processing", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret,
		phase4LifecycleEvent("evt_p4_late_fail", "payment_intent.payment_failed", "pi_p4_paid", order.ID,
			`, "last_payment_error": {"code": "card_declined", "message": "late failure"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "processing" || updated.FinancialStatus != "paid" {
		t.Fatalf("paid order must not regress, got %q/%q", updated.Status, updated.FinancialStatus)
	}
}

// Canceled maps to payment_canceled + voided (mainline vocabulary), atomically.
func TestPhase4WebhookCanceledMapsToVoided(t *testing.T) {
	const secret = "whsec_p4_cancel"
	db := newPaymentsWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-p4", PaymentIntentID: shopOrderStringPointer("pi_p4_cancel"),
		Status: "pending_payment", FinancialStatus: "pending",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret,
		phase4LifecycleEvent("evt_p4_cancel", "payment_intent.canceled", "pi_p4_cancel", order.ID,
			`, "cancellation_reason": "abandoned"`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "payment_canceled" || updated.FinancialStatus != "voided" {
		t.Fatalf("expected payment_canceled/voided, got %q/%q", updated.Status, updated.FinancialStatus)
	}
}

// Customer data for fulfillment comes from the durable order snapshot, not
// from PaymentIntent metadata.
func TestPhase4WebhookSucceededUsesOrderSnapshotCustomer(t *testing.T) {
	const webhookSecret = "whsec_test"
	db, payload := newSucceededWebhookFixture(t)
	if err := db.Model(&models.ShopOrder{}).Where("id = ?", "11111111-1111-1111-1111-111111111111").
		Updates(map[string]any{
			"customer_name":  "DB Buyer",
			"customer_email": "db-buyer@example.com",
			"customer_phone": "+85291112222",
		}).Error; err != nil {
		t.Fatal(err)
	}
	// Metadata carries conflicting legacy PII; the order snapshot must win.
	payload = bytes.Replace(payload, []byte(`"customer_name": "Test Buyer"`), []byte(`"customer_name": "Metadata Name"`), 1)

	fulfiller := &recordingFulfiller{}
	rec := serveSignedStripeWebhook(t, db, fulfiller, webhookSecret, payload)
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

// A REAL DB error during order lookup → 500 so Stripe retries.
func TestPhase4WebhookOrderLookupDBErrorReturns500(t *testing.T) {
	const secret = "whsec_p4_dberr"
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Only the event store exists — the shop_orders lookup hits a real error.
	if err := db.AutoMigrate(&models.ShopIntegrationEvent{}); err != nil {
		t.Fatal(err)
	}
	_, payload := newSucceededWebhookFixture(t)

	rec := serveSignedStripeWebhook(t, db, &recordingFulfiller{}, secret, payload)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}
