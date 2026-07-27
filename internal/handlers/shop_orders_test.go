package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeShopifyOrderAdmin struct {
	requestedReason string
	tagged          bool
}

func (f *fakeShopifyOrderAdmin) CreateOrder(context.Context, shopify.AdminOrderInput) (*shopify.AdminOrderResult, error) {
	return nil, nil
}
func (f *fakeShopifyOrderAdmin) FetchOrder(context.Context, string) (*shopify.AdminOrderSnapshot, error) {
	return &shopify.AdminOrderSnapshot{}, nil
}
func (f *fakeShopifyOrderAdmin) AddOrderTags(context.Context, string, []string) error {
	f.tagged = true
	return nil
}
func (f *fakeShopifyOrderAdmin) RequestReturn(_ context.Context, _, reason, _ string) (*shopify.AdminReturnResult, error) {
	f.requestedReason = reason
	return &shopify.AdminReturnResult{ID: "gid://shopify/Return/1", Name: "#R1", Status: "REQUESTED"}, nil
}

func newShopOrderTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ShopOrder{}, &models.ShopOrderItem{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func authorizedShopRequest(t *testing.T, method, path, userID string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	token, err := auth.GenerateToken(userID, userID+"@example.com", "Tester")
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestShopOrderReturnRequestIsOwnedConfirmedAndSynced(t *testing.T) {
	db := newShopOrderTestDB(t)
	receivedAt := time.Now().UTC()
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_1",
		ShopifyOrderID: "gid://shopify/Order/1", Status: "received",
		FinancialStatus: "paid", FulfillmentStatus: "DELIVERED", Currency: "HKD",
		TotalAmountMinor: 1000, CustomerReceivedAt: &receivedAt,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &fakeShopifyOrderAdmin{}
	req := authorizedShopRequest(t, http.MethodPost, "/api/shop/orders/"+order.ID+"/return-request", "user-1", map[string]any{
		"reason": "DEFECTIVE", "note": "broken", "confirmed": true,
	})
	req.SetPathValue("orderID", order.ID)
	rec := httptest.NewRecorder()
	NewShopOrderReturnHandler(db, admin).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if admin.requestedReason != "DEFECTIVE" {
		t.Fatalf("reason was not sent to Shopify: %q", admin.requestedReason)
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.ReturnStatus != "REQUESTED" || updated.Status != "return_requested" {
		t.Fatalf("return state not persisted: %+v", updated)
	}
}

func TestShopOrderCannotBeReadByAnotherUser(t *testing.T) {
	db := newShopOrderTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "owner", PaymentIntentID: "pi_private",
		Status: "processing", Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	req := authorizedShopRequest(t, http.MethodGet, "/api/shop/orders/"+order.ID, "other", nil)
	req.SetPathValue("orderID", order.ID)
	rec := httptest.NewRecorder()
	NewShopOrderDetailHandler(db, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestShopOrderReceiptSyncsTagWithoutFakingDelivery(t *testing.T) {
	db := newShopOrderTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_2",
		ShopifyOrderID: "gid://shopify/Order/2", Status: "shipped",
		FulfillmentStatus: "IN_TRANSIT", Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &fakeShopifyOrderAdmin{}
	req := authorizedShopRequest(t, http.MethodPost, "/api/shop/orders/"+order.ID+"/received", "user-1", nil)
	req.SetPathValue("orderID", order.ID)
	rec := httptest.NewRecorder()
	NewShopOrderReceivedHandler(db, admin).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !admin.tagged {
		t.Fatalf("receipt confirmation failed: code=%d tagged=%v body=%s", rec.Code, admin.tagged, rec.Body.String())
	}
	var updated models.ShopOrder
	_ = db.First(&updated, "id = ?", order.ID).Error
	if updated.CustomerReceivedAt == nil || updated.FulfillmentStatus != "IN_TRANSIT" {
		t.Fatalf("receipt should not overwrite carrier state: %+v", updated)
	}
}
