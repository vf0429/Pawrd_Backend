package payments

import "testing"

func TestParseItemsFromMetadata_NewFormatShopify(t *testing.T) {
	meta := map[string]string{
		"item_1":          "source=shopify | handle=dog-hoodie | variant=gid://123 | qty:2",
		"customer_name":   "Alice",
		"total_items":     "2",
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
