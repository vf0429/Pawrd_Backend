package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

const (
	maxShopCheckoutLines    = 40
	maxShopCheckoutQuantity = 99
)

type ShopQuoteRequest struct {
	QuoteID                      string                        `json:"quoteId,omitempty"`
	Version                      string                        `json:"version,omitempty"`
	SelectedDeliveryOptionHandle string                        `json:"selectedDeliveryOptionHandle,omitempty"`
	LineItems                    []ShopCheckoutLineItemRequest `json:"lineItems,omitempty"`
	Customer                     ShopCheckoutCustomerRequest   `json:"customer,omitempty"`
	Shipping                     ShopCheckoutShippingRequest   `json:"shipping,omitempty"`
	DiscountCode                 string                        `json:"discountCode,omitempty"`
}

type ShopQuoteResponse struct {
	QuoteID                string                           `json:"quoteId"`
	Version                string                           `json:"version"`
	Status                 string                           `json:"status"`
	ExpiresAt              time.Time                        `json:"expiresAt"`
	Currency               string                           `json:"currency"`
	LineItems              []models.ShopQuoteSnapshotItem   `json:"lineItems"`
	DeliveryOptions        []models.ShopQuoteDeliveryOption `json:"deliveryOptions"`
	SelectedDeliveryOption *models.ShopQuoteDeliveryOption  `json:"selectedDeliveryOption,omitempty"`
	Discount               models.ShopQuoteDiscount         `json:"discount"`
	Amounts                models.ShopQuoteAmounts          `json:"amounts"`
	Warnings               []string                         `json:"warnings,omitempty"`
}

type storefrontQuoteClientFactory func(*config.Config) (shopify.StorefrontQuoteClient, error)

func NewShopQuoteHandler(cfg *config.Config, db *gorm.DB) http.HandlerFunc {
	return newShopQuoteHandler(cfg, db, newStorefrontQuoteClient, time.Now)
}

func newStorefrontQuoteClient(cfg *config.Config) (shopify.StorefrontQuoteClient, error) {
	if cfg.UseMockShopify {
		return nil, fmt.Errorf("authoritative checkout quotes are unavailable while USE_MOCK_SHOPIFY=true")
	}
	return shopify.NewClient(cfg)
}

