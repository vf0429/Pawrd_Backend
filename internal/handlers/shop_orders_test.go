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
	snapshot        *shopify.AdminOrderSnapshot
	afterTag        func()
	afterReturn     func()
}

func (f *fakeShopifyOrderAdmin) CreateOrder(context.Context, shopify.AdminOrderInput) (*shopify.AdminOrderResult, error) {
	return nil, nil
}
func (f *fakeShopifyOrderAdmin) FetchOrder(context.Context, string) (*shopify.AdminOrderSnapshot, error) {
	if f.snapshot != nil {
		return f.snapshot, nil
	}
	return &shopify.AdminOrderSnapshot{}, nil
}
func (f *fakeShopifyOrderAdmin) AddOrderTags(context.Context, string, []string) error {
	f.tagged = true
	if f.afterTag != nil {
		f.afterTag()
	}
	return nil
}
func (f *fakeShopifyOrderAdmin) RequestReturn(_ context.Context, _, reason, _ string) (*shopify.AdminReturnResult, error) {
	f.requestedReason = reason
	if f.afterReturn != nil {
		f.afterReturn()
	}
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
	t.Setenv("JWT_SECRET", "test-only-jwt-secret-at-least-32-characters")
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
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_1"),
		ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/1"), Status: "received",
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

func TestUnfulfilledShopOrderCreatesCancellationRequestInsteadOfInvalidReturn(t *testing.T) {
	db := newShopOrderTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_cancel_1"),
		ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/cancel-1"),
		Status:         "processing", FinancialStatus: "paid", FulfillmentStatus: "UNFULFILLED",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &fakeShopifyOrderAdmin{}
	req := authorizedShopRequest(
		t,
		http.MethodPost,
		"/api/shop/orders/"+order.ID+"/return-request",
		"user-1",
		map[string]any{"reason": "UNWANTED", "note": "Please cancel", "confirmed": true},
	)
	req.SetPathValue("orderID", order.ID)
	rec := httptest.NewRecorder()

	NewShopOrderReturnHandler(db, admin).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !admin.tagged {
		t.Fatal("unfulfilled cancellation request was not synced to Shopify")
	}
	if admin.requestedReason != "" {
		t.Fatalf("unfulfilled order must not call Shopify returnRequest: %q", admin.requestedReason)
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "cancellation_requested" ||
		updated.ReturnStatus != "CANCELLATION_REQUESTED" ||
		updated.ReturnReason != "UNWANTED" {
		t.Fatalf("cancellation request state not persisted: %+v", updated)
	}
	dto := makeShopOrderDTO(updated)
	if dto.CanRequestReturn || dto.CanRequestCancellation {
		t.Fatalf("a submitted cancellation must disable duplicate customer requests: %+v", dto)
	}
}

func TestUnfulfilledShopOrderAdvertisesCancellationNotReturn(t *testing.T) {
	order := models.ShopOrder{
		ID:     uuid.NewString(),
		UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_cancel_eligibility"),
		ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/cancel-eligibility"),
		Status:         "processing", FinancialStatus: "paid", FulfillmentStatus: "UNFULFILLED",
		Currency: "HKD", TotalAmountMinor: 1000,
	}

	dto := makeShopOrderDTO(order)

	if dto.CanRequestReturn {
		t.Fatal("an unfulfilled order cannot use Shopify returnRequest")
	}
	if !dto.CanRequestCancellation {
		t.Fatal("an unfulfilled paid order should allow a cancellation/refund request")
	}
}

func TestCancellationRequestDoesNotHideConcurrentShipment(t *testing.T) {
	db := newShopOrderTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_cancel_race"),
		ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/cancel-race"),
		Status:         "processing", FinancialStatus: "paid", FulfillmentStatus: "UNFULFILLED",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &fakeShopifyOrderAdmin{afterTag: func() {
		if err := db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).
			Updates(map[string]any{
				"status": "shipped", "fulfillment_status": "IN_TRANSIT",
				"tracking_number": "TRACK-CANCEL-RACE",
			}).Error; err != nil {
			t.Fatalf("commit concurrent shipment: %v", err)
		}
	}}
	req := authorizedShopRequest(
		t,
		http.MethodPost,
		"/api/shop/orders/"+order.ID+"/return-request",
		"user-1",
		map[string]any{"reason": "UNWANTED", "confirmed": true},
	)
	req.SetPathValue("orderID", order.ID)
	rec := httptest.NewRecorder()

	NewShopOrderReturnHandler(db, admin).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Status != "shipped" ||
		updated.FulfillmentStatus != "IN_TRANSIT" ||
		updated.ReturnStatus != "CANCELLATION_REQUESTED" {
		t.Fatalf("cancellation request hid shipment or lost the request: %+v", updated)
	}
}

func TestShopOrderCannotBeReadByAnotherUser(t *testing.T) {
	db := newShopOrderTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "owner", PaymentIntentID: shopOrderStringPointer("pi_private"),
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
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_2"),
		ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/2"), Status: "shipped",
		FinancialStatus: "paid", FulfillmentStatus: "IN_TRANSIT",
		Currency: "HKD", TotalAmountMinor: 1000,
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

func TestShopOrderCustomerActionsFailClosedForMoneyAndReconciliationStates(t *testing.T) {
	tests := []struct {
		status          string
		financialStatus string
		disputeStatus   string
	}{
		{status: "refunded", financialStatus: "refunded"},
		{status: "payment_disputed", financialStatus: "disputed", disputeStatus: "needs_response"},
		{status: "payment_dispute_lost", financialStatus: "dispute_lost", disputeStatus: "lost"},
		{status: "refund_reconciliation_required", financialStatus: "paid"},
		{status: "reconciliation_required", financialStatus: "paid"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			db := newShopOrderTestDB(t)
			deliveredAt := time.Now().UTC()
			order := models.ShopOrder{
				ID: uuid.NewString(), UserID: "user-1",
				PaymentIntentID:   shopOrderStringPointer("pi_" + test.status),
				ShopifyOrderID:    shopOrderStringPointer("gid://shopify/Order/" + test.status),
				Status:            test.status,
				FinancialStatus:   test.financialStatus,
				DisputeStatus:     test.disputeStatus,
				FulfillmentStatus: "DELIVERED",
				TrackingNumber:    "TRACK-" + test.status,
				DeliveredAt:       &deliveredAt,
				Currency:          "HKD",
				TotalAmountMinor:  1000,
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			admin := &fakeShopifyOrderAdmin{}

			receivedReq := authorizedShopRequest(
				t,
				http.MethodPost,
				"/api/shop/orders/"+order.ID+"/received",
				"user-1",
				nil,
			)
			receivedReq.SetPathValue("orderID", order.ID)
			receivedRec := httptest.NewRecorder()
			NewShopOrderReceivedHandler(db, admin).ServeHTTP(receivedRec, receivedReq)
			if receivedRec.Code != http.StatusConflict || admin.tagged {
				t.Fatalf(
					"receipt was not blocked: code=%d tagged=%t body=%s",
					receivedRec.Code,
					admin.tagged,
					receivedRec.Body.String(),
				)
			}

			returnReq := authorizedShopRequest(
				t,
				http.MethodPost,
				"/api/shop/orders/"+order.ID+"/return-request",
				"user-1",
				map[string]any{
					"reason": "DEFECTIVE", "confirmed": true,
				},
			)
			returnReq.SetPathValue("orderID", order.ID)
			returnRec := httptest.NewRecorder()
			NewShopOrderReturnHandler(db, admin).ServeHTTP(returnRec, returnReq)
			if returnRec.Code != http.StatusConflict || admin.requestedReason != "" {
				t.Fatalf(
					"return was not blocked: code=%d reason=%q body=%s",
					returnRec.Code,
					admin.requestedReason,
					returnRec.Body.String(),
				)
			}
		})
	}
}

func TestShopOrderDetailRefreshUpdatesTrackingWithoutRegressingMoneyStatus(t *testing.T) {
	tests := []struct {
		status          string
		financialStatus string
		disputeStatus   string
	}{
		{status: "refunded", financialStatus: "refunded"},
		{status: "payment_disputed", financialStatus: "disputed", disputeStatus: "needs_response"},
		{status: "refund_reconciliation_required", financialStatus: "paid"},
		{status: "reconciliation_required", financialStatus: "paid"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			db := newShopOrderTestDB(t)
			order := models.ShopOrder{
				ID: uuid.NewString(), UserID: "user-1",
				PaymentIntentID:   shopOrderStringPointer("pi_detail_" + test.status),
				ShopifyOrderID:    shopOrderStringPointer("gid://shopify/Order/detail-" + test.status),
				Status:            test.status,
				FinancialStatus:   test.financialStatus,
				DisputeStatus:     test.disputeStatus,
				FulfillmentStatus: "UNFULFILLED",
				Currency:          "HKD",
				TotalAmountMinor:  1000,
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			deliveredAt := time.Now().UTC()
			admin := &fakeShopifyOrderAdmin{snapshot: &shopify.AdminOrderSnapshot{
				FulfillmentStatus: "DELIVERED",
				TrackingCompany:   "Carrier",
				TrackingNumber:    "TRACK-TERMINAL",
				TrackingURL:       "https://carrier.example/track",
				DeliveredAt:       &deliveredAt,
			}}
			req := authorizedShopRequest(
				t,
				http.MethodGet,
				"/api/shop/orders/"+order.ID,
				"user-1",
				nil,
			)
			req.SetPathValue("orderID", order.ID)
			rec := httptest.NewRecorder()
			NewShopOrderDetailHandler(db, admin).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("detail response=%d body=%s", rec.Code, rec.Body.String())
			}
			var stored models.ShopOrder
			if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != test.status ||
				stored.TrackingNumber != "TRACK-TERMINAL" ||
				stored.FulfillmentStatus != "DELIVERED" {
				t.Fatalf("detail refresh regressed business state or lost tracking: %+v", stored)
			}
		})
	}
}

func TestShopOrderDetailRefreshReconcilesClosedShopifyReturn(t *testing.T) {
	db := newShopOrderTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_return_detail"),
		ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/return-detail"),
		Status:         "return_requested", FinancialStatus: "paid",
		FulfillmentStatus: "DELIVERED", ReturnID: "gid://shopify/Return/1",
		ReturnName: "#1001-R1", ReturnStatus: "REQUESTED",
		ReturnReason: "DEFECTIVE", Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	deliveredAt := time.Now().UTC()
	admin := &fakeShopifyOrderAdmin{snapshot: &shopify.AdminOrderSnapshot{
		FulfillmentStatus:  "DELIVERED",
		HasShippingAddress: true,
		DeliveredAt:        &deliveredAt,
		Return: &shopify.AdminReturnResult{
			ID: "gid://shopify/Return/1", Name: "#1001-R1", Status: "CLOSED",
		},
	}}
	req := authorizedShopRequest(
		t,
		http.MethodGet,
		"/api/shop/orders/"+order.ID,
		"user-1",
		nil,
	)
	req.SetPathValue("orderID", order.ID)
	rec := httptest.NewRecorder()

	NewShopOrderDetailHandler(db, admin).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("detail response=%d body=%s", rec.Code, rec.Body.String())
	}
	var stored models.ShopOrder
	if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "return_closed" ||
		stored.ReturnStatus != "CLOSED" ||
		stored.ReturnReason != "DEFECTIVE" {
		t.Fatalf("Shopify return state was not reconciled: %+v", stored)
	}
}

