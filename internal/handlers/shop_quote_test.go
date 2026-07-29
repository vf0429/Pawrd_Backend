package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeStorefrontQuoteClient struct {
	createRequest  shopify.StorefrontQuoteRequest
	selectCartID   string
	selectRequest  shopify.StorefrontDeliverySelection
	initial        *shopify.StorefrontQuote
	selected       *shopify.StorefrontQuote
	createCalls    int
	selectionCalls int
	onSelect       func()
}

func (f *fakeStorefrontQuoteClient) CreateCartQuote(
	_ context.Context,
	req shopify.StorefrontQuoteRequest,
) (*shopify.StorefrontQuote, error) {
	f.createCalls++
	f.createRequest = req
	return f.initial, nil
}

func (f *fakeStorefrontQuoteClient) SelectCartDelivery(
	_ context.Context,
	cartID string,
	selection shopify.StorefrontDeliverySelection,
	_ string,
) (*shopify.StorefrontQuote, error) {
	f.selectionCalls++
	f.selectCartID = cartID
	f.selectRequest = selection
	if f.onSelect != nil {
		f.onSelect()
	}
	return f.selected, nil
}

type fakeCheckoutPayments struct {
	requests    []payments.CreatePaymentIntentRequest
	cancelCalls []string
	onCreate    func(payments.CreatePaymentIntentRequest)
}

func (f *fakeCheckoutPayments) CreatePaymentIntent(
	req payments.CreatePaymentIntentRequest,
) (*payments.CreatePaymentIntentResponse, error) {
	f.requests = append(f.requests, req)
	if f.onCreate != nil {
		f.onCreate(req)
	}
	return &payments.CreatePaymentIntentResponse{
		ClientSecret:    "pi_quote_secret",
		PaymentIntentID: "pi_quote",
		PublishableKey:  "pk_test_example",
	}, nil
}

func (f *fakeCheckoutPayments) CancelPaymentIntent(id string) error {
	f.cancelCalls = append(f.cancelCalls, id)
	return nil
}

func TestShopQuoteHandlerCreatesThenSelectsAuthoritativeDelivery(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-quote", "alice@example.com")
	db := newShopFlowTestDB(t, true)
	initial, selected := handlerTestStorefrontQuotes()
	client := &fakeStorefrontQuoteClient{initial: initial, selected: selected}
	factoryCalls := 0
	handler := newShopQuoteHandler(
		cfg,
		db,
		func(*config.Config) (shopify.StorefrontQuoteClient, error) {
			factoryCalls++
			return client, nil
		},
		func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
		fixedShopAccountEmail("alice@example.com"),
	)

	createRequest := ShopQuoteRequest{
		LineItems: []ShopCheckoutLineItemRequest{{
			Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		Customer: ShopCheckoutCustomerRequest{
			Name: "Alice Test", Email: "alice@example.com", Phone: "61234567",
		},
		Shipping: ShopCheckoutShippingRequest{
			RecipientName: "Alice Test", Phone: "61234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island",
		},
		DiscountCode: "PAWRD5",
	}
	createRecorder := performShopFlowRequest(t, handler, token, createRequest)
	if createRecorder.Code != http.StatusOK {
		t.Fatalf("create quote status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created ShopQuoteResponse
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.QuoteID == "" || len(created.Version) != 64 ||
		created.Status != models.ShopQuoteStatusDeliveryRequired ||
		len(created.DeliveryOptions) != 1 {
		t.Fatalf("unexpected initial quote response: %+v", created)
	}
	if client.createRequest.DiscountCode != "PAWRD5" ||
		client.createRequest.Lines[0].VariantID != "gid://shopify/ProductVariant/1" {
		t.Fatalf("handler did not forward authoritative quote inputs: %+v", client.createRequest)
	}

	selectRecorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
		QuoteID: created.QuoteID, Version: created.Version,
		SelectedDeliveryOptionHandle: "standard-hk",
	})
	if selectRecorder.Code != http.StatusOK {
		t.Fatalf("select delivery status=%d body=%s", selectRecorder.Code, selectRecorder.Body.String())
	}
	var finalized ShopQuoteResponse
	if err := json.Unmarshal(selectRecorder.Body.Bytes(), &finalized); err != nil {
		t.Fatal(err)
	}
	if finalized.Status != models.ShopQuoteStatusReady ||
		finalized.Version == created.Version ||
		finalized.SelectedDeliveryOption == nil ||
		finalized.SelectedDeliveryOption.Handle != "standard-hk" ||
		finalized.Amounts.TotalAmountMinor != 2000 {
		t.Fatalf("unexpected final quote response: %+v", finalized)
	}
	if client.selectCartID != initial.CartID ||
		client.selectRequest.DeliveryGroupID != "group-1" ||
		factoryCalls != 2 {
		t.Fatalf("unexpected delivery selection: client=%+v factories=%d", client, factoryCalls)
	}
	var stored models.ShopCheckoutQuote
	if err := db.First(&stored, "id = ?", created.QuoteID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.ShopQuoteStatusReady ||
		stored.SelectedDeliveryOptionHandle != "standard-hk" {
		t.Fatalf("final quote was not persisted: %+v", stored)
	}

	staleRecorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
		QuoteID: created.QuoteID, Version: created.Version,
		SelectedDeliveryOptionHandle: "standard-hk",
	})
	if staleRecorder.Code != http.StatusConflict {
		t.Fatalf("stale selection status=%d body=%s", staleRecorder.Code, staleRecorder.Body.String())
	}
	if client.selectionCalls != 1 {
		t.Fatalf("stale version reached Shopify; selection calls=%d", client.selectionCalls)
	}
}

func TestShopQuoteHandlerUsesCurrentAccountEmailWhenJWTEmailIsStaleOrMissing(t *testing.T) {
	for _, test := range []struct {
		name     string
		jwtEmail string
	}{
		{name: "stale JWT email", jwtEmail: "old@example.com"},
		{name: "missing JWT email", jwtEmail: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, cfg := shopFlowAuth(t, "user-current-email", test.jwtEmail)
			db := newShopFlowTestDB(t, true)
			initial, _ := handlerTestStorefrontQuotes()
			client := &fakeStorefrontQuoteClient{initial: initial}
			resolvedUserID := ""
			handler := newShopQuoteHandler(
				cfg,
				db,
				func(*config.Config) (shopify.StorefrontQuoteClient, error) {
					return client, nil
				},
				time.Now,
				func(_ context.Context, userID string) (string, error) {
					resolvedUserID = userID
					return "current@example.com", nil
				},
			)

			recorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
				LineItems: []ShopCheckoutLineItemRequest{{
					Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
				}},
				Customer: ShopCheckoutCustomerRequest{
					Name: "Current User", Email: "current@example.com", Phone: "61234567",
				},
				Shipping: ShopCheckoutShippingRequest{
					RecipientName: "Current User", Phone: "61234567",
					Address1: "1 Test Street", District: "Wan Chai",
					Region: "Hong Kong Island",
				},
			})

			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if resolvedUserID != "user-current-email" {
				t.Fatalf("resolver user_id=%q", resolvedUserID)
			}
			if client.createRequest.Email != "current@example.com" {
				t.Fatalf("Shopify email=%q, want current AuthDB email", client.createRequest.Email)
			}
			var stored models.ShopCheckoutQuote
			if err := db.First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			snapshot, err := stored.DecodeAndVerifySnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Customer.Email != "current@example.com" {
				t.Fatalf("sealed quote email=%q", snapshot.Customer.Email)
			}
		})
	}
}

