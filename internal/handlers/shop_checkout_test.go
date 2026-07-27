package handlers

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPendingCheckoutOrdersAllowMissingShopifyOrderID(t *testing.T) {
	db := newCheckoutPersistenceTestDB(t, true)

	for index, paymentIntentID := range []string{"pi_pending_1", "pi_pending_2"} {
		order := pendingCheckoutOrder(paymentIntentID)
		if err := persistCheckoutOrder(db, &order, nil); err != nil {
			t.Fatalf("persist pending order %d: %v", index+1, err)
		}
	}

	var orders []models.ShopOrder
	if err := db.Order("payment_intent_id").Find(&orders).Error; err != nil {
		t.Fatal(err)
	}
	if len(orders) != 2 {
		t.Fatalf("expected 2 pending orders, got %d", len(orders))
	}
	for _, order := range orders {
		if order.ShopifyOrderID != nil {
			t.Fatalf("pending order %s should have a NULL Shopify order ID, got %q", order.ID, order.ShopifyOrderGID())
		}
	}
}

func TestPersistCheckoutOrderCancelsIntentOnDatabaseFailure(t *testing.T) {
	db := newCheckoutPersistenceTestDB(t, false)
	order := pendingCheckoutOrder("pi_orphan")
	var canceledPaymentIntentID string

	err := persistCheckoutOrder(db, &order, func(paymentIntentID string) error {
		canceledPaymentIntentID = paymentIntentID
		return nil
	})

	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("expected missing table error, got %v", err)
	}
	if canceledPaymentIntentID != order.PaymentIntentID {
		t.Fatalf("expected %s to be canceled, got %s", order.PaymentIntentID, canceledPaymentIntentID)
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

func pendingCheckoutOrder(paymentIntentID string) models.ShopOrder {
	return models.ShopOrder{
		ID:                   uuid.NewString(),
		UserID:               "checkout-user",
		PaymentIntentID:      paymentIntentID,
		Status:               "pending_payment",
		FinancialStatus:      "pending",
		Currency:             "HKD",
		TotalAmountMinor:     2502,
		ShopifyOrderID:       nil,
		ShopifyOrderLegacyID: "",
	}
}