func TestShopOrderCustomerWritesDoNotOverwriteConcurrentMoneyTerminal(t *testing.T) {
	t.Run("receipt", func(t *testing.T) {
		db := newShopOrderTestDB(t)
		order := models.ShopOrder{
			ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_receipt_race"),
			ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/receipt-race"),
			Status:         "shipped", FinancialStatus: "paid", FulfillmentStatus: "IN_TRANSIT",
			TrackingNumber: "TRACK-RACE", Currency: "HKD", TotalAmountMinor: 1000,
		}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		admin := &fakeShopifyOrderAdmin{afterTag: func() {
			if err := db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).
				Updates(map[string]any{
					"status": "refunded", "financial_status": "refunded",
					"refunded_amount_minor": order.TotalAmountMinor,
				}).Error; err != nil {
				t.Fatalf("commit concurrent refund: %v", err)
			}
		}}
		req := authorizedShopRequest(
			t, http.MethodPost, "/api/shop/orders/"+order.ID+"/received", "user-1", nil,
		)
		req.SetPathValue("orderID", order.ID)
		rec := httptest.NewRecorder()
		NewShopOrderReceivedHandler(db, admin).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("receipt response=%d body=%s", rec.Code, rec.Body.String())
		}
		var stored models.ShopOrder
		if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != "refunded" || stored.FinancialStatus != "refunded" {
			t.Fatalf("receipt overwrote concurrent refund: %+v", stored)
		}
	})

	t.Run("return", func(t *testing.T) {
		db := newShopOrderTestDB(t)
		receivedAt := time.Now().UTC()
		order := models.ShopOrder{
			ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: shopOrderStringPointer("pi_return_race"),
			ShopifyOrderID: shopOrderStringPointer("gid://shopify/Order/return-race"),
			Status:         "received", FinancialStatus: "paid", FulfillmentStatus: "DELIVERED",
			CustomerReceivedAt: &receivedAt, Currency: "HKD", TotalAmountMinor: 1000,
		}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		admin := &fakeShopifyOrderAdmin{afterReturn: func() {
			if err := db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).
				Updates(map[string]any{
					"status": "payment_disputed", "financial_status": "disputed",
					"dispute_status": "needs_response", "dispute_id": "dp_return_race",
				}).Error; err != nil {
				t.Fatalf("commit concurrent dispute: %v", err)
			}
		}}
		req := authorizedShopRequest(
			t,
			http.MethodPost,
			"/api/shop/orders/"+order.ID+"/return-request",
			"user-1",
			map[string]any{"reason": "DEFECTIVE", "confirmed": true},
		)
		req.SetPathValue("orderID", order.ID)
		rec := httptest.NewRecorder()
		NewShopOrderReturnHandler(db, admin).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("return response=%d body=%s", rec.Code, rec.Body.String())
		}
		var stored models.ShopOrder
		if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != "payment_disputed" ||
			stored.FinancialStatus != "disputed" ||
			stored.ReturnStatus != "REQUESTED" {
			t.Fatalf("return overwrote concurrent dispute or lost external return: %+v", stored)
		}
	})
}

func shopOrderStringPointer(value string) *string {
	return &value
}
