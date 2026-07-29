package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeAdminFulfillmentRequester struct {
	orderID string
	result  *shopify.AdminFulfillmentRequestResult
	err     error
}

func (f *fakeAdminFulfillmentRequester) RequestOrderFulfillment(_ context.Context, orderID string) (*shopify.AdminFulfillmentRequestResult, error) {
	f.orderID = orderID
	return f.result, f.err
}

func TestShopOrderFulfillmentRequestRequiresAdminAndUsesShopifyOrder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ShopOrder{}, &models.ShopRefund{}); err != nil {
		t.Fatal(err)
	}
	shopifyOrderID := "gid://shopify/Order/42"
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: uuid.NewString(), PaymentIntentID: shopOrderStringPointer("pi_request_dsers"),
		ShopifyOrderID: &shopifyOrderID, Status: "processing", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	requester := &fakeAdminFulfillmentRequester{result: &shopify.AdminFulfillmentRequestResult{
		Requested: []shopify.AdminFulfillmentRequestItem{{
			FulfillmentOrderID: "gid://shopify/FulfillmentOrder/1",
			RequestStatus:      "SUBMITTED",
		}},
	}}
	handler := NewShopOrderFulfillmentRequestHandler(db, requester, "admin-secret")

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/admin/shop/orders/"+order.ID+"/request-fulfillment", nil)
	unauthorized.SetPathValue("orderID", order.ID)
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d, want 401", unauthorizedRecorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/shop/orders/"+order.ID+"/request-fulfillment", nil)
	request.SetPathValue("orderID", order.ID)
	request.Header.Set("X-Shop-Admin-Key", "admin-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if requester.orderID != shopifyOrderID {
		t.Fatalf("requested Shopify order=%q, want %q", requester.orderID, shopifyOrderID)
	}
	var body struct {
		OrderID string `json:"orderId"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.OrderID != order.ID {
		t.Fatalf("response order=%q, want %q", body.OrderID, order.ID)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FulfillmentRequestStatus != "submitted" {
		t.Fatalf("fulfillment request status=%q, want submitted", order.FulfillmentRequestStatus)
	}
}
