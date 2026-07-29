package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestShopCheckoutQuoteSealsAndVerifiesAuthoritativeSnapshot(t *testing.T) {
	record := ShopCheckoutQuote{ID: "quote-1"}
	snapshot := testShopQuoteSnapshot()
	if err := record.SetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	decoded, err := record.DecodeAndVerifySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Amounts.TotalAmountMinor != 2502 ||
		record.SelectedDeliveryOptionHandle != "standard-hk" ||
		record.SnapshotSHA256 == "" {
		t.Fatalf("unexpected sealed quote: record=%+v decoded=%+v", record, decoded)
	}
}

func TestShopCheckoutQuoteRejectsSnapshotOrIndexedFieldTampering(t *testing.T) {
	record := ShopCheckoutQuote{ID: "quote-1"}
	if err := record.SetSnapshot(testShopQuoteSnapshot()); err != nil {
		t.Fatal(err)
	}

	tamperedSnapshot := record
	tamperedSnapshot.SnapshotJSON = strings.Replace(
		tamperedSnapshot.SnapshotJSON,
		`"total":2502`,
		`"total":1`,
		1,
	)
	if _, err := tamperedSnapshot.DecodeAndVerifySnapshot(); err == nil ||
		!strings.Contains(err.Error(), "integrity") {
		t.Fatalf("expected snapshot hash failure, got %v", err)
	}

	tamperedIndex := record
	tamperedIndex.SubtotalAmountMinor++
	if _, err := tamperedIndex.DecodeAndVerifySnapshot(); err == nil ||
		!strings.Contains(err.Error(), "indexed fields") {
		t.Fatalf("expected indexed-field mismatch, got %v", err)
	}

	tamperedStatus := record
	tamperedStatus.Status = ShopQuoteStatusReady
	if _, err := tamperedStatus.DecodeAndVerifySnapshot(); err == nil ||
		!strings.Contains(err.Error(), "indexed fields") {
		t.Fatalf("expected status mismatch, got %v", err)
	}
}

func TestConsumedShopCheckoutQuoteRetainsVerifiableReadySnapshot(t *testing.T) {
	record := ShopCheckoutQuote{ID: "quote-1"}
	snapshot := testShopQuoteSnapshot()
	snapshot.Status = ShopQuoteStatusReady
	if err := record.SetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	consumedAt := time.Now().UTC()
	record.Status = ShopQuoteStatusConsumed
	record.ConsumedAt = &consumedAt
	if _, err := record.DecodeAndVerifySnapshot(); err != nil {
		t.Fatalf("consumed quote should retain its sealed ready snapshot: %v", err)
	}
}

func TestShopCheckoutQuoteSurvivesPostgresMicrosecondTimestampRoundTrip(t *testing.T) {
	snapshot := testShopQuoteSnapshot()
	snapshot.ShopifyCartUpdatedAt = snapshot.ShopifyCartUpdatedAt.Add(987654321 * time.Nanosecond)
	snapshot.QuotedAt = snapshot.QuotedAt.Add(123456789 * time.Nanosecond)
	snapshot.ExpiresAt = snapshot.ExpiresAt.Add(123456789 * time.Nanosecond)

	record := ShopCheckoutQuote{ID: "quote-pg-time"}
	if err := record.SetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if record.ExpiresAt.Nanosecond()%1000 != 0 {
		t.Fatalf("sealed expiry retained sub-microsecond precision: %s", record.ExpiresAt)
	}
	if _, err := record.DecodeAndVerifySnapshot(); err != nil {
		t.Fatalf("canonical sealed quote failed round-trip: %v", err)
	}

	// Also accept an already-sealed legacy JSON timestamp after PostgreSQL has
	// reduced only the indexed timestamptz column to microsecond precision.
	legacy := ShopCheckoutQuote{ID: "quote-legacy-pg-time"}
	if err := legacy.SetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	legacy.SnapshotJSON = string(raw)
	legacy.SnapshotSHA256 = hex.EncodeToString(sum[:])
	legacy.ExpiresAt = snapshot.ExpiresAt.UTC().Truncate(time.Microsecond)
	if _, err := legacy.DecodeAndVerifySnapshot(); err != nil {
		t.Fatalf("legacy nanosecond snapshot failed PostgreSQL round-trip: %v", err)
	}
}

func testShopQuoteSnapshot() ShopQuoteSnapshot {
	quotedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	selected := ShopQuoteDeliveryOption{
		DeliveryGroupID: "group-1",
		Handle:          "standard-hk",
		Code:            "STANDARD",
		Title:           "Hong Kong Standard",
		AmountMinor:     502,
		Currency:        "HKD",
	}
	return ShopQuoteSnapshot{
		Version:              ShopQuoteSnapshotVersion,
		ShopifyCartID:        "gid://shopify/Cart/cart-1",
		ShopifyCartUpdatedAt: quotedAt,
		UserID:               "user-1",
		Status:               ShopQuoteStatusDeliveryRequired,
		Currency:             "HKD",
		LineItems: []ShopQuoteSnapshotItem{{
			Source: "shopify", Handle: "cat-bed",
			VariantID: "gid://shopify/ProductVariant/1",
			Title:     "Cat Bed", Quantity: 1, UnitAmountMinor: 2200,
			RequiresShipping: true,
		}},
		DeliveryOptions:        []ShopQuoteDeliveryOption{selected},
		SelectedDeliveryOption: &selected,
		Discount: ShopQuoteDiscount{
			Code: "PAWRD2", Applicable: true, TargetType: "LINE_ITEM",
		},
		Amounts: ShopQuoteAmounts{
			SubtotalAmountMinor: 2200,
			DiscountAmountMinor: 200,
			ShippingAmountMinor: 502,
			TotalAmountMinor:    2502,
		},
		Customer: ShopQuoteCustomer{
			Name: "Alice", Email: "alice@example.com", Phone: "+85261234567",
		},
		Shipping: ShopQuoteShipping{
			RecipientName: "Alice", Phone: "+85261234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island", CountryCode: "HK",
		},
		QuotedAt:  quotedAt,
		ExpiresAt: quotedAt.Add(10 * time.Minute),
	}
}