func newShopQuoteHandler(
	cfg *config.Config,
	db *gorm.DB,
	clientFactory storefrontQuoteClientFactory,
	now func() time.Time,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		claims, ok := authenticatedShopClaims(w, r)
		if !ok {
			return
		}
		if err := cfg.ValidateShopCheckoutConfig(); err != nil {
			http.Error(w, "Shop checkout is not configured", http.StatusServiceUnavailable)
			return
		}
		if db == nil {
			http.Error(w, "Shop quote storage is unavailable", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req ShopQuoteRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "Invalid shop quote payload", http.StatusBadRequest)
			return
		}

		var (
			response ShopQuoteResponse
			err      error
		)
		if strings.TrimSpace(req.QuoteID) == "" {
			response, err = createShopQuote(r, cfg, db, claims, req, clientFactory, now)
		} else {
			response, err = selectShopQuoteDelivery(r, cfg, db, claims, req, clientFactory, now)
		}
		if err != nil {
			writeShopQuoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

type shopQuoteError struct {
	Status  int
	Message string
}

func (e *shopQuoteError) Error() string { return e.Message }

func quoteError(status int, message string) error {
	return &shopQuoteError{Status: status, Message: message}
}

func writeShopQuoteError(w http.ResponseWriter, err error) {
	if typed, ok := err.(*shopQuoteError); ok {
		http.Error(w, typed.Message, typed.Status)
		return
	}
	http.Error(w, "Unable to create Shopify quote", http.StatusBadGateway)
}

func createShopQuote(
	r *http.Request,
	cfg *config.Config,
	db *gorm.DB,
	claims *auth.Claims,
	req ShopQuoteRequest,
	clientFactory storefrontQuoteClientFactory,
	now func() time.Time,
) (ShopQuoteResponse, error) {
	lines, err := validateShopifyQuoteLines(req.LineItems)
	if err != nil {
		return ShopQuoteResponse{}, err
	}
	if err := validateShopQuoteCustomer(claims, req.Customer); err != nil {
		return ShopQuoteResponse{}, err
	}
	if err := validateHongKongShipping(req.Shipping); err != nil {
		return ShopQuoteResponse{}, quoteError(http.StatusBadRequest, err.Error())
	}
	discountCode := strings.TrimSpace(req.DiscountCode)
	if len(discountCode) > 255 {
		return ShopQuoteResponse{}, quoteError(http.StatusBadRequest, "Discount code is too long")
	}
	client, err := clientFactory(cfg)
	if err != nil {
		return ShopQuoteResponse{}, quoteError(http.StatusServiceUnavailable, "Shopify quote service is not configured")
	}
	storefrontQuote, err := client.CreateCartQuote(r.Context(), shopify.StorefrontQuoteRequest{
		Lines:        lines,
		Email:        strings.TrimSpace(req.Customer.Email),
		Phone:        strings.TrimSpace(req.Customer.Phone),
		DiscountCode: discountCode,
		BuyerIP:      shopBuyerIP(r),
		Shipping: shopify.StorefrontQuoteAddress{
			RecipientName: strings.TrimSpace(req.Shipping.RecipientName),
			Phone:         strings.TrimSpace(req.Shipping.Phone),
			Address1:      strings.TrimSpace(req.Shipping.Address1),
			District:      strings.TrimSpace(req.Shipping.District),
			Region:        strings.TrimSpace(req.Shipping.Region),
		},
	})
	if err != nil {
		return ShopQuoteResponse{}, quoteError(http.StatusUnprocessableEntity, err.Error())
	}
	if !sameRequestedQuoteLines(lines, storefrontQuote.Lines) {
		return ShopQuoteResponse{}, quoteError(
			http.StatusUnprocessableEntity,
			"Shopify adjusted the requested merchandise or quantity; review the cart and request a new quote",
		)
	}
	requiresShipping := storefrontQuoteRequiresShipping(storefrontQuote)
	if requiresShipping && len(storefrontQuote.DeliveryOptions) == 0 {
		return ShopQuoteResponse{}, quoteError(http.StatusUnprocessableEntity, "No Shopify delivery option is available for this Hong Kong address")
	}

	quotedAt := now().UTC()
	snapshot := shopQuoteSnapshot(
		claims.UserID,
		models.ShopQuoteCustomer{
			Name:  strings.TrimSpace(req.Customer.Name),
			Email: strings.TrimSpace(req.Customer.Email),
			Phone: strings.TrimSpace(req.Customer.Phone),
		},
		models.ShopQuoteShipping{
			RecipientName: strings.TrimSpace(req.Shipping.RecipientName),
			Phone:         strings.TrimSpace(req.Shipping.Phone),
			Address1:      strings.TrimSpace(req.Shipping.Address1),
			District:      strings.TrimSpace(req.Shipping.District),
			Region:        strings.TrimSpace(req.Shipping.Region),
			CountryCode:   "HK",
		},
		storefrontQuote,
		quotedAt,
		quotedAt.Add(shopQuoteTTL(cfg)),
		false,
	)
	record := models.ShopCheckoutQuote{ID: uuid.NewString()}
	if err := record.SetSnapshot(snapshot); err != nil {
		return ShopQuoteResponse{}, err
	}
	if err := db.Create(&record).Error; err != nil {
		return ShopQuoteResponse{}, fmt.Errorf("persist shop quote: %w", err)
	}
	return shopQuoteResponse(record.ID, record.SnapshotSHA256, snapshot), nil
}

func selectShopQuoteDelivery(
	r *http.Request,
	cfg *config.Config,
	db *gorm.DB,
	claims *auth.Claims,
	req ShopQuoteRequest,
	clientFactory storefrontQuoteClientFactory,
	now func() time.Time,
) (ShopQuoteResponse, error) {
	quoteID := strings.TrimSpace(req.QuoteID)
	expectedVersion := strings.ToLower(strings.TrimSpace(req.Version))
	if expectedVersion == "" {
		return ShopQuoteResponse{}, quoteError(http.StatusBadRequest, "version is required when selecting delivery")
	}
	handle := strings.TrimSpace(req.SelectedDeliveryOptionHandle)
	if handle == "" {
		return ShopQuoteResponse{}, quoteError(http.StatusBadRequest, "selectedDeliveryOptionHandle is required")
	}
	var record models.ShopCheckoutQuote
	if err := db.Where("id = ? AND user_id = ?", quoteID, strings.TrimSpace(claims.UserID)).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ShopQuoteResponse{}, quoteError(http.StatusNotFound, "Shop quote not found")
		}
		return ShopQuoteResponse{}, fmt.Errorf("load shop quote: %w", err)
	}
	currentTime := now().UTC()
	if record.ConsumedAt != nil || record.Status == models.ShopQuoteStatusConsumed {
		return ShopQuoteResponse{}, quoteError(http.StatusConflict, "Shop quote has already been used")
	}
	if !currentTime.Before(record.ExpiresAt) {
		return ShopQuoteResponse{}, quoteError(http.StatusGone, "Shop quote has expired")
	}
	previousVersion := strings.ToLower(strings.TrimSpace(record.SnapshotSHA256))
	previousStatus := record.Status
	if expectedVersion != previousVersion {
		return ShopQuoteResponse{}, quoteError(http.StatusConflict, "Shop quote changed; refresh the quote before selecting delivery")
	}
	previous, err := record.DecodeAndVerifySnapshot()
	if err != nil {
		return ShopQuoteResponse{}, fmt.Errorf("verify shop quote: %w", err)
	}
	var selected *models.ShopQuoteDeliveryOption
	for index := range previous.DeliveryOptions {
		if previous.DeliveryOptions[index].Handle == handle {
			selected = &previous.DeliveryOptions[index]
			break
		}
	}
	if selected == nil {
		return ShopQuoteResponse{}, quoteError(http.StatusBadRequest, "Selected delivery option is not part of this quote")
	}

	client, err := clientFactory(cfg)
	if err != nil {
		return ShopQuoteResponse{}, quoteError(http.StatusServiceUnavailable, "Shopify quote service is not configured")
	}
	updated, err := client.SelectCartDelivery(r.Context(), record.ShopifyCartID, shopify.StorefrontDeliverySelection{
		DeliveryGroupID:      selected.DeliveryGroupID,
		DeliveryOptionHandle: selected.Handle,
	}, shopBuyerIP(r))
	if err != nil {
		return ShopQuoteResponse{}, quoteError(http.StatusUnprocessableEntity, err.Error())
	}
	if updated.CartID != record.ShopifyCartID || !sameQuotedItems(previous.LineItems, updated.Lines) {
		return ShopQuoteResponse{}, quoteError(http.StatusConflict, "Shopify cart changed; request a new quote")
	}
	if updated.SelectedDeliveryOption == nil ||
		updated.SelectedDeliveryOption.DeliveryGroupID != selected.DeliveryGroupID ||
		updated.SelectedDeliveryOption.Handle != selected.Handle {
		return ShopQuoteResponse{}, quoteError(http.StatusConflict, "Shopify did not accept the selected delivery option")
	}

	snapshot := shopQuoteSnapshot(
		claims.UserID,
		previous.Customer,
		previous.Shipping,
		updated,
		currentTime,
		currentTime.Add(shopQuoteTTL(cfg)),
		true,
	)
	record.ID = quoteID
	if err := record.SetSnapshot(snapshot); err != nil {
		return ShopQuoteResponse{}, err
	}
	result := db.Model(&models.ShopCheckoutQuote{}).
		Where(
			"id = ? AND user_id = ? AND consumed_at IS NULL AND status = ? AND snapshot_sha256 = ?",
			quoteID,
			claims.UserID,
			previousStatus,
			previousVersion,
		).
		Updates(shopQuoteUpdateColumns(record))
	if result.Error != nil {
		return ShopQuoteResponse{}, fmt.Errorf("update selected shop quote: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ShopQuoteResponse{}, quoteError(http.StatusConflict, "Shop quote is no longer available")
	}
	return shopQuoteResponse(record.ID, record.SnapshotSHA256, snapshot), nil
}

func validateShopifyQuoteLines(items []ShopCheckoutLineItemRequest) ([]shopify.StorefrontQuoteLineInput, error) {
	if len(items) == 0 {
		return nil, quoteError(http.StatusBadRequest, "At least one line item is required")
	}
	if len(items) > maxShopCheckoutLines {
		return nil, quoteError(http.StatusBadRequest, fmt.Sprintf("A checkout supports at most %d line items", maxShopCheckoutLines))
	}
	quantities := make(map[string]int, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		source := strings.ToLower(strings.TrimSpace(item.Source))
		if source == "" {
			source = "shopify"
		}
		switch source {
		case "shopify":
		case "hicustom":
			return nil, quoteError(http.StatusUnprocessableEntity, "HiCustom checkout is disabled until factory fulfillment is production-ready")
		default:
			return nil, quoteError(http.StatusBadRequest, fmt.Sprintf("Unsupported shop source %q", source))
		}
		if item.Quantity <= 0 || item.Quantity > maxShopCheckoutQuantity {
			return nil, quoteError(http.StatusBadRequest, fmt.Sprintf("Quantity must be between 1 and %d", maxShopCheckoutQuantity))
		}
		variantID := strings.TrimSpace(item.VariantID)
		if !strings.HasPrefix(variantID, "gid://shopify/ProductVariant/") {
			return nil, quoteError(http.StatusBadRequest, "A valid Shopify variantId is required")
		}
		if _, exists := quantities[variantID]; !exists {
			order = append(order, variantID)
		}
		quantities[variantID] += item.Quantity
		if quantities[variantID] > maxShopCheckoutQuantity {
			return nil, quoteError(http.StatusBadRequest, fmt.Sprintf("Combined variant quantity must not exceed %d", maxShopCheckoutQuantity))
		}
	}
	lines := make([]shopify.StorefrontQuoteLineInput, 0, len(order))
	for _, variantID := range order {
		lines = append(lines, shopify.StorefrontQuoteLineInput{
			VariantID: variantID,
			Quantity:  quantities[variantID],
		})
	}
	return lines, nil
}

func validateShopQuoteCustomer(claims *auth.Claims, customer ShopCheckoutCustomerRequest) error {
	email := strings.ToLower(strings.TrimSpace(customer.Email))
	if email == "" {
		return quoteError(http.StatusBadRequest, "Customer email is required")
	}
	if claimEmail := strings.ToLower(strings.TrimSpace(claims.Email)); claimEmail == "" || claimEmail != email {
		return quoteError(http.StatusForbidden, "Checkout email must match the authenticated account")
	}
	if strings.TrimSpace(customer.Name) == "" {
		return quoteError(http.StatusBadRequest, "Customer name is required")
	}
	return nil
}

func shopQuoteSnapshot(
	userID string,
	customer models.ShopQuoteCustomer,
	shipping models.ShopQuoteShipping,
	quote *shopify.StorefrontQuote,
	quotedAt time.Time,
	expiresAt time.Time,
	deliveryConfirmed bool,
) models.ShopQuoteSnapshot {
	quotedAt = quotedAt.UTC().Truncate(time.Microsecond)
	expiresAt = expiresAt.UTC().Truncate(time.Microsecond)
	status := models.ShopQuoteStatusReady
	if storefrontQuoteRequiresShipping(quote) && !deliveryConfirmed {
		status = models.ShopQuoteStatusDeliveryRequired
	} else if quote.DiscountCode != "" && !quote.DiscountCodeApplicable {
		status = models.ShopQuoteStatusDiscountInvalid
	}
	snapshot := models.ShopQuoteSnapshot{
		Version:              models.ShopQuoteSnapshotVersion,
		ShopifyCartID:        quote.CartID,
		ShopifyCartUpdatedAt: quote.CartUpdatedAt.UTC().Truncate(time.Microsecond),
		UserID:               strings.TrimSpace(userID),
		Status:               status,
		Currency:             strings.ToUpper(quote.Currency),
		Discount: models.ShopQuoteDiscount{
			Code:       quote.DiscountCode,
			Applicable: quote.DiscountCodeApplicable,
			TargetType: quote.DiscountTargetType,
		},
		Amounts: models.ShopQuoteAmounts{
			SubtotalAmountMinor: quote.SubtotalAmountMinor,
			DiscountAmountMinor: quote.DiscountAmountMinor,
			ShippingAmountMinor: quote.ShippingAmountMinor,
			TaxAmountMinor:      quote.TaxAmountMinor,
			TotalAmountMinor:    quote.TotalAmountMinor,
		},
		Customer: models.ShopQuoteCustomer{
			Name:  strings.TrimSpace(customer.Name),
			Email: strings.TrimSpace(customer.Email),
			Phone: strings.TrimSpace(customer.Phone),
		},
		Shipping: models.ShopQuoteShipping{
			RecipientName: strings.TrimSpace(shipping.RecipientName),
			Phone:         strings.TrimSpace(shipping.Phone),
			Address1:      strings.TrimSpace(shipping.Address1),
			District:      strings.TrimSpace(shipping.District),
			Region:        strings.TrimSpace(shipping.Region),
			CountryCode:   "HK",
		},
		Warnings:  quote.Warnings,
		QuotedAt:  quotedAt,
		ExpiresAt: expiresAt,
	}
	for _, line := range quote.Lines {
		snapshot.LineItems = append(snapshot.LineItems, models.ShopQuoteSnapshotItem{
			Source:           "shopify",
			Handle:           line.Handle,
			VariantID:        line.VariantID,
			Title:            line.Title,
			VariantTitle:     line.VariantTitle,
			ImageURL:         line.ImageURL,
			Quantity:         line.Quantity,
			UnitAmountMinor:  line.UnitAmountMinor,
			RequiresShipping: line.RequiresShipping,
		})
	}
	for _, option := range quote.DeliveryOptions {
		snapshot.DeliveryOptions = append(snapshot.DeliveryOptions, quoteDeliveryOption(option))
	}
	if quote.SelectedDeliveryOption != nil {
		selected := quoteDeliveryOption(*quote.SelectedDeliveryOption)
		snapshot.SelectedDeliveryOption = &selected
	}
	return snapshot
}

func shopQuoteTTL(cfg *config.Config) time.Duration {
	seconds := cfg.ShopCheckoutQuoteTTLSeconds
	if seconds <= 0 {
		seconds = 600
	}
	return time.Duration(seconds) * time.Second
}

func quoteDeliveryOption(option shopify.StorefrontDeliveryOption) models.ShopQuoteDeliveryOption {
	return models.ShopQuoteDeliveryOption{
		DeliveryGroupID: option.DeliveryGroupID,
		Handle:          option.Handle,
		Code:            option.Code,
		Title:           option.Title,
		Description:     option.Description,
		DeliveryMethod:  option.DeliveryMethod,
		AmountMinor:     option.AmountMinor,
		Currency:        option.Currency,
	}
}

func storefrontQuoteRequiresShipping(quote *shopify.StorefrontQuote) bool {
	for _, line := range quote.Lines {
		if line.RequiresShipping {
			return true
		}
	}
	return false
}

func sameQuotedItems(previous []models.ShopQuoteSnapshotItem, updated []shopify.StorefrontQuoteLine) bool {
	expected := make(map[string]int, len(previous))
	for _, line := range previous {
		variantID := strings.TrimSpace(line.VariantID)
		if variantID == "" || line.Quantity <= 0 {
			return false
		}
		expected[variantID] += line.Quantity
	}
	actual := storefrontQuoteLineQuantities(updated)
	return sameQuoteLineQuantities(expected, actual)
}

func sameRequestedQuoteLines(
	requested []shopify.StorefrontQuoteLineInput,
	quoted []shopify.StorefrontQuoteLine,
) bool {
	expected := make(map[string]int, len(requested))
	for _, line := range requested {
		variantID := strings.TrimSpace(line.VariantID)
		if variantID == "" || line.Quantity <= 0 {
			return false
		}
		expected[variantID] += line.Quantity
	}
	actual := storefrontQuoteLineQuantities(quoted)
	return sameQuoteLineQuantities(expected, actual)
}

func storefrontQuoteLineQuantities(lines []shopify.StorefrontQuoteLine) map[string]int {
	quantities := make(map[string]int, len(lines))
	for _, line := range lines {
		variantID := strings.TrimSpace(line.VariantID)
		if variantID == "" || line.Quantity <= 0 {
			return nil
		}
		quantities[variantID] += line.Quantity
	}
	return quantities
}

func sameQuoteLineQuantities(expected, actual map[string]int) bool {
	if len(expected) == 0 || len(expected) != len(actual) {
		return false
	}
	for variantID, quantity := range expected {
		if actual[variantID] != quantity {
			return false
		}
	}
	return true
}

func shopQuoteResponse(quoteID, version string, snapshot models.ShopQuoteSnapshot) ShopQuoteResponse {
	return ShopQuoteResponse{
		QuoteID:                quoteID,
		Version:                strings.ToLower(strings.TrimSpace(version)),
		Status:                 snapshot.Status,
		ExpiresAt:              snapshot.ExpiresAt,
		Currency:               snapshot.Currency,
		LineItems:              snapshot.LineItems,
		DeliveryOptions:        snapshot.DeliveryOptions,
		SelectedDeliveryOption: snapshot.SelectedDeliveryOption,
		Discount:               snapshot.Discount,
		Amounts:                snapshot.Amounts,
		Warnings:               snapshot.Warnings,
	}
}

func shopQuoteUpdateColumns(record models.ShopCheckoutQuote) map[string]any {
	return map[string]any{
		"shopify_cart_id":                 record.ShopifyCartID,
		"status":                          record.Status,
		"currency":                        record.Currency,
		"subtotal_amount_minor":           record.SubtotalAmountMinor,
		"discount_amount_minor":           record.DiscountAmountMinor,
		"shipping_amount_minor":           record.ShippingAmountMinor,
		"tax_amount_minor":                record.TaxAmountMinor,
		"total_amount_minor":              record.TotalAmountMinor,
		"discount_code":                   record.DiscountCode,
		"discount_code_applicable":        record.DiscountCodeApplicable,
		"delivery_group_id":               record.DeliveryGroupID,
		"selected_delivery_option_handle": record.SelectedDeliveryOptionHandle,
		"snapshot_json":                   record.SnapshotJSON,
		"snapshot_sha256":                 record.SnapshotSHA256,
		"expires_at":                      record.ExpiresAt,
		"updated_at":                      time.Now().UTC(),
	}
}

func authenticatedShopClaims(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return nil, false
	}
	claims, err := auth.ValidateToken(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return nil, false
	}
	if strings.TrimSpace(claims.UserID) == "" {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return nil, false
	}
	return claims, true
}

func shopBuyerIP(r *http.Request) string {
	for _, candidate := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if ip := net.ParseIP(strings.TrimSpace(candidate)); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); ip != nil {
		return ip.String()
	}
	return ""
}
