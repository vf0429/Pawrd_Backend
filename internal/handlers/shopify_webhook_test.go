package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
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