func TestShopQuoteHandlerRejectsStaleJWTEmailAsCustomerEmail(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-email-changed", "old@example.com")
	db := newShopFlowTestDB(t, true)
	factoryCalls := 0
	handler := newShopQuoteHandler(
		cfg,
		db,
		func(*config.Config) (shopify.StorefrontQuoteClient, error) {
			factoryCalls++
			return &fakeStorefrontQuoteClient{}, nil
		},
		time.Now,
		fixedShopAccountEmail("current@example.com"),
	)

	recorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
		LineItems: []ShopCheckoutLineItemRequest{{
			Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		Customer: ShopCheckoutCustomerRequest{
			Name: "Current User", Email: "old@example.com", Phone: "61234567",
		},
		Shipping: ShopCheckoutShippingRequest{
			RecipientName: "Current User", Phone: "61234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island",
		},
	})

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("stale JWT email reached Shopify factory %d times", factoryCalls)
	}
}

func TestShopQuoteHandlerFailsClosedWhenAuthDBIsUnavailable(t *testing.T) {
	previousAuthDB := models.AuthDB
	models.AuthDB = nil
	t.Cleanup(func() {
		models.AuthDB = previousAuthDB
	})

	token, cfg := shopFlowAuth(t, "user-auth-db-unavailable", "alice@example.com")
	factoryCalls := 0
	handler := newShopQuoteHandler(
		cfg,
		newShopFlowTestDB(t, true),
		func(*config.Config) (shopify.StorefrontQuoteClient, error) {
			factoryCalls++
			return &fakeStorefrontQuoteClient{}, nil
		},
		time.Now,
		currentShopAccountEmail,
	)

	recorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("unavailable AuthDB reached Shopify factory %d times", factoryCalls)
	}
}

