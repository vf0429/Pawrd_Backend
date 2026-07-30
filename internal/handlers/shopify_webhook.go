package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

var errShopifyWebhookOrderNotMapped = errors.New("Shopify webhook order is not mapped yet")

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
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "event store unavailable", http.StatusInternalServerError)
			return
		}
		if event.ID == "" {
			event = models.ShopIntegrationEvent{ID: uuid.NewString(), Provider: "shopify", ExternalEventID: deliveryID, Topic: topic, Status: "processing"}
			if err := db.Create(&event).Error; err != nil {
				http.Error(w, "event store unavailable", http.StatusInternalServerError)
				return
			}
		} else {
			if err := db.Model(&event).Updates(map[string]any{
				"status": "processing", "last_error": "", "processed_at": nil,
			}).Error; err != nil {
				http.Error(w, "event store unavailable", http.StatusInternalServerError)
				return
			}
		}
		if err := applyShopifyWebhook(db, topic, body); err != nil {
			if storeErr := db.Model(&event).Updates(map[string]any{
				"status": "failed", "last_error": err.Error(),
			}).Error; storeErr != nil {
				http.Error(w, "event store unavailable", http.StatusInternalServerError)
				return
			}
			http.Error(w, "webhook processing failed", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		result := db.Model(&event).Updates(map[string]any{
			"status": "completed", "processed_at": &now, "last_error": "",
		})
		if result.Error != nil || result.RowsAffected != 1 {
			http.Error(w, "event store unavailable", http.StatusInternalServerError)
			return
		}
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
		ID                json.Number `json:"id"`
		AdminGraphQLAPIID string      `json:"admin_graphql_api_id"`
		OrderID           json.Number `json:"order_id"`
		Order             struct {
			ID                json.Number `json:"id"`
			AdminGraphQLAPIID string      `json:"admin_graphql_api_id"`
		} `json:"order"`
		Status          string `json:"status"`
		ShipmentStatus  string `json:"shipment_status"`
		TrackingCompany string `json:"tracking_company"`
		TrackingNumber  string `json:"tracking_number"`
		TrackingURL     string `json:"tracking_url"`
		CancelReason    string `json:"cancel_reason"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return err
	}
	orderLegacyID := strings.TrimSpace(payload.OrderID.String())
	if orderLegacyID == "" {
		orderLegacyID = strings.TrimSpace(payload.Order.ID.String())
	}
	if orderLegacyID == "" {
		orderLegacyID = shopifyLegacyIDFromGID(payload.Order.AdminGraphQLAPIID, "Order")
	}
	if orderLegacyID == "" && strings.HasPrefix(topic, "orders/") {
		orderLegacyID = strings.TrimSpace(payload.ID.String())
	}
	returnID := ""
	if strings.HasPrefix(topic, "returns/") {
		returnID = strings.TrimSpace(payload.AdminGraphQLAPIID)
		if returnID == "" {
			returnLegacyID := strings.TrimSpace(payload.ID.String())
			if returnLegacyID != "" {
				returnID = "gid://shopify/Return/" + returnLegacyID
			}
		}
	}

	knownTopic := strings.HasPrefix(topic, "fulfillments/") ||
		strings.HasPrefix(topic, "returns/") ||
		topic == "refunds/create" ||
		topic == "orders/fulfilled" ||
		topic == "orders/cancelled"
	if !knownTopic {
		return nil
	}

	order, err := shopifyWebhookOrder(db, orderLegacyID, returnID)
	if err != nil {
		return err
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
		updates["status"] = shopOrderLogisticsStatusUnlessProtected("shipped")
		if strings.EqualFold(status, "delivered") {
			now := time.Now().UTC()
			updates["status"] = shopOrderLogisticsStatusUnlessProtected("delivered")
			updates["delivered_at"] = &now
		}
	case strings.HasPrefix(topic, "returns/"):
		returnStatus := shopifyWebhookReturnStatus(topic, payload.Status)
		if returnStatus == "" {
			// Shopify returns/update can contain only the Return GID and fee or
			// line-item deltas. Pawrd does not mirror those P1 fields, but
			// resolving the saved Return GID proves this is not an early event.
			if topic == "returns/update" {
				return nil
			}
			return fmt.Errorf("Shopify %s webhook is missing a return status", topic)
		}
		updates["return_status"] = returnStatus
		updates["status"] = shopOrderStatusUnlessProtected(
			"return_" + strings.ToLower(returnStatus),
		)
	case topic == "refunds/create":
		// Stripe, not Shopify, is Pawrd's payment processor. This delivery is
		// still recorded in ShopIntegrationEvent, but it is never allowed to
		// derive or overwrite Pawrd's money state. Stripe refund/dispute
		// webhooks are the sole authority for those fields.
		return nil
	case topic == "orders/fulfilled":
		updates["fulfillment_status"] = "FULFILLED"
		updates["status"] = shopOrderLogisticsStatusUnlessProtected("shipped")
	case topic == "orders/cancelled":
		updates["fulfillment_status"] = "CANCELLED"
		updates["status"] = shopOrderLogisticsStatusUnlessProtected("canceled")
		reason := strings.TrimSpace(payload.CancelReason)
		if reason == "" {
			reason = "Shopify order canceled; Stripe refund requires operator confirmation"
		} else {
			reason = "Shopify order canceled: " + reason
		}
		updates["failure_reason"] = shopOrderFailureUnlessProtected(reason)
	default:
		return nil
	}
	result := db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: order %s disappeared before applying %s", errShopifyWebhookOrderNotMapped, order.ID, topic)
	}
	return nil
}

func shopifyWebhookOrder(db *gorm.DB, orderLegacyID, returnID string) (models.ShopOrder, error) {
	var order models.ShopOrder
	var err error
	switch {
	case strings.TrimSpace(orderLegacyID) != "":
		err = db.First(&order, "shopify_order_legacy_id = ?", strings.TrimSpace(orderLegacyID)).Error
	case strings.TrimSpace(returnID) != "":
		err = db.First(&order, "return_id = ?", strings.TrimSpace(returnID)).Error
	default:
		return order, fmt.Errorf("%w: webhook payload has no order identity", errShopifyWebhookOrderNotMapped)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return order, fmt.Errorf(
			"%w: legacy_order_id=%q return_id=%q",
			errShopifyWebhookOrderNotMapped,
			strings.TrimSpace(orderLegacyID),
			strings.TrimSpace(returnID),
		)
	}
	return order, err
}

func shopifyLegacyIDFromGID(value, resource string) string {
	value = strings.TrimSpace(value)
	prefix := "gid://shopify/" + strings.TrimSpace(resource) + "/"
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	legacyID := strings.TrimPrefix(value, prefix)
	if legacyID == "" || strings.Contains(legacyID, "/") {
		return ""
	}
	for _, char := range legacyID {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return legacyID
}

func shopifyWebhookReturnStatus(topic, supplied string) string {
	if status := strings.ToUpper(strings.TrimSpace(supplied)); status != "" {
		return status
	}
	switch topic {
	case "returns/request":
		return "REQUESTED"
	case "returns/approve", "returns/reopen":
		return "OPEN"
	case "returns/process":
		// Processing can be partial; Shopify closes the return separately once
		// all items and restock decisions are complete.
		return "OPEN"
	case "returns/decline":
		return "DECLINED"
	case "returns/cancel":
		return "CANCELED"
	case "returns/close":
		return "CLOSED"
	default:
		return ""
	}
}
