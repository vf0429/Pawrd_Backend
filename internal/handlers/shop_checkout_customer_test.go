package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakePaymentIntentService stubs the Stripe boundary and captures the last
// CreatePaymentIntent request for assertions.
type fakePaymentIntentService struct {
	calls       int
	lastRequest payments.CreatePaymentIntentRequest
	onCreate    func(payments.CreatePaymentIntentRequest)
	failWith    error
}

var errFakeStripeDown = errors.New("fake stripe: service unavailable")

func (f *fakePaymentIntentService) CreatePaymentIntent(req payments.CreatePaymentIntentRequest) (*payments.CreatePaymentIntentResponse, error) {
	f.calls++
	f.lastRequest = req
	if f.onCreate != nil {
		f.onCreate(req)
	}
	if f.failWith != nil {
		return nil, f.failWith
	}
	return &payments.CreatePaymentIntentResponse{
		ClientSecret:    "pi_test_secret",
		PaymentIntentID: fmt.Sprintf("pi_test_%d", f.calls),
		PublishableKey:  "pk_test",
	}, nil
}

// setupCheckoutHandlerTest wires an in-memory DB (orders + auth users), the
// mock Shopify client, and the fake Stripe boundary.
func setupCheckoutHandlerTest(t *testing.T) (*gorm.DB, *config.Config, *fakePaymentIntentService) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.AuthUser{}, &models.ShopOrder{}, &models.ShopOrderItem{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	models.AuthDB = db

	cfg := &config.Config{UseMockShopify: true}

	fake := &fakePaymentIntentService{}
	previous := newPaymentIntentService
	newPaymentIntentService = func(*config.Config) (paymentIntentService, error) {
		return fake, nil
	}
	t.Cleanup(func() { newPaymentIntentService = previous })

	return db, cfg, fake
}

func seedCheckoutUser(t *testing.T, db *gorm.DB, email, phone, name string) (string, string) {
	t.Helper()
	user := models.AuthUser{Email: email, Phone: phone, PasswordHash: "x", Name: name}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userID := fmt.Sprintf("%d", user.ID)
	token, err := auth.GenerateToken(userID, user.Email, user.Name)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return userID, token
}

func validCheckoutBody() map[string]any {
	return map[string]any{
		"lineItems": []map[string]any{
			{"handle": "premium-grain-free-dog-food", "variantId": "gid://shopify/ProductVariant/200001", "quantity": 2},
		},
		"shipping": map[string]any{
			"recipientName": "Chan Tai Man",
			"phone":         "9123 4567",
			"address1":      "Flat A, 1 Harbour Road",
			"district":      "Wan Chai",
			"region":        "Hong Kong Island",
		},
	}
}

func postCheckout(t *testing.T, cfg *config.Config, db *gorm.DB, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/shop/checkout/payment-sheet", bytes.NewReader(payload))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	NewShopPaymentSheetHandler(cfg, db).ServeHTTP(rec, req)
	return rec
}

