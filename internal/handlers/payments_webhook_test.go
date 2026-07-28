package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if err := db.AutoMigrate(&models.ShopIntegrationEvent{}); err != nil {
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
