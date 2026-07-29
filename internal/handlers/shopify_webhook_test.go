package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newShopifyWebhookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ShopOrder{}, &models.ShopIntegrationEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func signedShopifyWebhookRequest(t *testing.T, secret, topic, deliveryID string, body []byte) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/api/shop/webhooks/shopify", bytes.NewReader(body))
	req.Header.Set("X-Shopify-Hmac-Sha256", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-Shopify-Topic", topic)
	req.Header.Set("X-Shopify-Webhook-Id", deliveryID)
	return req
}

func TestShopifyWebhookUpdatesFulfillmentAndDeduplicatesDelivery(t *testing.T) {
	const secret = "webhook-secret"
	db := newShopifyWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_1"),
		ShopifyOrderLegacyID: "123", Status: "processing", Currency: "HKD",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: secret}, db)

	body := []byte(`{"id":456,"order_id":123,"status":"success","shipment_status":"in_transit","tracking_company":"SF Express","tracking_number":"SF123","tracking_url":"https://example.com/SF123"}`)
	req := signedShopifyWebhookRequest(t, secret, "fulfillments/update", "delivery-1", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "shipped" || updated.FulfillmentStatus != "in_transit" ||
		updated.TrackingNumber != "SF123" {
		t.Fatalf("fulfillment was not applied: %+v", updated)
	}

	duplicateBody := []byte(`{"id":456,"order_id":123,"shipment_status":"delivered","tracking_number":"changed"}`)
	duplicateReq := signedShopifyWebhookRequest(t, secret, "fulfillments/update", "delivery-1", duplicateBody)
	duplicateRec := httptest.NewRecorder()
	handler.ServeHTTP(duplicateRec, duplicateReq)
	if duplicateRec.Code != http.StatusOK {
		t.Fatalf("expected duplicate delivery to return 200, got %d", duplicateRec.Code)
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "shipped" || updated.TrackingNumber != "SF123" {
		t.Fatalf("duplicate delivery was applied twice: %+v", updated)
	}
}

func TestShopifyWebhookEarlyEventRetriesAfterOrderMappingAppears(t *testing.T) {
	const secret = "webhook-secret"
	db := newShopifyWebhookTestDB(t)
	handler := NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: secret}, db)
	body := []byte(`{"id":456,"order_id":123,"shipment_status":"in_transit","tracking_number":"SF-EARLY"}`)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "fulfillments/update", "delivery-early", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unmapped early event must be retried, got %d: %s", rec.Code, rec.Body.String())
	}
	var event models.ShopIntegrationEvent
	if err := db.First(&event, "provider = ? AND external_event_id = ?", "shopify", "delivery-early").Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "failed" || !strings.Contains(event.LastError, errShopifyWebhookOrderNotMapped.Error()) {
		t.Fatalf("early event was incorrectly completed: %+v", event)
	}

	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-early", PaymentIntentID: shopOrderStringPointer("pi_early"),
		ShopifyOrderLegacyID: "123", Status: "processing", Currency: "HKD",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "fulfillments/update", "delivery-early", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("mapped event replay should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "shipped" || updated.TrackingNumber != "SF-EARLY" {
		t.Fatalf("replayed early event was not applied: %+v", updated)
	}
	if err := db.First(&event, "id = ?", event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != "completed" || event.ProcessedAt == nil || event.LastError != "" {
		t.Fatalf("replayed event was not completed: %+v", event)
	}
}

