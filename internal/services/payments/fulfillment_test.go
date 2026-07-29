package payments

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type capturingShopifyOrderAdmin struct {
	input        shopify.AdminOrderInput
	existing     *shopify.AdminOrderResult
	createResult *shopify.AdminOrderResult
	createErr    error
	createCalls  int
	lookupCalls  int
	beforeCreate func()
}

func (c *capturingShopifyOrderAdmin) CreateOrder(_ context.Context, input shopify.AdminOrderInput) (*shopify.AdminOrderResult, error) {
	c.input = input
	c.createCalls++
	if c.beforeCreate != nil {
		c.beforeCreate()
	}
	if c.createErr != nil {
		return nil, c.createErr
	}
	if c.createResult != nil {
		return c.createResult, nil
	}
	return &shopify.AdminOrderResult{
		ID:          "gid://shopify/Order/1",
		LegacyID:    "1",
		Name:        "#1001",
		TotalAmount: input.Amount,
		Currency:    input.Currency,
		LineItemIDs: []string{"gid://shopify/LineItem/1"},
	}, nil
}

func TestDispatcher_DeterministicOrderRejectionConfirmsNoMapping(t *testing.T) {
	db := newFulfillmentTestDB(t)
	order := models.ShopOrder{
		ID: "order-rejected", UserID: "user-1",
		PaymentIntentID: testStringPointer("pi_rejected"), Status: "fulfillment_pending",
		FinancialStatus: "paid", Currency: "HKD", TotalAmountMinor: 1000,
		Items: []models.ShopOrderItem{{
			ID: "item-rejected", OrderID: "order-rejected", Source: "shopify",
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			UnitAmountMinor: 1000, Currency: "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &capturingShopifyOrderAdmin{
		createErr: errors.Join(
			shopify.ErrOrderCreateRejected,
			errors.New("inventory is no longer available"),
		),
	}
	err := NewOrderDispatcher(db, admin).Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: order.Items[0].VariantID, Quantity: 1,
		}},
	})
	if !errors.Is(err, ErrFulfillmentOrderDefinitelyRejected) {
		t.Fatalf("deterministic rejection was not classified: %v", err)
	}
	if admin.lookupCalls != 2 || admin.createCalls != 1 {
		t.Fatalf(
			"rejection must query before and after create: lookup=%d create=%d",
			admin.lookupCalls,
			admin.createCalls,
		)
	}
}

func (c *capturingShopifyOrderAdmin) FindOrderBySourceIdentifier(context.Context, string) (*shopify.AdminOrderResult, error) {
	c.lookupCalls++
	return c.existing, nil
}

func (c *capturingShopifyOrderAdmin) FetchOrder(context.Context, string) (*shopify.AdminOrderSnapshot, error) {
	return &shopify.AdminOrderSnapshot{}, nil
}

func (c *capturingShopifyOrderAdmin) AddOrderTags(context.Context, string, []string) error {
	return nil
}

func (c *capturingShopifyOrderAdmin) RequestReturn(context.Context, string, string, string) (*shopify.AdminReturnResult, error) {
	return &shopify.AdminReturnResult{}, nil
}

func newFulfillmentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.ShopOrder{},
		&models.ShopOrderItem{},
		&models.ShopCheckoutQuote{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestParseItemsFromMetadata_NewFormatShopify(t *testing.T) {
	meta := map[string]string{
		"item_1":        "source=shopify | handle=dog-hoodie | variant=gid://123 | qty:2",
		"customer_name": "Alice",
		"total_items":   "2",
	}
	items := ParseItemsFromMetadata(meta)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Source != SourceShopify {
		t.Errorf("source = %q, want shopify", it.Source)
	}
	if it.Handle != "dog-hoodie" {
		t.Errorf("handle = %q", it.Handle)
	}
	if it.VariantID != "gid://123" {
		t.Errorf("variant = %q", it.VariantID)
	}
	if it.Quantity != 2 {
		t.Errorf("qty = %d", it.Quantity)
	}
}

func TestParseItemsFromMetadata_NewFormatHiCustom(t *testing.T) {
	meta := map[string]string{
		"item_2": "source=hicustom | customProductId=cp_88 | sku=BLANK-009 | qty:1",
	}
	items := ParseItemsFromMetadata(meta)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Source != SourceHiCustom {
		t.Errorf("source = %q, want hicustom", it.Source)
	}
	if it.CustomProductID != "cp_88" {
		t.Errorf("customProductId = %q", it.CustomProductID)
	}
	if it.SKU != "BLANK-009" {
		t.Errorf("sku = %q", it.SKU)
	}
	if it.Quantity != 1 {
		t.Errorf("qty = %d", it.Quantity)
	}
}

func TestParseItemsFromMetadata_LegacyFormatDefaultsShopify(t *testing.T) {
	meta := map[string]string{
		"item_1": "dog-hoodie | gid://123 | qty:3",
	}
	items := ParseItemsFromMetadata(meta)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Source != SourceShopify {
		t.Errorf("legacy should default to shopify, got %q", it.Source)
	}
	if it.Handle != "dog-hoodie" || it.VariantID != "gid://123" || it.Quantity != 3 {
		t.Errorf("parsed legacy item = %+v", it)
	}
}

func TestParseItemsFromMetadata_MultipleAndSkipsNonItemKeys(t *testing.T) {
	meta := map[string]string{
		"item_1":         "source=shopify | handle=a | variant=v1 | qty:1",
		"item_2":         "source=hicustom | customProductId=c | sku=s | qty:4",
		"customer_phone": "555",
		"total_items":    "5",
	}
	items := ParseItemsFromMetadata(meta)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestParseItemsFromMetadata_MissingQtyDefaultsOne(t *testing.T) {
	meta := map[string]string{
		"item_1": "source=shopify | handle=a | variant=v1",
	}
	items := ParseItemsFromMetadata(meta)
	if len(items) != 1 || items[0].Quantity != 1 {
		t.Fatalf("expected qty default 1, got %+v", items)
	}
}

func TestDispatcher_FulfillRoutesBySource(t *testing.T) {
	d := NewDispatcher()
	req := FulfillmentRequest{
		PaymentIntentID: "pi_test",
		CustomerEmail:   "alice@example.com",
		Items: []FulfillmentItem{
			{Source: SourceShopify, Handle: "h", VariantID: "v", Quantity: 1},
			{Source: SourceHiCustom, CustomProductID: "cp", SKU: "s", Quantity: 1},
		},
	}
	if err := d.Fulfill(req); err != nil {
		t.Fatalf("fulfill returned error: %v", err)
	}
}

func TestDispatcher_FulfillNoItemsErrors(t *testing.T) {
	d := NewDispatcher()
	if err := d.Fulfill(FulfillmentRequest{PaymentIntentID: "pi_test"}); err == nil {
		t.Fatal("expected error for empty items, got nil")
	}
}

func TestDispatcher_ShopifyPhysicalItemsRequireShipping(t *testing.T) {
	db := newFulfillmentTestDB(t)
	order := models.ShopOrder{
		ID:               "order-1",
		UserID:           "user-1",
		PaymentIntentID:  testStringPointer("pi_physical"),
		Status:           "pending_payment",
		FinancialStatus:  "pending",
		Currency:         "HKD",
		TotalAmountMinor: 8500,
		CustomerName:     "Alice Test",
		CustomerEmail:    "alice@example.com",
		CustomerPhone:    "+85261234567",
		ShippingAddress1: "1 Test Street",
		ShippingDistrict: "Wan Chai",
		ShippingRegion:   "Hong Kong Island",
		ShippingCountry:  "Hong Kong",
		Items: []models.ShopOrderItem{{
			ID:              "item-1",
			OrderID:         "order-1",
			Source:          string(SourceShopify),
			Handle:          "physical-cat-bed",
			VariantID:       "gid://shopify/ProductVariant/1",
			Title:           "Physical Cat Bed",
			Quantity:        1,
			UnitAmountMinor: 8500,
			Currency:        "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	admin := &capturingShopifyOrderAdmin{}
	dispatcher := NewOrderDispatcher(db, admin)
	err := dispatcher.Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source:    SourceShopify,
			Handle:    "physical-cat-bed",
			VariantID: "gid://shopify/ProductVariant/1",
			Quantity:  1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(admin.input.Lines) != 1 {
		t.Fatalf("expected one Shopify line item, got %#v", admin.input.Lines)
	}
	if admin.lookupCalls != 1 || admin.createCalls != 1 {
		t.Fatalf("expected lookup before create, got lookup=%d create=%d", admin.lookupCalls, admin.createCalls)
	}
	if !admin.input.Lines[0].RequiresShipping {
		t.Fatalf("Shopify physical item must require shipping: %#v", admin.input.Lines[0])
	}
	if admin.input.Lines[0].UnitPrice != "85.00" {
		t.Fatalf("Shopify line must preserve stored price: %#v", admin.input.Lines[0])
	}
	if admin.input.ShippingAddress != "1 Test Street" ||
		admin.input.ShippingCity != "Wan Chai" ||
		admin.input.ShippingRegion != "Hong Kong Island" {
		t.Fatalf("unexpected Shopify shipping address: %#v", admin.input)
	}
}

func TestDispatcher_RecoversShopifyOrderBySourceIdentifierBeforeCreate(t *testing.T) {
	db := newFulfillmentTestDB(t)
	order := models.ShopOrder{
		ID: "order-recover", UserID: "user-1", PaymentIntentID: testStringPointer("pi_recover"),
		Status: "fulfillment_retrying", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
		Items: []models.ShopOrderItem{{
			ID: "item-recover", OrderID: "order-recover", Source: "shopify",
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			UnitAmountMinor: 1000, Currency: "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &capturingShopifyOrderAdmin{existing: &shopify.AdminOrderResult{
		ID: "gid://shopify/Order/77", LegacyID: "77", Name: "#1077",
		TotalAmount: "10.00", Currency: "HKD",
		LineItemIDs: []string{"gid://shopify/LineItem/77"},
	}}
	dispatchMarkerCalls := 0
	if err := NewOrderDispatcher(db, admin).Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		BeforeExternalDispatch: func() error {
			dispatchMarkerCalls++
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if admin.lookupCalls != 1 || admin.createCalls != 0 || dispatchMarkerCalls != 0 {
		t.Fatalf(
			"recovery must not mark or create a duplicate: lookup=%d marker=%d create=%d",
			admin.lookupCalls,
			dispatchMarkerCalls,
			admin.createCalls,
		)
	}
	var stored models.ShopOrder
	if err := db.Preload("Items").First(&stored, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ShopifyOrderGID() != "gid://shopify/Order/77" ||
		stored.Items[0].ShopifyLineItemID != "gid://shopify/LineItem/77" {
		t.Fatalf("recovered Shopify mapping was not persisted: %+v item=%+v", stored, stored.Items[0])
	}
}

func TestDispatcher_ValidatesCheckoutQuoteBeforeDispatchMarker(t *testing.T) {
	db := newFulfillmentTestDB(t)
	order := models.ShopOrder{
		ID: "order-invalid-quote", UserID: "user-1",
		PaymentIntentID: testStringPointer("pi_invalid_quote"), Status: "fulfillment_pending",
		FinancialStatus: "paid", Currency: "HKD", TotalAmountMinor: 1000,
		Items: []models.ShopOrderItem{{
			ID: "item-invalid-quote", OrderID: "order-invalid-quote", Source: "shopify",
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			UnitAmountMinor: 1000, Currency: "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	quote := models.ShopCheckoutQuote{
		ID: "quote-invalid", UserID: order.UserID, ShopifyCartID: "cart-invalid",
		Status: models.ShopQuoteStatusReady, Currency: "HKD",
		SubtotalAmountMinor: 1000, TotalAmountMinor: 1000,
		SnapshotJSON: "{}", SnapshotSHA256: strings.Repeat("0", 64),
		ExpiresAt: time.Now().UTC().Add(time.Hour), OrderID: order.ID,
		PaymentIntentID: order.PaymentIntentIDValue(),
	}
	if err := db.Create(&quote).Error; err != nil {
		t.Fatal(err)
	}

	admin := &capturingShopifyOrderAdmin{}
	dispatchMarkerCalls := 0
	err := NewOrderDispatcher(db, admin).Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: order.Items[0].VariantID, Quantity: 1,
		}},
		BeforeExternalDispatch: func() error {
			dispatchMarkerCalls++
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("expected quote validation failure, got %v", err)
	}
	if dispatchMarkerCalls != 0 || admin.createCalls != 0 {
		t.Fatalf(
			"invalid quote reached external dispatch: marker=%d create=%d",
			dispatchMarkerCalls,
			admin.createCalls,
		)
	}
}

func TestDispatcher_RejectsShopifyOrderTotalMismatchBeforePersistingMapping(t *testing.T) {
	db := newFulfillmentTestDB(t)
	order := models.ShopOrder{
		ID: "order-total-mismatch", UserID: "user-1", PaymentIntentID: testStringPointer("pi_total_mismatch"),
		Status: "fulfillment_pending", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
		Items: []models.ShopOrderItem{{
			ID: "item-total-mismatch", OrderID: "order-total-mismatch", Source: "shopify",
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			UnitAmountMinor: 1000, Currency: "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &capturingShopifyOrderAdmin{createResult: &shopify.AdminOrderResult{
		ID: "gid://shopify/Order/88", LegacyID: "88", Name: "#1088",
		TotalAmount: "9.99", Currency: "HKD",
		LineItemIDs: []string{"gid://shopify/LineItem/88"},
	}}
	err := NewOrderDispatcher(db, admin).Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: order.Items[0].VariantID, Quantity: 1,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "total mismatch") {
		t.Fatalf("expected Shopify total mismatch, got %v", err)
	}
	var stored models.ShopOrder
	if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ShopifyOrderGID() != "gid://shopify/Order/88" ||
		!strings.Contains(stored.FailureReason, shopifyReconciliationFailurePrefix) {
		t.Fatalf("mismatched remote order was not preserved for reconciliation: %+v", stored)
	}
	secondErr := NewOrderDispatcher(db, admin).Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: order.Items[0].VariantID, Quantity: 1,
		}},
	})
	if secondErr == nil || admin.createCalls != 1 {
		t.Fatalf("reconciliation retry must not create another Shopify order: err=%v calls=%d", secondErr, admin.createCalls)
	}
}

func TestDispatcher_DoesNotOverwriteRefundOrDisputeFinancialStatus(t *testing.T) {
	for _, status := range protectedFinancialStatuses {
		t.Run(status, func(t *testing.T) {
			db := newFulfillmentTestDB(t)
			orderID := "order-" + status
			order := models.ShopOrder{
				ID: orderID, UserID: "user-1", PaymentIntentID: testStringPointer("pi_" + status),
				Status: "fulfillment_pending", FinancialStatus: status,
				Currency: "HKD", TotalAmountMinor: 1000,
				Items: []models.ShopOrderItem{{
					ID: "item-" + status, OrderID: orderID, Source: "shopify",
					VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
					UnitAmountMinor: 1000, Currency: "HKD",
				}},
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			if err := NewOrderDispatcher(db, &capturingShopifyOrderAdmin{}).Fulfill(
				FulfillmentRequest{
					PaymentIntentID: order.PaymentIntentIDValue(),
					Items: []FulfillmentItem{{
						Source: SourceShopify, VariantID: order.Items[0].VariantID, Quantity: 1,
					}},
				},
			); err != nil {
				t.Fatal(err)
			}
			var stored models.ShopOrder
			if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.FinancialStatus != status {
				t.Fatalf("financial status changed from %q to %q", status, stored.FinancialStatus)
			}
		})
	}
}

func TestDispatcher_PreservesDisputeThatArrivesDuringShopifyOrderCreate(t *testing.T) {
	db := newFulfillmentTestDB(t)
	order := models.ShopOrder{
		ID: "order-dispute-race", UserID: "user-1",
		PaymentIntentID: testStringPointer("pi_dispute_race"), Status: "fulfillment_pending",
		FinancialStatus: "paid", Currency: "HKD", TotalAmountMinor: 1000,
		Items: []models.ShopOrderItem{{
			ID: "item-dispute-race", OrderID: "order-dispute-race", Source: "shopify",
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
			UnitAmountMinor: 1000, Currency: "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	admin := &capturingShopifyOrderAdmin{}
	admin.beforeCreate = func() {
		if err := db.Model(&models.ShopOrder{}).
			Where("id = ?", order.ID).
			Updates(map[string]any{
				"status":           "payment_disputed",
				"financial_status": "disputed",
				"dispute_id":       "dp_race",
				"dispute_status":   "needs_response",
				"failure_reason":   "Stripe dispute dp_race requires review",
			}).Error; err != nil {
			t.Fatalf("record concurrent dispute: %v", err)
		}
	}
	if err := NewOrderDispatcher(db, admin).Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentIDValue(),
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: order.Items[0].VariantID, Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	var stored models.ShopOrder
	if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ShopifyOrderGID() == "" {
		t.Fatal("successful Shopify order mapping was not persisted")
	}
	if stored.Status != "payment_disputed" ||
		stored.FinancialStatus != "disputed" ||
		stored.DisputeStatus != "needs_response" ||
		stored.FailureReason != "Stripe dispute dp_race requires review" {
		t.Fatalf("Shopify result regressed concurrent dispute state: %+v", stored)
	}
}
