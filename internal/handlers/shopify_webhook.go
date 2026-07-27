package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

func NewShopifyWebhookHandler(cfg *config.Config, db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if cfg.ShopifyWebhookSecret == "" {
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
		if err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if !validShopifyHMAC(body, r.Header.Get("X-Shopify-Hmac-Sha256"), cfg.ShopifyWebhookSecret) {
			http.Error(w, "signature verification failed", http.StatusUnauthorized)
			return
		}
		deliveryID := strings.TrimSpace(r.Header.Get("X-Shopify-Webhook-Id"))
		if deliveryID == "" {
			deliveryID = strings.TrimSpace(r.Header.Get("X-Shopify-Event-Id"))
		}
		topic := strings.TrimSpace(r.Header.Get("X-Shopify-Topic"))
		if deliveryID == "" || topic == "" {
			http.Error(w, "missing webhook headers", http.StatusBadRequest)
			return
		}
		var event models.ShopIntegrationEvent
		err = db.Where("provider = ? AND external_event_id = ?", "shopify", deliveryID).First(&event).Error
		if err == nil && event.Status == "completed" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			http.Error(w, "event store unavailable", http.StatusInternalServerError)
			return
		}
		if event.ID == "" {
			event = models.ShopIntegrationEvent{ID: uuid.NewString(), Provider: "shopify", ExternalEventID: deliveryID, Topic: topic, Status: "processing"}
			if err := db.Create(&event).Error; err != nil {
				http.Error(w, "event store unavailable", http.StatusInternalServerError)
				return
			}
		}
		if err := applyShopifyWebhook(db, topic, body); err != nil {
			_ = db.Model(&event).Updates(map[string]any{"status": "failed", "last_error": err.Error()}).Error
			http.Error(w, "webhook processing failed", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		_ = db.Model(&event).Updates(map[string]any{"status": "completed", "processed_at": &now, "last_error": ""}).Error
		w.WriteHeader(http.StatusOK)
	}
}

func validShopifyHMAC(body []byte, supplied, secret string) bool {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(supplied))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(decoded, mac.Sum(nil))
}

func applyShopifyWebhook(db *gorm.DB, topic string, body []byte) error {
	var payload struct {
		ID              json.Number `json:"id"`
		OrderID         json.Number `json:"order_id"`
		Status          string      `json:"status"`
		ShipmentStatus  string      `json:"shipment_status"`
		TrackingCompany string      `json:"tracking_company"`
		TrackingNumber  string      `json:"tracking_number"`
		TrackingURL     string      `json:"tracking_url"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	orderLegacyID := payload.OrderID.String()
	if orderLegacyID == "" && strings.HasPrefix(topic, "orders/") {
		orderLegacyID = payload.ID.String()
	}
	if orderLegacyID == "" {
		return nil
	}
	updates := map[string]any{}
	switch {
	case strings.HasPrefix(topic, "fulfillments/"):
		status := payload.ShipmentStatus
		if status == "" {
			status = payload.Status
		}
		updates["fulfillment_status"] = status
		updates["tracking_company"] = payload.TrackingCompany
		updates["tracking_number"] = payload.TrackingNumber
		updates["tracking_url"] = payload.TrackingURL
		updates["status"] = "shipped"
		if strings.EqualFold(status, "delivered") {
			now := time.Now().UTC()
			updates["status"] = "delivered"
			updates["delivered_at"] = &now
		}
	case strings.HasPrefix(topic, "returns/"):
		updates["return_status"] = strings.ToUpper(payload.Status)
		updates["status"] = "return_" + strings.ToLower(payload.Status)
	case topic == "refunds/create":
		updates["financial_status"] = "refunded"
		updates["status"] = "refunded"
	case topic == "orders/fulfilled":
		updates["fulfillment_status"] = "FULFILLED"
		updates["status"] = "shipped"
	default:
		return nil
	}
	return db.Model(&models.ShopOrder{}).Where("shopify_order_legacy_id = ?", orderLegacyID).Updates(updates).Error
}
