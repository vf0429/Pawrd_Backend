package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

type shopOrderItemDTO struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	ImageURL        string `json:"imageUrl,omitempty"`
	Quantity        int    `json:"quantity"`
	UnitAmountMinor int64  `json:"unitAmountMinor"`
	Currency        string `json:"currency"`
}

type shopOrderDTO struct {
	ID                 string             `json:"id"`
	OrderNumber        string             `json:"orderNumber"`
	Status             string             `json:"status"`
	FinancialStatus    string             `json:"financialStatus"`
	FulfillmentStatus  string             `json:"fulfillmentStatus"`
	Currency           string             `json:"currency"`
	TotalAmountMinor   int64              `json:"totalAmountMinor"`
	Items              []shopOrderItemDTO `json:"items"`
	ShippingAddress    any                `json:"shippingAddress"`
	Tracking           any                `json:"tracking"`
	CustomerReceivedAt *time.Time         `json:"customerReceivedAt,omitempty"`
	ReturnRequest      any                `json:"returnRequest,omitempty"`
	CanConfirmReceipt  bool               `json:"canConfirmReceipt"`
	CanRequestReturn   bool               `json:"canRequestReturn"`
	CreatedAt          time.Time          `json:"createdAt"`
}

func NewShopOrdersHandler(db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		var orders []models.ShopOrder
		if err := db.Preload("Items").
			Where("user_id = ? AND status <> ?", userID, "pending_payment").
			Order("created_at DESC").Find(&orders).Error; err != nil {
			http.Error(w, "failed to load orders", http.StatusInternalServerError)
			return
		}
		result := make([]shopOrderDTO, 0, len(orders))
		for _, order := range orders {
			result = append(result, makeShopOrderDTO(order))
		}
		writeJSON(w, http.StatusOK, map[string]any{"orders": result})
	}
}

func NewShopOrderDetailHandler(db *gorm.DB, admin shopify.AdminOrderClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		order, ok := loadOwnedShopOrder(w, db, r.PathValue("orderID"), userID)
		if !ok {
			return
		}
		if admin != nil && order.ShopifyOrderID != "" {
			if snapshot, err := admin.FetchOrder(r.Context(), order.ShopifyOrderID); err == nil {
				updates := map[string]any{
					"fulfillment_status":    snapshot.FulfillmentStatus,
					"tracking_company":      snapshot.TrackingCompany,
					"tracking_number":       snapshot.TrackingNumber,
					"tracking_url":          snapshot.TrackingURL,
					"estimated_delivery_at": snapshot.EstimatedDeliveryAt,
					"delivered_at":          snapshot.DeliveredAt,
				}
				if snapshot.DeliveredAt != nil {
					updates["status"] = "delivered"
				} else if snapshot.TrackingNumber != "" {
					updates["status"] = "shipped"
				}
				_ = db.Model(order).Updates(updates).Error
				_ = db.Preload("Items").First(order, "id = ?", order.ID).Error
			}
		}
		writeJSON(w, http.StatusOK, makeShopOrderDTO(*order))
	}
}

func NewShopOrderReceivedHandler(db *gorm.DB, admin shopify.AdminOrderClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		order, ok := loadOwnedShopOrder(w, db, r.PathValue("orderID"), userID)
		if !ok {
			return
		}
		if !canConfirmReceipt(*order) {
			http.Error(w, "order cannot be confirmed as received", http.StatusConflict)
			return
		}
		if admin == nil || order.ShopifyOrderID == "" {
			http.Error(w, "Shopify order service is unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := admin.AddOrderTags(r.Context(), order.ShopifyOrderID, []string{"Pawrd: customer received"}); err != nil {
			http.Error(w, "failed to sync receipt confirmation", http.StatusBadGateway)
			return
		}
		now := time.Now().UTC()
		if err := db.Model(order).Updates(map[string]any{"customer_received_at": &now, "status": "received"}).Error; err != nil {
			http.Error(w, "failed to save receipt confirmation", http.StatusInternalServerError)
			return
		}
		order.CustomerReceivedAt = &now
		order.Status = "received"
		writeJSON(w, http.StatusOK, makeShopOrderDTO(*order))
	}
}

type shopReturnRequest struct {
	Reason    string `json:"reason"`
	Note      string `json:"note"`
	Confirmed bool   `json:"confirmed"`
}

var allowedShopReturnReasons = map[string]bool{
	"COLOR": true, "DEFECTIVE": true, "NOT_AS_DESCRIBED": true, "OTHER": true,
	"SIZE_TOO_LARGE": true, "SIZE_TOO_SMALL": true, "STYLE": true, "UNWANTED": true, "WRONG_ITEM": true,
}