func TestShopifyReturnWebhooksResolveNestedOrderPayloads(t *testing.T) {
	const secret = "webhook-secret"
	testCases := []struct {
		topic          string
		suppliedStatus string
		expectedStatus string
	}{
		{topic: "returns/request", expectedStatus: "REQUESTED"},
		{topic: "returns/approve", expectedStatus: "OPEN"},
		{topic: "returns/decline", expectedStatus: "DECLINED"},
		{topic: "returns/cancel", expectedStatus: "CANCELED"},
		{topic: "returns/close", expectedStatus: "CLOSED"},
		{topic: "returns/reopen", expectedStatus: "OPEN"},
		{topic: "returns/process", suppliedStatus: "closed", expectedStatus: "CLOSED"},
	}
	for index, testCase := range testCases {
		t.Run(testCase.topic, func(t *testing.T) {
			db := newShopifyWebhookTestDB(t)
			legacyID := fmt.Sprintf("47832965448%d", index)
			order := models.ShopOrder{
				ID: uuid.NewString(), UserID: "user-return", PaymentIntentID: shopOrderStringPointer("pi_" + legacyID),
				ShopifyOrderLegacyID: legacyID, Status: "delivered", ReturnStatus: "REQUESTED",
				Currency: "HKD",
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			body := []byte(fmt.Sprintf(
				`{"id":123,"admin_graphql_api_id":"gid://shopify/Return/123","status":%q,"order":{"id":%s,"admin_graphql_api_id":"gid://shopify/Order/%s"}}`,
				testCase.suppliedStatus,
				legacyID,
				legacyID,
			))
			rec := httptest.NewRecorder()
			NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: secret}, db).ServeHTTP(
				rec,
				signedShopifyWebhookRequest(t, secret, testCase.topic, "return-"+fmt.Sprint(index), body),
			)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var updated models.ShopOrder
			if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if updated.ReturnStatus != testCase.expectedStatus ||
				updated.Status != "return_"+strings.ToLower(testCase.expectedStatus) {
				t.Fatalf("nested return payload was not applied: %+v", updated)
			}
		})
	}
}