func TestCheckoutUsesAccountCustomerNotClientPayload(t *testing.T) {
	db, cfg, fake := setupCheckoutHandlerTest(t)
	userID, token := seedCheckoutUser(t, db, "real@example.com", "+85291234567", "Real Name")

	body := validCheckoutBody()
	// Bogus client-sent customer must be ignored entirely.
	body["customer"] = map[string]any{"name": "Forged Name", "email": "bogus@evil.example", "phone": "00000000"}

	rec := postCheckout(t, cfg, db, token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ShopPaymentSheetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var order models.ShopOrder
	if err := db.Preload("Items").First(&order, "id = ?", resp.OrderID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if order.UserID != userID {
		t.Fatalf("expected order owned by %s, got %s", userID, order.UserID)
	}
	if order.CustomerEmail != "real@example.com" {
		t.Fatalf("expected account email, got %q", order.CustomerEmail)
	}
	// Shipping snapshot = the per-order override, exactly as sent.
	if order.CustomerName != "Chan Tai Man" || order.CustomerPhone != "9123 4567" ||
		order.ShippingAddress1 != "Flat A, 1 Harbour Road" || order.ShippingDistrict != "Wan Chai" ||
		order.ShippingRegion != "Hong Kong Island" || order.ShippingCountry != "Hong Kong" {
		t.Fatalf("unexpected shipping snapshot: %+v", order)
	}
	if order.Status != "pending_payment" || order.PaymentIntentIDValue() != resp.PaymentIntentID {
		t.Fatalf("unexpected order state: %+v", order)
	}
	if order.TotalAmountMinor != 90000 || order.Currency != "HKD" {
		t.Fatalf("expected 90000 HKD minor units, got %d %s", order.TotalAmountMinor, order.Currency)
	}
	if len(order.Items) != 1 || order.Items[0].Quantity != 2 || order.Items[0].OrderID != order.ID {
		t.Fatalf("unexpected order items: %+v", order.Items)
	}

	// The outbound Stripe request uses the account email for receipts.
	if fake.calls != 1 {
		t.Fatalf("expected 1 payment intent call, got %d", fake.calls)
	}
	if fake.lastRequest.ReceiptEmail != "real@example.com" {
		t.Fatalf("expected receipt email from account, got %q", fake.lastRequest.ReceiptEmail)
	}

	// No client-forged data anywhere in the outbound payload.
	if strings.Contains(fmt.Sprintf("%v", fake.lastRequest.Metadata), "bogus") ||
		strings.Contains(fmt.Sprintf("%v", fake.lastRequest.Metadata), "Forged") {
		t.Fatalf("client customer data leaked into metadata: %v", fake.lastRequest.Metadata)
	}
}

func TestCheckoutPlaceholderPhoneNeverLeaks(t *testing.T) {
	db, cfg, fake := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "placeholder@example.com", phoneNotSetPrefix+"abc-123", "Placeholder User")

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ShopPaymentSheetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var order models.ShopOrder
	if err := db.First(&order, "id = ?", resp.OrderID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}

	// The placeholder must not appear in any persisted order field.
	orderJSON, _ := json.Marshal(order)
	if strings.Contains(string(orderJSON), "phone-not-set-") {
		t.Fatalf("placeholder phone leaked into order: %s", orderJSON)
	}
	// Nor in the outbound Stripe payload (metadata, description, receipt email).
	outbound, _ := json.Marshal(fake.lastRequest)
	if strings.Contains(string(outbound), "phone-not-set-") {
		t.Fatalf("placeholder phone leaked into Stripe payload: %s", outbound)
	}
	// CustomerPhone is the per-order shipping phone, not the account placeholder.
	if order.CustomerPhone != "9123 4567" {
		t.Fatalf("expected shipping phone in snapshot, got %q", order.CustomerPhone)
	}
}

func TestCheckoutShippingValidation(t *testing.T) {
	db, cfg, _ := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "ship@example.com", phoneNotSetPrefix+"x", "Ship User")

	mutate := func(mut func(map[string]any)) map[string]any {
		body := validCheckoutBody()
		mut(body)
		return body
	}
	setShipping := func(key string, value any) func(map[string]any) {
		return func(body map[string]any) {
			body["shipping"].(map[string]any)[key] = value
		}
	}

	cases := map[string]map[string]any{
		"7-digit phone":       mutate(setShipping("phone", "9123456")),
		"phone leading 1":     mutate(setShipping("phone", "11234567")),
		"phone with letters":  mutate(setShipping("phone", "9123abcd")),
		"empty address1":      mutate(setShipping("address1", "   ")),
		"empty recipientName": mutate(setShipping("recipientName", "")),
		"unknown region":      mutate(setShipping("region", "Lantau")),
		"unknown district":    mutate(setShipping("district", "Repulse Bay")),
		"district in wrong region": mutate(func(body map[string]any) {
			body["shipping"].(map[string]any)["district"] = "Kwun Tong" // Kowloon, not HK Island
		}),
		"oversized address1": mutate(setShipping("address1", strings.Repeat("a", 201))),
	}
	for name, body := range cases {
		rec := postCheckout(t, cfg, db, token, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d body=%s", name, rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) == "" {
			t.Fatalf("%s: expected a clear error message", name)
		}
	}

	var orderCount int64
	if err := db.Model(&models.ShopOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orderCount != 0 {
		t.Fatalf("expected no orders persisted for invalid shipping, got %d", orderCount)
	}
}

func TestCheckoutPaymentIntentMetadataMinimized(t *testing.T) {
	db, cfg, fake := setupCheckoutHandlerTest(t)
	userID, token := seedCheckoutUser(t, db, "meta@example.com", "+85269876543", "Meta User")

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	metadata := fake.lastRequest.Metadata
	// Operational keys only: item lines, totals, order linkage. No user_id —
	// the webhook resolves the user through the order row.
	for key := range metadata {
		if !strings.HasPrefix(key, "item_") && key != "total_items" && key != "pawrd_order_id" {
			t.Fatalf("unexpected metadata key %q", key)
		}
	}
	// No PII: no name, no phone, no email, no address fragments, no user id.
	joined := strings.ToLower(fmt.Sprintf("%v", metadata))
	for _, pii := range []string{"chan tai man", "9123", "harbour road", "wan chai", "meta@example.com", "customer_name", "customer_phone", "customer_email", "address", "user_id"} {
		if strings.Contains(joined, pii) {
			t.Fatalf("PII %q found in metadata: %v", pii, metadata)
		}
	}
	for key, value := range metadata {
		if value == userID {
			t.Fatalf("user id leaked as metadata value at key %q", key)
		}
	}
	if metadata["pawrd_order_id"] == "" {
		t.Fatalf("expected pawrd_order_id linkage")
	}

	var resp ShopPaymentSheetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var order models.ShopOrder
	if err := db.First(&order, "id = ?", metadata["pawrd_order_id"]).Error; err != nil {
		t.Fatalf("order linked by metadata id must exist: %v", err)
	}
	if order.PaymentIntentIDValue() == "" || order.PaymentIntentIDValue() != resp.PaymentIntentID {
		t.Fatalf("order must be linked to the payment intent, got %q", order.PaymentIntentIDValue())
	}
}

func TestCheckoutIgnoresCustomerFieldEntirelyWhenMissing(t *testing.T) {
	db, cfg, _ := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "nocust@example.com", phoneNotSetPrefix+"z", "No Customer")

	// No customer object at all — old validation would 400; now ignored.
	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without customer field, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCheckoutUnknownAccountReturns404(t *testing.T) {
	db, cfg, _ := setupCheckoutHandlerTest(t)
	token, err := auth.GenerateToken("987654", "ghost@example.com", "Ghost")
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCheckoutRequiresJWT(t *testing.T) {
	db, cfg, _ := setupCheckoutHandlerTest(t)

	rec := postCheckout(t, cfg, db, "", validCheckoutBody())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}