func TestCurrentShopAccountEmailLoadsCurrentAuthDBValueByUserID(t *testing.T) {
	authDB, err := gorm.Open(
		sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := authDB.AutoMigrate(&models.AuthUser{}); err != nil {
		t.Fatal(err)
	}
	user := models.AuthUser{
		ID:           42,
		Email:        "current@example.com",
		Phone:        "phone-current-email-test",
		PasswordHash: "not-used",
		Name:         "Current User",
	}
	if err := authDB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	previousAuthDB := models.AuthDB
	models.AuthDB = authDB
	t.Cleanup(func() {
		models.AuthDB = previousAuthDB
	})

	email, err := currentShopAccountEmail(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if email != "current@example.com" {
		t.Fatalf("email=%q", email)
	}
}

func TestShopQuoteHandlerRejectsUnsupportedSourceBeforeShopifyCall(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-source", "alice@example.com")
	db := newShopFlowTestDB(t, true)
	factoryCalls := 0
	handler := newShopQuoteHandler(
		cfg,
		db,
		func(*config.Config) (shopify.StorefrontQuoteClient, error) {
			factoryCalls++
			return &fakeStorefrontQuoteClient{}, nil
		},
		time.Now,
		fixedShopAccountEmail("alice@example.com"),
	)
	request := ShopQuoteRequest{
		LineItems: []ShopCheckoutLineItemRequest{{
			Source: "hicustom", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		Customer: ShopCheckoutCustomerRequest{
			Name: "Alice", Email: "alice@example.com",
		},
		Shipping: ShopCheckoutShippingRequest{
			RecipientName: "Alice", Phone: "61234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island",
		},
	}
	recorder := performShopFlowRequest(t, handler, token, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("unsupported source reached Shopify factory %d times", factoryCalls)
	}
}

func TestShopQuoteHandlerRejectsInvalidCustomerPhoneBeforeShopifyCall(t *testing.T) {
	for _, test := range []struct {
		name          string
		phone         string
		wantErrorText string
	}{
		{
			name:          "legacy nine digit owner phone",
			phone:         "612345678",
			wantErrorText: "Hong Kong phone number must contain 8 digits",
		},
		{
			name:          "invalid Hong Kong leading digit",
			phone:         "11234567",
			wantErrorText: "Hong Kong phone number must start with 2-9",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, cfg := shopFlowAuth(t, "user-invalid-customer-phone", "alice@example.com")
			db := newShopFlowTestDB(t, true)
			factoryCalls := 0
			handler := newShopQuoteHandler(
				cfg,
				db,
				func(*config.Config) (shopify.StorefrontQuoteClient, error) {
					factoryCalls++
					initial, _ := handlerTestStorefrontQuotes()
					return &fakeStorefrontQuoteClient{initial: initial}, nil
				},
				time.Now,
				fixedShopAccountEmail("alice@example.com"),
			)

			recorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
				LineItems: []ShopCheckoutLineItemRequest{{
					Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
				}},
				Customer: ShopCheckoutCustomerRequest{
					Name: "Alice", Email: "alice@example.com", Phone: test.phone,
				},
				Shipping: ShopCheckoutShippingRequest{
					RecipientName: "Alice", Phone: "61234567",
					Address1: "1 Test Street", District: "Wan Chai",
					Region: "Hong Kong Island",
				},
			})

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			if !strings.Contains(body, "Invalid customer.phone:") ||
				!strings.Contains(body, test.wantErrorText) {
				t.Fatalf("body=%q does not identify invalid customer.phone", body)
			}
			if factoryCalls != 0 {
				t.Fatalf("invalid customer.phone reached Shopify factory %d times", factoryCalls)
			}
			var count int64
			if err := db.Model(&models.ShopCheckoutQuote{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("invalid customer.phone persisted %d quote rows", count)
			}
		})
	}
}

func TestShopQuoteHandlerRequiresExactNormalizedCartLines(t *testing.T) {
	for _, test := range []struct {
		name              string
		returnedQuantity  int
		wantStatus        int
		wantPersistedRows int64
	}{
		{
			name:              "duplicate request variants match one returned total",
			returnedQuantity:  2,
			wantStatus:        http.StatusOK,
			wantPersistedRows: 1,
		},
		{
			name:              "Shopify quantity adjustment is rejected",
			returnedQuantity:  1,
			wantStatus:        http.StatusUnprocessableEntity,
			wantPersistedRows: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, cfg := shopFlowAuth(t, "user-adjusted-lines", "alice@example.com")
			db := newShopFlowTestDB(t, true)
			initial, _ := handlerTestStorefrontQuotes()
			initial.Lines[0].Quantity = test.returnedQuantity
			client := &fakeStorefrontQuoteClient{initial: initial}
			handler := newShopQuoteHandler(
				cfg,
				db,
				func(*config.Config) (shopify.StorefrontQuoteClient, error) {
					return client, nil
				},
				func() time.Time {
					return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
				},
				fixedShopAccountEmail("alice@example.com"),
			)

			recorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
				LineItems: []ShopCheckoutLineItemRequest{
					{
						Source: "shopify", VariantID: "gid://shopify/ProductVariant/1",
						Quantity: 1,
					},
					{
						Source: "shopify", VariantID: "gid://shopify/ProductVariant/1",
						Quantity: 1,
					},
				},
				Customer: ShopCheckoutCustomerRequest{
					Name: "Alice Test", Email: "alice@example.com", Phone: "61234567",
				},
				Shipping: ShopCheckoutShippingRequest{
					RecipientName: "Alice Test", Phone: "61234567",
					Address1: "1 Test Street", District: "Wan Chai",
					Region: "Hong Kong Island",
				},
			})
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if len(client.createRequest.Lines) != 1 ||
				client.createRequest.Lines[0].Quantity != 2 {
				t.Fatalf("duplicate variants were not normalized: %+v", client.createRequest.Lines)
			}
			var count int64
			if err := db.Model(&models.ShopCheckoutQuote{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != test.wantPersistedRows {
				t.Fatalf("persisted quote rows=%d, want %d", count, test.wantPersistedRows)
			}
		})
	}
}

func TestShopQuoteHandlerFailsClosedWhenCheckoutIsDisabled(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-disabled", "alice@example.com")
	cfg.ShopCheckoutEnabled = false
	factoryCalls := 0
	handler := newShopQuoteHandler(
		cfg,
		newShopFlowTestDB(t, true),
		func(*config.Config) (shopify.StorefrontQuoteClient, error) {
			factoryCalls++
			return &fakeStorefrontQuoteClient{}, nil
		},
		time.Now,
		fixedShopAccountEmail("alice@example.com"),
	)

	recorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if factoryCalls != 0 {
		t.Fatalf("disabled checkout reached Shopify factory %d times", factoryCalls)
	}
}

func TestShopQuoteDeliverySelectionCASRejectsMutationAfterShopifyCall(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-cas", "alice@example.com")
	db := newShopFlowTestDB(t, true)
	initial, selected := handlerTestStorefrontQuotes()
	client := &fakeStorefrontQuoteClient{initial: initial, selected: selected}
	handler := newShopQuoteHandler(
		cfg,
		db,
		func(*config.Config) (shopify.StorefrontQuoteClient, error) {
			return client, nil
		},
		func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
		fixedShopAccountEmail("alice@example.com"),
	)
	createRecorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
		LineItems: []ShopCheckoutLineItemRequest{{
			Source: "shopify", VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
		Customer: ShopCheckoutCustomerRequest{
			Name: "Alice Test", Email: "alice@example.com", Phone: "61234567",
		},
		Shipping: ShopCheckoutShippingRequest{
			RecipientName: "Alice Test", Phone: "61234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island",
		},
	})
	if createRecorder.Code != http.StatusOK {
		t.Fatalf("create quote status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created ShopQuoteResponse
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	concurrentVersion := strings.Repeat("b", 64)
	client.onSelect = func() {
		if err := db.Model(&models.ShopCheckoutQuote{}).
			Where("id = ?", created.QuoteID).
			Update("snapshot_sha256", concurrentVersion).Error; err != nil {
			t.Fatalf("simulate concurrent mutation: %v", err)
		}
	}
	selectRecorder := performShopFlowRequest(t, handler, token, ShopQuoteRequest{
		QuoteID: created.QuoteID, Version: created.Version,
		SelectedDeliveryOptionHandle: "standard-hk",
	})
	if selectRecorder.Code != http.StatusConflict {
		t.Fatalf("CAS conflict status=%d body=%s", selectRecorder.Code, selectRecorder.Body.String())
	}
	var stored models.ShopCheckoutQuote
	if err := db.First(&stored, "id = ?", created.QuoteID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.SnapshotSHA256 != concurrentVersion || stored.SelectedDeliveryOptionHandle != "" {
		t.Fatalf("stale mutation overwrote newer quote state: %+v", stored)
	}
}

func TestFreeShippingCodeRemainsSelectableBeforeFinalApplicabilityCheck(t *testing.T) {
	initial, _ := handlerTestStorefrontQuotes()
	initial.DiscountCode = "SHIPFREE"
	initial.DiscountCodeApplicable = false
	initial.DiscountTargetType = "SHIPPING_LINE"

	beforeSelection := shopQuoteSnapshot(
		"user-1",
		models.ShopQuoteCustomer{Email: "alice@example.com"},
		models.ShopQuoteShipping{CountryCode: "HK"},
		initial,
		time.Now().UTC(),
		time.Now().UTC().Add(10*time.Minute),
		false,
	)
	if beforeSelection.Status != models.ShopQuoteStatusDeliveryRequired {
		t.Fatalf("free-shipping quote must allow delivery selection, got %q", beforeSelection.Status)
	}

	afterSelection := shopQuoteSnapshot(
		"user-1",
		models.ShopQuoteCustomer{Email: "alice@example.com"},
		models.ShopQuoteShipping{CountryCode: "HK"},
		initial,
		time.Now().UTC(),
		time.Now().UTC().Add(10*time.Minute),
		true,
	)
	if afterSelection.Status != models.ShopQuoteStatusDiscountInvalid {
		t.Fatalf("final inapplicable code must be rejected, got %q", afterSelection.Status)
	}
}

func TestShopQuoteResponseAlwaysIncludesSwiftStringContractFields(t *testing.T) {
	response := shopQuoteResponse("quote-contract", strings.Repeat("a", 64), models.ShopQuoteSnapshot{
		Status: "ready", Currency: "HKD",
		LineItems: []models.ShopQuoteSnapshotItem{{
			Source: "shopify", VariantID: "gid://shopify/ProductVariant/1",
			Title: "Cat Bed", Quantity: 1,
		}},
		DeliveryOptions: []models.ShopQuoteDeliveryOption{{
			DeliveryGroupID: "group-1", Handle: "standard",
			Title: "Standard", Currency: "HKD",
		}},
		Discount: models.ShopQuoteDiscount{},
	})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	lines := payload["lineItems"].([]any)
	line := lines[0].(map[string]any)
	for _, key := range []string{"variantTitle", "imageUrl"} {
		if value, exists := line[key]; !exists || value != "" {
			t.Fatalf("line field %q must be emitted as an empty string: %s", key, raw)
		}
	}
	options := payload["deliveryOptions"].([]any)
	option := options[0].(map[string]any)
	for _, key := range []string{"code", "description"} {
		if value, exists := option[key]; !exists || value != "" {
			t.Fatalf("delivery field %q must be emitted as an empty string: %s", key, raw)
		}
	}
	discount := payload["discount"].(map[string]any)
	for _, key := range []string{"code", "targetType"} {
		if value, exists := discount[key]; !exists || value != "" {
			t.Fatalf("discount field %q must be emitted as an empty string: %s", key, raw)
		}
	}
}

func TestShopPaymentSheetConsumesReadyQuoteWithStableIdempotencyKey(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-payment", "alice@example.com")
	db := newShopFlowTestDB(t, true)
	clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	record := persistReadyHandlerQuote(t, db, "quote-payment", "user-payment", clock.Add(10*time.Minute))
	paymentService := &fakeCheckoutPayments{}
	handler := newShopPaymentSheetHandler(
		cfg,
		db,
		func(*config.Config) (checkoutPaymentService, error) {
			return paymentService, nil
		},
		func() time.Time { return clock },
		fixedShopAccountEmail("alice@example.com"),
	)

	recorder := performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if recorder.Code != http.StatusOK {
		t.Fatalf("payment sheet status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(paymentService.requests) != 1 {
		t.Fatalf("expected one PaymentIntent, got %d", len(paymentService.requests))
	}
	paymentRequest := paymentService.requests[0]
	if paymentRequest.Amount != 2000 ||
		paymentRequest.IdempotencyKey != shopPaymentIntentIdempotencyKey(record.ID, record.SnapshotSHA256) ||
		paymentRequest.Metadata["pawrd_quote_id"] != record.ID ||
		paymentRequest.Metadata["pawrd_quote_version"] != record.SnapshotSHA256 ||
		paymentRequest.Metadata["pawrd_quote_expires_at"] != record.ExpiresAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("unexpected PaymentIntent request: %+v", paymentRequest)
	}
	var storedQuote models.ShopCheckoutQuote
	if err := db.First(&storedQuote, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedQuote.Status != models.ShopQuoteStatusConsumed ||
		storedQuote.PaymentIntentID != "pi_quote" || storedQuote.OrderID == "" {
		t.Fatalf("quote was not atomically consumed: %+v", storedQuote)
	}
	var order models.ShopOrder
	if err := db.Preload("Items").First(&order, "id = ?", storedQuote.OrderID).Error; err != nil {
		t.Fatal(err)
	}
	if order.TotalAmountMinor != 2000 || len(order.Items) != 1 ||
		order.ShippingAddress1 != "1 Test Street" {
		t.Fatalf("unexpected persisted paid-order candidate: %+v", order)
	}

	clock = clock.Add(20 * time.Minute)
	retry := performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if retry.Code != http.StatusOK {
		t.Fatalf("consumed quote replay status=%d body=%s", retry.Code, retry.Body.String())
	}
	if len(paymentService.requests) != 2 ||
		!reflect.DeepEqual(paymentService.requests[0], paymentService.requests[1]) {
		t.Fatalf("consumed quote did not replay identical Stripe parameters: %#v", paymentService.requests)
	}
	var orderCount int64
	if err := db.Model(&models.ShopOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 {
		t.Fatalf("consumed quote replay created %d local orders", orderCount)
	}
}

func TestShopPaymentSheetUsesCurrentAccountEmailWhenJWTEmailIsStaleOrMissing(t *testing.T) {
	for _, test := range []struct {
		name     string
		jwtEmail string
	}{
		{name: "stale JWT email", jwtEmail: "old@example.com"},
		{name: "missing JWT email", jwtEmail: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			token, cfg := shopFlowAuth(t, "user-payment-current-email", test.jwtEmail)
			db := newShopFlowTestDB(t, true)
			clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
			record := persistReadyHandlerQuote(
				t,
				db,
				"quote-payment-current-email",
				"user-payment-current-email",
				clock.Add(10*time.Minute),
			)
			paymentService := &fakeCheckoutPayments{}
			handler := newShopPaymentSheetHandler(
				cfg,
				db,
				func(*config.Config) (checkoutPaymentService, error) {
					return paymentService, nil
				},
				func() time.Time { return clock },
				fixedShopAccountEmail("alice@example.com"),
			)

			recorder := performShopFlowRequest(
				t,
				handler,
				token,
				ShopPaymentSheetRequest{QuoteID: record.ID},
			)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if len(paymentService.requests) != 1 {
				t.Fatalf("PaymentIntent calls=%d", len(paymentService.requests))
			}
			request := paymentService.requests[0]
			if request.ReceiptEmail != "alice@example.com" ||
				request.Metadata["customer_email"] != "alice@example.com" {
				t.Fatalf("PaymentIntent did not use sealed current email: %+v", request)
			}
		})
	}
}

func TestShopPaymentSheetRejectsQuoteFromBeforeAccountEmailChanged(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-payment-email-changed", "old@example.com")
	db := newShopFlowTestDB(t, true)
	clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	record := persistReadyHandlerQuote(
		t,
		db,
		"quote-payment-email-changed",
		"user-payment-email-changed",
		clock.Add(10*time.Minute),
	)
	paymentService := &fakeCheckoutPayments{}
	handler := newShopPaymentSheetHandler(
		cfg,
		db,
		func(*config.Config) (checkoutPaymentService, error) {
			return paymentService, nil
		},
		func() time.Time { return clock },
		fixedShopAccountEmail("current@example.com"),
	)

	recorder := performShopFlowRequest(
		t,
		handler,
		token,
		ShopPaymentSheetRequest{QuoteID: record.ID},
	)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(paymentService.requests) != 0 {
		t.Fatalf("stale quote reached Stripe: %#v", paymentService.requests)
	}
	var stored models.ShopCheckoutQuote
	if err := db.First(&stored, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.ShopQuoteStatusReady || stored.ConsumedAt != nil {
		t.Fatalf("stale-email quote was consumed: %+v", stored)
	}
}

func TestShopPaymentSheetFailsClosedWhenAuthUserNoLongerExists(t *testing.T) {
	authDB, err := gorm.Open(
		sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"_auth?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := authDB.AutoMigrate(&models.AuthUser{}); err != nil {
		t.Fatal(err)
	}
	previousAuthDB := models.AuthDB
	models.AuthDB = authDB
	t.Cleanup(func() {
		models.AuthDB = previousAuthDB
	})

	token, cfg := shopFlowAuth(t, "404", "deleted@example.com")
	paymentFactoryCalls := 0
	handler := newShopPaymentSheetHandler(
		cfg,
		newShopFlowTestDB(t, true),
		func(*config.Config) (checkoutPaymentService, error) {
			paymentFactoryCalls++
			return &fakeCheckoutPayments{}, nil
		},
		time.Now,
		currentShopAccountEmail,
	)

	recorder := performShopFlowRequest(
		t,
		handler,
		token,
		ShopPaymentSheetRequest{QuoteID: "deleted-user-quote"},
	)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if paymentFactoryCalls != 0 {
		t.Fatalf("deleted AuthDB user reached Stripe factory %d times", paymentFactoryCalls)
	}
}

func TestShopPaymentSheetDoesNotConsumeAConcurrentQuoteVersion(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-race", "alice@example.com")
	db := newShopFlowTestDB(t, true)
	clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	record := persistReadyHandlerQuote(t, db, "quote-version-race", "user-race", clock.Add(10*time.Minute))
	originalVersion := record.SnapshotSHA256
	paymentService := &fakeCheckoutPayments{}
	paymentService.onCreate = func(payments.CreatePaymentIntentRequest) {
		paymentService.onCreate = nil
		var current models.ShopCheckoutQuote
		if err := db.First(&current, "id = ?", record.ID).Error; err != nil {
			t.Fatal(err)
		}
		snapshot, err := current.DecodeAndVerifySnapshot()
		if err != nil {
			t.Fatal(err)
		}
		express := models.ShopQuoteDeliveryOption{
			DeliveryGroupID: "group-1", Handle: "express-hk",
			Code: "EXPRESS", Title: "Hong Kong Express",
			DeliveryMethod: "SHIPPING", AmountMinor: 900, Currency: "HKD",
		}
		snapshot.DeliveryOptions = append(snapshot.DeliveryOptions, express)
		snapshot.SelectedDeliveryOption = &express
		snapshot.Amounts.ShippingAmountMinor = 900
		snapshot.Amounts.TotalAmountMinor = 2400
		if err := current.SetSnapshot(snapshot); err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&models.ShopCheckoutQuote{}).
			Where("id = ?", current.ID).
			Updates(shopQuoteUpdateColumns(current)).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler := newShopPaymentSheetHandler(
		cfg,
		db,
		func(*config.Config) (checkoutPaymentService, error) {
			return paymentService, nil
		},
		func() time.Time { return clock },
		fixedShopAccountEmail("alice@example.com"),
	)

	recorder := performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("version race status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(paymentService.requests) != 1 ||
		paymentService.requests[0].IdempotencyKey != shopPaymentIntentIdempotencyKey(record.ID, originalVersion) {
		t.Fatalf("unexpected original PaymentIntent request: %#v", paymentService.requests)
	}
	var stored models.ShopCheckoutQuote
	if err := db.First(&stored, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.ShopQuoteStatusReady ||
		stored.SnapshotSHA256 == originalVersion ||
		stored.SelectedDeliveryOptionHandle != "express-hk" {
		t.Fatalf("concurrent quote was incorrectly consumed or overwritten: %+v", stored)
	}
	var orderCount int64
	if err := db.Model(&models.ShopOrder{}).Count(&orderCount).Error; err != nil {
		t.Fatal(err)
	}
	if orderCount != 0 {
		t.Fatalf("version race persisted %d stale orders", orderCount)
	}
}

func TestShopPaymentSheetRetainsIdempotentIntentWhenLocalTransactionFails(t *testing.T) {
	token, cfg := shopFlowAuth(t, "user-retry", "alice@example.com")
	db := newShopFlowTestDB(t, false)
	if err := db.AutoMigrate(&models.ShopCheckoutQuote{}); err != nil {
		t.Fatal(err)
	}
	record := persistReadyHandlerQuote(t, db, "quote-retry", "user-retry", time.Now().Add(10*time.Minute))
	paymentService := &fakeCheckoutPayments{}
	handler := newShopPaymentSheetHandler(
		cfg,
		db,
		func(*config.Config) (checkoutPaymentService, error) {
			return paymentService, nil
		},
		time.Now,
		fixedShopAccountEmail("alice@example.com"),
	)

	for attempt := 0; attempt < 2; attempt++ {
		recorder := performShopFlowRequest(t, handler, token, ShopPaymentSheetRequest{QuoteID: record.ID})
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("attempt %d status=%d body=%s", attempt+1, recorder.Code, recorder.Body.String())
		}
	}
	if len(paymentService.requests) != 2 {
		t.Fatalf("expected two retry calls into fake Stripe, got %d", len(paymentService.requests))
	}
	if paymentService.requests[0].IdempotencyKey != paymentService.requests[1].IdempotencyKey {
		t.Fatalf("PaymentIntent retry key changed: %#v", paymentService.requests)
	}
	if !reflect.DeepEqual(paymentService.requests[0], paymentService.requests[1]) {
		t.Fatalf("same quote produced different Stripe parameters: %#v", paymentService.requests)
	}
	if paymentService.requests[0].Metadata["pawrd_order_id"] != shopOrderIDForQuote(record.ID) {
		t.Fatalf("order ID is not quote-derived: %#v", paymentService.requests[0].Metadata)
	}
	if len(paymentService.cancelCalls) != 0 {
		t.Fatalf("quote retry path must not cancel its reusable PaymentIntent: %#v", paymentService.cancelCalls)
	}
	var stored models.ShopCheckoutQuote
	if err := db.First(&stored, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.ShopQuoteStatusReady || stored.ConsumedAt != nil {
		t.Fatalf("failed transaction did not roll back quote claim: %+v", stored)
	}
}

func shopFlowAuth(t *testing.T, userID, email string) (string, *config.Config) {
	t.Helper()
	secret := "test-only-jwt-secret-at-least-32-characters"
	t.Setenv("JWT_SECRET", secret)
	token, err := auth.GenerateToken(userID, email, "Test User")
	if err != nil {
		t.Fatal(err)
	}
	return token, &config.Config{
		ShopCheckoutEnabled:           true,
		DatabaseURL:                   "postgres://pawrd:secret@db.example.com/pawrd",
		JWTSecret:                     secret,
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontAPIVersion:   "2026-07",
		ShopifyStorefrontPrivateToken: "storefront-token",
		ShopifyAdminAccessToken:       "admin-token",
		ShopifyAdminAPIVersion:        "2026-07",
		ShopifyWebhookSecret:          "abcdef0123456789abcdef0123456789",
		ShopifyWebhookCallbackURL:     "https://api.pawrd.com/api/shop/webhooks/shopify",
		StripeSecretKey:               "sk_test_example",
		StripePublishableKey:          "pk_test_example",
		StripeWebhookSecret:           "whsec_0123456789abcdef0123456789abcdef",
		ShopAdminKey:                  "0123456789abcdef0123456789abcdef",
	}
}

func fixedShopAccountEmail(email string) shopAccountEmailResolver {
	return func(context.Context, string) (string, error) {
		return email, nil
	}
}

func newShopFlowTestDB(t *testing.T, migrateOrders bool) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if migrateOrders {
		if err := db.AutoMigrate(
			&models.ShopCheckoutQuote{},
			&models.ShopOrder{},
			&models.ShopOrderItem{},
		); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func performShopFlowRequest(
	t *testing.T,
	handler http.Handler,
	token string,
	payload any,
) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/shop/test", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.10:4321"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func handlerTestStorefrontQuotes() (*shopify.StorefrontQuote, *shopify.StorefrontQuote) {
	line := shopify.StorefrontQuoteLine{
		VariantID: "gid://shopify/ProductVariant/1", Handle: "cat-bed",
		Title: "Cat Bed", Quantity: 1, UnitAmountMinor: 2000,
		RequiresShipping: true,
	}
	option := shopify.StorefrontDeliveryOption{
		DeliveryGroupID: "group-1", Handle: "standard-hk",
		Code: "STANDARD", Title: "Hong Kong Standard",
		DeliveryMethod: "SHIPPING", AmountMinor: 500, Currency: "HKD",
	}
	initial := &shopify.StorefrontQuote{
		CartID: "gid://shopify/Cart/cart-1", Currency: "HKD",
		Lines:           []shopify.StorefrontQuoteLine{line},
		DeliveryOptions: []shopify.StorefrontDeliveryOption{option},
		DiscountCode:    "PAWRD5", DiscountCodeApplicable: true,
		DiscountTargetType:  "LINE_ITEM",
		SubtotalAmountMinor: 2000, DiscountAmountMinor: 500,
		TotalAmountMinor: 1500,
	}
	selected := *initial
	selected.SelectedDeliveryOption = &option
	selected.ShippingAmountMinor = 500
	selected.TotalAmountMinor = 2000
	return initial, &selected
}

func persistReadyHandlerQuote(
	t *testing.T,
	db *gorm.DB,
	id string,
	userID string,
	expiresAt time.Time,
) models.ShopCheckoutQuote {
	t.Helper()
	selected := models.ShopQuoteDeliveryOption{
		DeliveryGroupID: "group-1", Handle: "standard-hk",
		Code: "STANDARD", Title: "Hong Kong Standard",
		DeliveryMethod: "SHIPPING", AmountMinor: 500, Currency: "HKD",
	}
	snapshot := models.ShopQuoteSnapshot{
		Version:              models.ShopQuoteSnapshotVersion,
		ShopifyCartID:        "gid://shopify/Cart/" + id,
		ShopifyCartUpdatedAt: time.Now().UTC(),
		UserID:               userID, Status: models.ShopQuoteStatusReady, Currency: "HKD",
		LineItems: []models.ShopQuoteSnapshotItem{{
			Source: "shopify", Handle: "cat-bed",
			VariantID: "gid://shopify/ProductVariant/1",
			Title:     "Cat Bed", Quantity: 1, UnitAmountMinor: 2000,
			RequiresShipping: true,
		}},
		DeliveryOptions:        []models.ShopQuoteDeliveryOption{selected},
		SelectedDeliveryOption: &selected,
		Amounts: models.ShopQuoteAmounts{
			SubtotalAmountMinor: 2000,
			ShippingAmountMinor: 500,
			DiscountAmountMinor: 500,
			TotalAmountMinor:    2000,
		},
		Discount: models.ShopQuoteDiscount{
			Code: "PAWRD5", Applicable: true, TargetType: "LINE_ITEM",
		},
		Customer: models.ShopQuoteCustomer{
			Name: "Alice Test", Email: "alice@example.com", Phone: "+85261234567",
		},
		Shipping: models.ShopQuoteShipping{
			RecipientName: "Alice Test", Phone: "+85261234567",
			Address1: "1 Test Street", District: "Wan Chai",
			Region: "Hong Kong Island", CountryCode: "HK",
		},
		QuotedAt: time.Now().UTC(), ExpiresAt: expiresAt.UTC(),
	}
	record := models.ShopCheckoutQuote{ID: id}
	if err := record.SetSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	return record
}
