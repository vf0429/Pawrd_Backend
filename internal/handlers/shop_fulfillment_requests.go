package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

// NewShopOrderFulfillmentRequestHandler is the explicit operator action for
// sending a Shopify fulfillment request to the assigned service (normally
// DSers). Automatic requests have a separate, default-off configuration flag.
func NewShopOrderFulfillmentRequestHandler(
	db *gorm.DB,
	requester shopify.AdminFulfillmentRequester,
	adminKey string,
) http.HandlerFunc {
	expectedAdminKey := strings.TrimSpace(adminKey)
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if expectedAdminKey == "" {
			http.Error(w, "shop fulfillment operations are not configured", http.StatusServiceUnavailable)
			return
		}
		if !constantTimeStringEqual(expectedAdminKey, r.Header.Get("X-Shop-Admin-Key")) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if requester == nil {
			http.Error(w, "Shopify fulfillment operations are unavailable", http.StatusServiceUnavailable)
			return
		}
		orderID := strings.TrimSpace(r.PathValue("orderID"))
		var order models.ShopOrder
		if err := db.First(&order, "id = ?", orderID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "Order not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to load order", http.StatusInternalServerError)
			return
		}
		result, err := payments.RequestOrderFulfillmentIfEligible(
			r.Context(),
			db,
			requester,
			order.ID,
		)
		if err != nil {
			if errors.Is(err, payments.ErrFulfillmentRequestIneligible) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			http.Error(w, "Shopify fulfillment request failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"orderId": order.ID, "shopifyOrderId": order.ShopifyOrderGID(),
			"result": result,
		})
	}
}
