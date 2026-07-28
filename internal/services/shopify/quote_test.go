package shopify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorefrontQuoteCreatesCartAndSelectsAuthoritativeDelivery(t *testing.T) {
	var createInput map[string]any
	var selectedVariables map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(privateStorefrontTokenHeader); got != "private-token" {
			t.Errorf("unexpected Storefront token %q", got)
		}
		if got := r.Header.Get("Shopify-Storefront-Buyer-IP"); got != "203.0.113.10" {
			t.Errorf("unexpected buyer IP %q", got)
		}
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Storefront request: %v", err)
			return
		}
		if strings.Contains(request.Query, "quantityAvailable") {
			t.Errorf("quote query must not depend on exact-inventory Storefront scope")
		}
		switch {
		case strings.Contains(request.Query, "PawrdCartQuote"):
			createInput, _ = request.Variables["input"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"cartCreate":{"cart":` +
				testQuoteCartJSON(false, "15.00") +
				`,"userErrors":[],"warnings":[]}}}`))
		case strings.Contains(request.Query, "PawrdSelectCartDelivery"):
			selectedVariables = request.Variables
			_, _ = w.Write([]byte(`{"data":{"cartSelectedDeliveryOptionsUpdate":{"cart":` +
				testQuoteCartJSON(true, "20.00") +
				`,"userErrors":[],"warnings":[]}}}`))
		default:
			t.Errorf("unexpected Storefront query: %s", request.Query)
		}
	}))
	defer server.Close()

	client := &Client{
		endpoint: server.URL, storefrontToken: "private-token",
		authHeader: privateStorefrontTokenHeader, httpClient: server.Client(),
	}
	initial, err := client.CreateCartQuote(context.Background(), StorefrontQuoteRequest{
		Lines: []StorefrontQuoteLineInput{{
			VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		Email: "alice@example.com", Phone: "61234567",
		Shipping: StorefrontQuoteAddress{
			RecipientName: "Alice Test", Phone: "61234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island",
		},
		DiscountCode: "PAWRD5", BuyerIP: "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if initial.TotalAmountMinor != 1500 || initial.DiscountAmountMinor != 500 ||
		initial.DiscountTargetType != "LINE_ITEM" || len(initial.DeliveryOptions) != 1 {
		t.Fatalf("unexpected initial quote: %+v", initial)
	}
	if createInput == nil {
		t.Fatal("missing cartCreate input")
	}
	discountCodes, ok := createInput["discountCodes"].([]any)
	if !ok || len(discountCodes) != 1 || discountCodes[0] != "PAWRD5" {
		t.Fatalf("discount code was not sent to Shopify: %#v", createInput["discountCodes"])
	}
	delivery, ok := createInput["delivery"].(map[string]any)
	if !ok {
		t.Fatalf("missing delivery address: %#v", createInput["delivery"])
	}
	addresses, ok := delivery["addresses"].([]any)
	if !ok || len(addresses) != 1 {
		t.Fatalf("unexpected delivery addresses: %#v", delivery)
	}
	selectableAddress, ok := addresses[0].(map[string]any)
	if !ok || selectableAddress["oneTimeUse"] != true ||
		selectableAddress["selected"] != true ||
		selectableAddress["validationStrategy"] != "STRICT" {
		t.Fatalf("address must use strict one-time validation: %#v", addresses[0])
	}

	finalQuote, err := client.SelectCartDelivery(
		context.Background(),
		initial.CartID,
		StorefrontDeliverySelection{
			DeliveryGroupID:      "gid://shopify/CartDeliveryGroup/group-1",
			DeliveryOptionHandle: "standard-hk",
		},
		"203.0.113.10",
	)
	if err != nil {
		t.Fatal(err)
	}
	if finalQuote.SelectedDeliveryOption == nil ||
		finalQuote.SelectedDeliveryOption.Handle != "standard-hk" ||
		finalQuote.ShippingAmountMinor != 500 ||
		finalQuote.TotalAmountMinor != 2000 {
		t.Fatalf("unexpected selected delivery quote: %+v", finalQuote)
	}
	if selectedVariables["cartId"] != "gid://shopify/Cart/cart-1" {
		t.Fatalf("unexpected selected cart ID: %#v", selectedVariables)
	}
}

func TestCreateCartQuoteRequiresExactNormalizedCartLines(t *testing.T) {
	for _, test := range []struct {
		name             string
		returnedQuantity string
		wantError        bool
	}{
		{
			name:             "duplicate requested variants match returned total",
			returnedQuantity: "2",
		},
		{
			name:             "automatic quantity adjustment is rejected",
			returnedQuantity: "1",
			wantError:        true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				cartJSON := strings.Replace(
					testQuoteCartJSON(false, "15.00"),
					`"quantity":1,`,
					`"quantity":`+test.returnedQuantity+`,`,
					1,
				)
				_, _ = w.Write([]byte(
					`{"data":{"cartCreate":{"cart":` + cartJSON +
						`,"userErrors":[],"warnings":[]}}}`,
				))
			}))
			defer server.Close()

			client := &Client{
				endpoint: server.URL, storefrontToken: "private-token",
				authHeader: privateStorefrontTokenHeader, httpClient: server.Client(),
			}
			quote, err := client.CreateCartQuote(context.Background(), StorefrontQuoteRequest{
				Lines: []StorefrontQuoteLineInput{
					{VariantID: "gid://shopify/ProductVariant/1", Quantity: 1},
					{VariantID: "gid://shopify/ProductVariant/1", Quantity: 1},
				},
				Email: "alice@example.com",
				Shipping: StorefrontQuoteAddress{
					RecipientName: "Alice Test", Phone: "61234567",
					Address1: "1 Test Street", District: "Wan Chai",
					Region: "Hong Kong Island",
				},
			})
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "adjusted the requested merchandise or quantity") {
					t.Fatalf("expected adjusted-cart rejection, quote=%+v err=%v", quote, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("matching normalized lines were rejected: %v", err)
			}
			if quote == nil || len(quote.Lines) != 1 || quote.Lines[0].Quantity != 2 {
				t.Fatalf("unexpected quote: %+v", quote)
			}
		})
	}
}

func TestNormalizeStorefrontQuoteFailsClosedForShippingTaxAndAutomaticDiscounts(t *testing.T) {
	t.Run("non-shippable variant", func(t *testing.T) {
		cart := decodeTestQuoteCart(t, testQuoteCartJSON(false, "15.00"))
		cart.Lines.Nodes[0].Merchandise.RequiresShipping = false
		if _, err := normalizeStorefrontQuote(cart, "PAWRD5", nil); err == nil ||
			!strings.Contains(err.Error(), "shippable physical product") {
			t.Fatalf("expected non-shippable variant rejection, got %v", err)
		}
	})

	t.Run("nonzero tax", func(t *testing.T) {
		cart := decodeTestQuoteCart(t, testQuoteCartJSON(false, "15.00"))
		cart.Cost.TotalTaxAmount = &rawQuoteMoney{Amount: "1.00", CurrencyCode: "HKD"}
		if _, err := normalizeStorefrontQuote(cart, "PAWRD5", nil); err == nil ||
			!strings.Contains(err.Error(), "tax-bearing") {
			t.Fatalf("expected unsupported tax rejection, got %v", err)
		}
	})

	t.Run("automatic discount", func(t *testing.T) {
		cart := decodeTestQuoteCart(t, testQuoteCartJSON(false, "15.00"))
		cart.DiscountApplications[0].TypeName = "CartAutomaticDiscountApplication"
		if _, err := normalizeStorefrontQuote(cart, "PAWRD5", nil); err == nil ||
			!strings.Contains(err.Error(), "automatic") {
			t.Fatalf("expected automatic-discount rejection, got %v", err)
		}
	})
}

func TestNormalizeQuoteMutationRejectsUnsafeAutomaticCartWarnings(t *testing.T) {
	for _, test := range []struct {
		name string
		code string
	}{
		{name: "quantity capped", code: "MERCHANDISE_NOT_ENOUGH_STOCK"},
		{name: "line out of stock", code: "MERCHANDISE_OUT_OF_STOCK"},
		{name: "unavailable in buyer location", code: "PRODUCT_UNAVAILABLE_IN_BUYER_LOCATION"},
		{name: "unknown automatic adjustment", code: "FUTURE_CART_ADJUSTMENT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := quoteMutationPayload{
				Cart: decodeTestQuoteCart(t, testQuoteCartJSON(false, "15.00")),
				Warnings: []rawQuoteWarning{{
					Code: test.code, Message: "Shopify automatically changed the cart",
				}},
			}
			if _, err := normalizeQuoteMutation(payload, "PAWRD5"); err == nil ||
				!strings.Contains(err.Error(), "adjusted the cart") {
				t.Fatalf("warning %s was not rejected: %v", test.code, err)
			}
		})
	}

	t.Run("discount applicability warning remains a non-chargeable quote state", func(t *testing.T) {
		cart := decodeTestQuoteCart(t, testQuoteCartJSON(false, "20.00"))
		cart.DiscountCodes[0].Applicable = false
		cart.DiscountApplications = nil
		payload := quoteMutationPayload{
			Cart: cart,
			Warnings: []rawQuoteWarning{{
				Code: "DISCOUNT_NOT_FOUND", Message: "The discount code was not found",
			}},
		}
		quote, err := normalizeQuoteMutation(payload, "PAWRD5")
		if err != nil {
			t.Fatalf("discount warning should remain representable: %v", err)
		}
		if quote.DiscountCodeApplicable {
			t.Fatalf("invalid discount unexpectedly became applicable: %+v", quote)
		}
	})
}

func decodeTestQuoteCart(t *testing.T, raw string) *rawQuoteCart {
	t.Helper()
	var cart rawQuoteCart
	if err := json.Unmarshal([]byte(raw), &cart); err != nil {
		t.Fatal(err)
	}
	return &cart
}

func testQuoteCartJSON(selected bool, total string) string {
	selectedDelivery := "null"
	if selected {
		selectedDelivery = `{
			"handle":"standard-hk","code":"STANDARD","title":"Hong Kong Standard",
			"description":"2-4 business days","deliveryMethodType":"SHIPPING",
			"estimatedCost":{"amount":"5.00","currencyCode":"HKD"}
		}`
	}
	return `{
		"id":"gid://shopify/Cart/cart-1",
		"updatedAt":"2026-07-28T12:00:00Z",
		"discountCodes":[{"code":"PAWRD5","applicable":true}],
		"discountApplications":[{
			"__typename":"CartCodeDiscountApplication",
			"code":"PAWRD5","targetType":"LINE_ITEM"
		}],
		"cost":{
			"subtotalAmount":{"amount":"20.00","currencyCode":"HKD"},
			"totalTaxAmount":{"amount":"0.00","currencyCode":"HKD"},
			"totalAmount":{"amount":"` + total + `","currencyCode":"HKD"}
		},
		"lines":{"nodes":[{
			"id":"gid://shopify/CartLine/1","quantity":1,
			"cost":{
				"amountPerQuantity":{"amount":"20.00","currencyCode":"HKD"},
				"subtotalAmount":{"amount":"20.00","currencyCode":"HKD"},
				"totalAmount":{"amount":"15.00","currencyCode":"HKD"}
			},
			"merchandise":{
				"id":"gid://shopify/ProductVariant/1","title":"Pink / M",
				"availableForSale":true,
				"requiresShipping":true,
				"image":{"url":"https://cdn.example/cat-bed.jpg"},
				"product":{"title":"Cat Bed","handle":"cat-bed"}
			}
		}]},
		"deliveryGroups":{"nodes":[{
			"id":"gid://shopify/CartDeliveryGroup/group-1",
			"deliveryOptions":[{
				"handle":"standard-hk","code":"STANDARD","title":"Hong Kong Standard",
				"description":"2-4 business days","deliveryMethodType":"SHIPPING",
				"estimatedCost":{"amount":"5.00","currencyCode":"HKD"}
			}],
			"selectedDeliveryOption":` + selectedDelivery + `
		}]}
	}`
}
