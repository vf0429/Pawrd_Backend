package models

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNormalizePendingShopOrderIDsConvertsLegacyEmptyValueToNull(t *testing.T) {
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&ShopOrder{}); err != nil {
		t.Fatal(err)
	}

	legacyEmptyID := ""
	legacyEmptyIntentID := ""
	order := ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: &legacyEmptyIntentID,
		ShopifyOrderID: &legacyEmptyID, Status: "pending_payment",
		Currency: "HKD", TotalAmountMinor: 2502,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := normalizePendingShopOrderIDs(db); err != nil {
		t.Fatal(err)
	}

	var normalized ShopOrder
	if err := db.First(&normalized, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if normalized.ShopifyOrderID != nil {
		t.Fatalf("expected legacy empty Shopify order ID to become NULL, got %q", normalized.ShopifyOrderGID())
	}
	if normalized.PaymentIntentID != nil {
		t.Fatalf("expected legacy empty payment intent ID to become NULL, got %q", normalized.PaymentIntentIDValue())
	}

	// Rows with a real intent id must be untouched.
	realIntentID := "pi_real"
	realOrder := ShopOrder{
		ID: uuid.NewString(), UserID: "user-2", PaymentIntentID: &realIntentID,
		Status: "pending_payment", Currency: "HKD", TotalAmountMinor: 100,
	}
	if err := db.Create(&realOrder).Error; err != nil {
		t.Fatal(err)
	}
	if err := normalizePendingShopOrderIDs(db); err != nil {
		t.Fatal(err)
	}
	var reloaded ShopOrder
	if err := db.First(&reloaded, "id = ?", realOrder.ID).Error; err != nil {
		t.Fatal(err)
	}
	if reloaded.PaymentIntentIDValue() != "pi_real" {
		t.Fatalf("real payment intent ID must be preserved, got %q", reloaded.PaymentIntentIDValue())
	}
}
