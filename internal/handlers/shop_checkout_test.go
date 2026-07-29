package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestPendingCheckoutOrdersCoexistWithoutPaymentIntent verifies the nullable
// payment_intent_id: any number of pre-intent orders coexist despite the
// unique index (SQLite + Postgres both treat NULLs as distinct).
func TestPendingCheckoutOrdersCoexistWithoutPaymentIntent(t *testing.T) {
	db := newCheckoutPersistenceTestDB(t, true)

	for index := range []string{"order-a", "order-b", "order-c"} {
		order := models.ShopOrder{
			ID:               uuid.NewString(),
			UserID:           "checkout-user",
			PaymentIntentID:  nil,
			Status:           "pending_payment",
			FinancialStatus:  "pending",
			Currency:         "HKD",
			TotalAmountMinor: int64(1000 + index),
		}
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("persist pending order %d: %v", index+1, err)
		}
	}

	var count int64
	if err := db.Model(&models.ShopOrder{}).Where("payment_intent_id IS NULL").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 orders with NULL payment_intent_id, got %d", count)
	}
}

// TestCheckoutOrderRowExistsBeforeStripeCall asserts at the Stripe seam that
// the durable order (with the immutable shipping snapshot) is already
// persisted when CreatePaymentIntent runs.
func TestCheckoutOrderRowExistsBeforeStripeCall(t *testing.T) {
	db, cfg, fake := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "order-first@example.com", phoneNotSetPrefix+"q", "Order First")

	var foundAtStripeTime *models.ShopOrder
	fake.onCreate = func(req payments.CreatePaymentIntentRequest) {
		var order models.ShopOrder
		if err := db.Preload("Items").First(&order, "id = ?", req.Metadata["pawrd_order_id"]).Error; err == nil {
			foundAtStripeTime = &order
		}
	}

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if foundAtStripeTime == nil {
		t.Fatalf("order must exist before the Stripe call")
	}
	if foundAtStripeTime.Status != "pending_payment" {
		t.Fatalf("expected pending_payment at Stripe time, got %q", foundAtStripeTime.Status)
	}
	if foundAtStripeTime.PaymentIntentID != nil {
		t.Fatalf("payment_intent_id must be NULL before the Stripe call, got %v", *foundAtStripeTime.PaymentIntentID)
	}
	if foundAtStripeTime.CustomerName != "Chan Tai Man" || foundAtStripeTime.ShippingDistrict != "Wan Chai" ||
		foundAtStripeTime.ShippingRegion != "Hong Kong Island" || foundAtStripeTime.ShippingAddress1 != "Flat A, 1 Harbour Road" {
		t.Fatalf("shipping snapshot must be complete before the Stripe call: %+v", foundAtStripeTime)
	}
	if len(foundAtStripeTime.Items) != 1 {
		t.Fatalf("order items must be persisted before the Stripe call: %+v", foundAtStripeTime.Items)
	}
}

// TestCheckoutStripeFailureMarksOrderPaymentFailed: when Stripe intent
// creation fails, the order stays as the durable record of the attempt.
func TestCheckoutStripeFailureMarksOrderPaymentFailed(t *testing.T) {
	db, cfg, fake := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "stripe-fail@example.com", phoneNotSetPrefix+"f", "Stripe Fail")
	fake.failWith = errFakeStripeDown

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}

	var order models.ShopOrder
	if err := db.First(&order, "user_id <> ?", "").Error; err != nil {
		t.Fatalf("durable order must exist after Stripe failure: %v", err)
	}
	if order.Status != "payment_failed" {
		t.Fatalf("expected payment_failed, got %q", order.Status)
	}
	if order.FinancialStatus != "failed" {
		t.Fatalf("expected financial_status failed, got %q", order.FinancialStatus)
	}
	if order.PaymentIntentID != nil {
		t.Fatalf("payment_intent_id must stay NULL, got %v", *order.PaymentIntentID)
	}
	if !strings.Contains(order.FailureReason, "stripe payment intent creation failed") {
		t.Fatalf("expected failure reason recorded, got %q", order.FailureReason)
	}
	// Shipping snapshot intact on the failed order.
	if order.CustomerName != "Chan Tai Man" || order.ShippingDistrict != "Wan Chai" {
		t.Fatalf("shipping snapshot missing on failed order: %+v", order)
	}
}

// TestCheckoutBackfillFailureStillResponds: if the post-intent UPDATE fails,
// the checkout still returns the payment sheet (the intent exists; the webhook
// reconciles via pawrd_order_id metadata). Failure injected with a SQLite
// trigger that aborts any update of payment_intent_id.
func TestCheckoutBackfillFailureStillResponds(t *testing.T) {
	db, cfg, fake := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "backfill@example.com", phoneNotSetPrefix+"b", "Backfill")

	if err := db.Exec(`CREATE TRIGGER fail_backfill BEFORE UPDATE OF payment_intent_id ON shop_orders
		BEGIN SELECT RAISE(ABORT, 'injected backfill failure'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (reconcilable via metadata), got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp ShopPaymentSheetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var order models.ShopOrder
	if err := db.First(&order, "id = ?", resp.OrderID).Error; err != nil {
		t.Fatalf("order must exist: %v", err)
	}
	if order.PaymentIntentID != nil {
		t.Fatalf("back-fill must have failed (injected), got %v", *order.PaymentIntentID)
	}
	// The intent carries the order id, so reconciliation is possible.
	if fake.lastRequest.Metadata["pawrd_order_id"] != resp.OrderID {
		t.Fatalf("expected pawrd_order_id=%s in intent metadata, got %q", resp.OrderID, fake.lastRequest.Metadata["pawrd_order_id"])
	}
}

// TestCheckoutStripeConfigFailureLeavesNoOrder: if the Stripe service cannot
// even be constructed/validated, no payment attempt was ever possible — the
// client gets a 500 and NO order row is persisted.
func TestCheckoutStripeConfigFailureLeavesNoOrder(t *testing.T) {
	db, cfg, _ := setupCheckoutHandlerTest(t)
	_, token := seedCheckoutUser(t, db, "cfg-fail@example.com", phoneNotSetPrefix+"c", "Cfg Fail")

	newPaymentIntentService = func(*config.Config) (paymentIntentService, error) {
		return nil, errFakeStripeDown
	}

	rec := postCheckout(t, cfg, db, token, validCheckoutBody())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}

	var orderCount int64
	if err := db.Model(&models.ShopOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if orderCount != 0 {
		t.Fatalf("config failure must not leave an order row, got %d", orderCount)
	}
}

func newCheckoutPersistenceTestDB(t *testing.T, migrate bool) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if migrate {
		if err := db.AutoMigrate(&models.ShopOrder{}, &models.ShopOrderItem{}); err != nil {
			t.Fatal(err)
		}
	}
	return db
}