func NewShopOrderReturnHandler(db *gorm.DB, admin shopify.AdminOrderClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		var req shopReturnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid return request", http.StatusBadRequest)
			return
		}
		req.Reason = strings.ToUpper(strings.TrimSpace(req.Reason))
		if !req.Confirmed || !allowedShopReturnReasons[req.Reason] {
			http.Error(w, "a valid return reason and confirmation are required", http.StatusBadRequest)
			return
		}
		order, ok := loadOwnedShopOrder(w, db, r.PathValue("orderID"), userID)
		if !ok {
			return
		}
		if !canRequestReturn(*order) {
			http.Error(w, "order is not eligible for a return request", http.StatusConflict)
			return
		}
		if admin == nil || order.ShopifyOrderID == "" {
			http.Error(w, "Shopify order service is unavailable", http.StatusServiceUnavailable)
			return
		}
		result, err := admin.RequestReturn(r.Context(), order.ShopifyOrderID, req.Reason, strings.TrimSpace(req.Note))
		if err != nil {
			http.Error(w, "Shopify return request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		if err := db.Model(order).Updates(map[string]any{
			"return_id": result.ID, "return_name": result.Name, "return_status": result.Status,
			"return_reason": req.Reason, "return_note": strings.TrimSpace(req.Note), "status": "return_requested",
		}).Error; err != nil {
			http.Error(w, "failed to save return request", http.StatusInternalServerError)
			return
		}
		order.ReturnID, order.ReturnName, order.ReturnStatus = result.ID, result.Name, result.Status
		order.ReturnReason, order.ReturnNote, order.Status = req.Reason, strings.TrimSpace(req.Note), "return_requested"
		writeJSON(w, http.StatusOK, makeShopOrderDTO(*order))
	}
}

func loadOwnedShopOrder(w http.ResponseWriter, db *gorm.DB, orderID, userID string) (*models.ShopOrder, bool) {
	var order models.ShopOrder
	err := db.Preload("Items").Where("id = ? AND user_id = ?", strings.TrimSpace(orderID), userID).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		http.Error(w, "order not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		http.Error(w, "failed to load order", http.StatusInternalServerError)
		return nil, false
	}
	return &order, true
}

func canConfirmReceipt(order models.ShopOrder) bool {
	if order.CustomerReceivedAt != nil || order.ReturnStatus != "" {
		return false
	}
	status := strings.ToUpper(order.FulfillmentStatus)
	return order.TrackingNumber != "" || status == "FULFILLED" || status == "IN_TRANSIT" || status == "DELIVERED"
}

func canRequestReturn(order models.ShopOrder) bool {
	return order.ReturnStatus == "" && (order.CustomerReceivedAt != nil || order.DeliveredAt != nil ||
		strings.EqualFold(order.FulfillmentStatus, "DELIVERED"))
}

func makeShopOrderDTO(order models.ShopOrder) shopOrderDTO {
	items := make([]shopOrderItemDTO, 0, len(order.Items))
	for _, item := range order.Items {
		items = append(items, shopOrderItemDTO{
			ID: item.ID, Title: item.Title, ImageURL: item.ImageURL, Quantity: item.Quantity,
			UnitAmountMinor: item.UnitAmountMinor, Currency: item.Currency,
		})
	}
	number := order.ShopifyOrderName
	if number == "" {
		number = "Pawrd-" + strings.ToUpper(order.ID[:min(8, len(order.ID))])
	}
	var returnRequest any
	if order.ReturnStatus != "" {
		returnRequest = map[string]any{
			"id": order.ReturnID, "name": order.ReturnName, "status": order.ReturnStatus,
			"reason": order.ReturnReason, "note": order.ReturnNote,
		}
	}
	return shopOrderDTO{
		ID: order.ID, OrderNumber: number, Status: order.Status,
		FinancialStatus: order.FinancialStatus, FulfillmentStatus: order.FulfillmentStatus,
		Currency: order.Currency, TotalAmountMinor: order.TotalAmountMinor, Items: items,
		ShippingAddress: map[string]any{
			"recipientName": order.CustomerName, "phone": order.CustomerPhone, "address1": order.ShippingAddress1,
			"district": order.ShippingDistrict, "region": order.ShippingRegion, "country": order.ShippingCountry,
		},
		Tracking: map[string]any{
			"company": order.TrackingCompany, "number": order.TrackingNumber, "url": order.TrackingURL,
			"status": order.FulfillmentStatus, "estimatedDeliveryAt": order.EstimatedDeliveryAt, "deliveredAt": order.DeliveredAt,
		},
		CustomerReceivedAt: order.CustomerReceivedAt, ReturnRequest: returnRequest,
		CanConfirmReceipt: canConfirmReceipt(order), CanRequestReturn: canRequestReturn(order), CreatedAt: order.CreatedAt,
	}
}