func TestShopifyReturnUpdateCanResolveSavedReturnGID(t *testing.T) {
	const secret = "webhook-secret"
	db := newShopifyWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-return-update", PaymentIntentID: shopOrderStringPointer("pi_return_update"),
		ShopifyOrderLegacyID: "8080", ReturnID: "gid://shopify/Return/9090",
		Status: "return_requested", ReturnStatus: "REQUESTED", Currency: "HKD",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: secret}, db)

	body := []byte(`{"admin_graphql_api_id":"gid://shopify/Return/9090","status":"open"}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "returns/update", "return-update-1", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.ReturnStatus != "OPEN" || updated.Status != "return_open" {
		t.Fatalf("return GID update was not applied: %+v", updated)
	}

	feesOnlyBody := []byte(`{"admin_graphql_api_id":"gid://shopify/Return/9090","return_shipping_fees":{"updates":[]}}`)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "returns/update", "return-update-2", feesOnlyBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("fees-only update for a mapped return should be acknowledged, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.ReturnStatus != "OPEN" || updated.Status != "return_open" {
		t.Fatalf("fees-only update changed tracked lifecycle: %+v", updated)
	}
}

func TestShopifyWebhookRejectsInvalidSignature(t *testing.T) {
	db := newShopifyWebhookTestDB(t)
	req := httptest.NewRequest(http.MethodPost, "/api/shop/webhooks/shopify", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-Shopify-Hmac-Sha256", "invalid")
	req.Header.Set("X-Shopify-Topic", "refunds/create")
	req.Header.Set("X-Shopify-Webhook-Id", "delivery-1")
	rec := httptest.NewRecorder()
	NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: "secret"}, db).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestShopifyRefundNotificationDoesNotManufactureStripeMoneyState(t *testing.T) {
	const secret = "webhook-secret"
	db := newShopifyWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_refund_notice"),
		ShopifyOrderLegacyID: "123", Status: "return_closed", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: secret}, db)
	body := []byte(`{"id":456,"order_id":123}`)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "refunds/create", "refund-delivery-1", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.FinancialStatus != "paid" || updated.Status != "return_closed" ||
		updated.RefundedAmountMinor != 0 {
		t.Fatalf("Shopify notification fabricated a refund state: %+v", updated)
	}

	if err := db.Model(&updated).Update("refunded_amount_minor", 400).Error; err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "refunds/create", "refund-delivery-2", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.FinancialStatus != "paid" ||
		updated.Status != "return_closed" ||
		updated.RefundedAmountMinor != 400 {
		t.Fatalf("Shopify notification changed authoritative money state: %+v", updated)
	}
}

func TestShopifyCanceledOrderRequiresSeparateStripeRefund(t *testing.T) {
	const secret = "webhook-secret"
	db := newShopifyWebhookTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_cancel_notice"),
		ShopifyOrderLegacyID: "789", Status: "processing", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewShopifyWebhookHandler(&config.Config{ShopifyWebhookSecret: secret}, db)
	body := []byte(`{"id":789,"cancel_reason":"customer"}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedShopifyWebhookRequest(t, secret, "orders/cancelled", "cancel-delivery-1", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "canceled" || updated.FulfillmentStatus != "CANCELLED" ||
		updated.FinancialStatus != "paid" || updated.RefundedAmountMinor != 0 {
		t.Fatalf("cancellation must not fabricate a Stripe refund: %+v", updated)
	}
}

func TestShopifyWebhookUpdatesIntegrationFieldsWithoutRegressingProtectedOrderStatus(t *testing.T) {
	const secret = "webhook-secret"
	tests := []struct {
		name            string
		topic           string
		body            string
		status          string
		financialStatus string
		disputeStatus   string
		failureReason   string
		wantFulfillment string
		wantTracking    string
		wantReturn      string
	}{
		{
			name:   "late fulfillment after refund",
			topic:  "fulfillments/update",
			body:   `{"order_id":901,"shipment_status":"in_transit","tracking_number":"LATE-REFUND"}`,
			status: "refunded", financialStatus: "refunded",
			wantFulfillment: "in_transit", wantTracking: "LATE-REFUND",
		},
		{
			name:   "late fulfilled after dispute",
			topic:  "orders/fulfilled",
			body:   `{"id":902}`,
			status: "payment_disputed", financialStatus: "disputed",
			disputeStatus: "needs_response", wantFulfillment: "FULFILLED",
		},
		{
			name:   "late cancellation during refund reconciliation",
			topic:  "orders/cancelled",
			body:   `{"id":903,"cancel_reason":"customer"}`,
			status: "refund_reconciliation_required", financialStatus: "paid",
			failureReason:   "Automatic Stripe refund requires reconciliation",
			wantFulfillment: "CANCELLED",
		},
		{
			name:   "late return during order reconciliation",
			topic:  "returns/close",
			body:   `{"id":77,"status":"closed","order":{"id":904}}`,
			status: "reconciliation_required", financialStatus: "paid",
			failureReason: "Shopify order requires reconciliation",
			wantReturn:    "CLOSED",
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newShopifyWebhookTestDB(t)
			legacyID := fmt.Sprint(901 + index)
			order := models.ShopOrder{
				ID: uuid.NewString(), UserID: "user-terminal",
				PaymentIntentID:      shopOrderStringPointer("pi_terminal_" + fmt.Sprint(index)),
				ShopifyOrderLegacyID: legacyID,
				Status:               test.status,
				FinancialStatus:      test.financialStatus,
				DisputeStatus:        test.disputeStatus,
				FailureReason:        test.failureReason,
				Currency:             "HKD",
				TotalAmountMinor:     1000,
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			NewShopifyWebhookHandler(
				&config.Config{ShopifyWebhookSecret: secret},
				db,
			).ServeHTTP(
				rec,
				signedShopifyWebhookRequest(
					t,
					secret,
					test.topic,
					"protected-"+fmt.Sprint(index),
					[]byte(test.body),
				),
			)
			if rec.Code != http.StatusOK {
				t.Fatalf("webhook response=%d body=%s", rec.Code, rec.Body.String())
			}
			var stored models.ShopOrder
			if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != test.status ||
				stored.FinancialStatus != test.financialStatus ||
				stored.FailureReason != test.failureReason {
				t.Fatalf("Shopify webhook regressed protected business state: %+v", stored)
			}
			if stored.FulfillmentStatus != test.wantFulfillment ||
				stored.TrackingNumber != test.wantTracking ||
				stored.ReturnStatus != test.wantReturn {
				t.Fatalf("Shopify integration fields were not updated: %+v", stored)
			}
		})
	}
}
