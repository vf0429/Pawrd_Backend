package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

type ShopCheckoutLineItemRequest struct {
	Handle    string `json:"handle"`
	VariantID string `json:"variantId"`
	Quantity  int    `json:"quantity"`
	// Source identifies the fulfillment pipeline. Empty defaults to "shopify".
	// "hicustom" is reserved for Phase C (custom products); checkout currently
	// only emits shopify items. See docs/hicustom_integration_design.md §13.
	Source string `json:"source,omitempty"`
}

// ShopCheckoutCustomerRequest is DEPRECATED and ignored: the customer identity
// is derived server-side from the JWT user's AuthUser account. The field is
// kept only so older iOS clients that still send it don't break.
type ShopCheckoutCustomerRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type ShopPaymentSheetRequest struct {
	LineItems []ShopCheckoutLineItemRequest `json:"lineItems"`
	Customer  ShopCheckoutCustomerRequest   `json:"customer"`
	Shipping  ShopCheckoutShippingRequest   `json:"shipping"`
}

type ShopCheckoutShippingRequest struct {
	RecipientName string `json:"recipientName"`
	Phone         string `json:"phone"`
	Address1      string `json:"address1"`
	District      string `json:"district"`
	Region        string `json:"region"`
}

type ShopPaymentSheetResponse struct {
	PaymentIntentClientSecret string `json:"paymentIntentClientSecret"`
	PublishableKey            string `json:"publishableKey"`
	MerchantDisplayName       string `json:"merchantDisplayName"`
	Amount                    int64  `json:"amount"`
	Currency                  string `json:"currency"`
	OrderID                   string `json:"orderId"`
	PaymentIntentID           string `json:"paymentIntentId"`
}

func NewShopPaymentSheetHandler(cfg *config.Config, db *gorm.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		var req ShopPaymentSheetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid checkout payload", http.StatusBadRequest)
			return
		}

		if len(req.LineItems) == 0 {
			http.Error(w, "At least one line item is required", http.StatusBadRequest)
			return
		}

		// Customer identity is server-authoritative: derive it from the JWT
		// user's account, never from the client-sent customer object.
		var account models.AuthUser
		if err := models.AuthDB.First(&account, "id = ?", userID).Error; err != nil {
			http.Error(w, "Account not found", http.StatusNotFound)
			return
		}
		customerEmail := strings.TrimSpace(account.Email)
		if customerEmail == "" {
			http.Error(w, "Account email is missing", http.StatusBadRequest)
			return
		}

		if err := validateHongKongShipping(req.Shipping); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !cfg.UseMockShopify {
			if err := cfg.ValidateShopifyAdminConfig(); err != nil {
				http.Error(w, "Shopify order service is not configured", http.StatusServiceUnavailable)
				return
			}
		}

		shopifyClient, err := newShopifyClient(cfg)
		if err != nil {
			http.Error(w, "Shopify configuration error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		amount, currency, description, metadata, orderItems, err := buildCheckoutPaymentData(shopifyClient, db, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		orderID := uuid.NewString()
		// PaymentIntent metadata is minimized to operational linkage only — no
		// customer PII, no address, no user id (the webhook resolves the user
		// through the order row). The receipt email travels via Stripe's
		// dedicated ReceiptEmail field, not metadata.
		metadata["pawrd_order_id"] = orderID

		// Step 1 — initialize/validate the Stripe service BEFORE persisting
		// anything: a config failure means no payment attempt was ever
		// possible, so no durable order row is needed.
		stripeService, err := newPaymentIntentService(cfg)
		if err != nil {
			http.Error(w, "Stripe configuration error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Step 2 — durable order: full immutable shipping snapshot with a
		// NULL payment_intent_id (back-filled after the Stripe call succeeds).
		// A failed intent creation or a client abandon leaves a reconcilable
		// record instead of an orphan Stripe intent.
		order := models.ShopOrder{
			ID:               orderID,
			UserID:           userID,
			PaymentIntentID:  nil,
			Status:           "pending_payment",
			FinancialStatus:  "pending",
			Currency:         strings.ToUpper(currency),
			TotalAmountMinor: amount,
			CustomerName:     strings.TrimSpace(req.Shipping.RecipientName),
			CustomerEmail:    customerEmail,
			CustomerPhone:    strings.TrimSpace(req.Shipping.Phone),
			ShippingAddress1: strings.TrimSpace(req.Shipping.Address1),
			ShippingDistrict: strings.TrimSpace(req.Shipping.District),
			ShippingRegion:   strings.TrimSpace(req.Shipping.Region),
			ShippingCountry:  "Hong Kong",
			Items:            orderItems,
		}
		for index := range order.Items {
			order.Items[index].OrderID = orderID
		}
		if err := db.Create(&order).Error; err != nil {
			log.Printf("[shop-checkout] persist order failed order=%s: %v", orderID, err)
			http.Error(w, "Failed to persist checkout order", http.StatusInternalServerError)
			return
		}

		// Step 3 — create the Stripe PaymentIntent.
		intent, err := stripeService.CreatePaymentIntent(payments.CreatePaymentIntentRequest{
			Amount:        amount,
			Currency:      currency,
			Description:   description,
			ReceiptEmail:  customerEmail,
			Metadata:      metadata,
			StatementNote: "PAWRD",
		})
		if err != nil {
			// The order stays as the durable record of the attempt.
			reason := truncateFailureReason("stripe payment intent creation failed: " + err.Error())
			if uerr := db.Model(&models.ShopOrder{}).Where("id = ?", orderID).
				Updates(map[string]any{"status": "payment_failed", "financial_status": "failed", "failure_reason": reason}).Error; uerr != nil {
				log.Printf("[shop-checkout] CRITICAL: order %s could not be marked payment_failed after Stripe error: %v", orderID, uerr)
			}
			http.Error(w, "Failed to create payment intent: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Step 4 — back-fill the intent id. If this update fails the order still
		// exists and the intent carries pawrd_order_id, so the webhook can
		// reconcile; log loudly and let the checkout proceed.
		if err := db.Model(&models.ShopOrder{}).Where("id = ?", orderID).
			Update("payment_intent_id", intent.PaymentIntentID).Error; err != nil {
			log.Printf("[shop-checkout] CRITICAL: payment intent %s created for order %s but back-fill failed: %v — reconcile via pawrd_order_id metadata",
				intent.PaymentIntentID, orderID, err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ShopPaymentSheetResponse{
			PaymentIntentClientSecret: intent.ClientSecret,
			PublishableKey:            intent.PublishableKey,
			MerchantDisplayName:       "Pawrd",
			Amount:                    amount,
			Currency:                  strings.ToLower(currency),
			OrderID:                   orderID,
			PaymentIntentID:           intent.PaymentIntentID,
		})
	}
}

func truncateFailureReason(reason string) string {
	const max = 500
	if len(reason) > max {
		return reason[:max]
	}
	return reason
}

// paymentIntentService abstracts the Stripe boundary so tests can stub it.
type paymentIntentService interface {
	CreatePaymentIntent(payments.CreatePaymentIntentRequest) (*payments.CreatePaymentIntentResponse, error)
}

// newPaymentIntentService is a package-level seam, swapped out in tests.
var newPaymentIntentService = func(cfg *config.Config) (paymentIntentService, error) {
	return payments.NewStripeService(cfg)
}

func buildCheckoutPaymentData(client ShopifyClient, db *gorm.DB, req ShopPaymentSheetRequest) (int64, string, string, map[string]string, []models.ShopOrderItem, error) {
	var totalAmount int64
	var currency string
	var totalQuantity int
	var itemDescriptions []string
	metadata := map[string]string{}
	orderItems := make([]models.ShopOrderItem, 0, len(req.LineItems))

	for index, item := range req.LineItems {
		if item.Quantity <= 0 {
			return 0, "", "", nil, nil, fmt.Errorf("quantity must be greater than zero")
		}

		source := strings.TrimSpace(strings.ToLower(item.Source))
		if source == "" {
			source = "shopify"
		}

		var title, lineCurrency, linePrice, metaHandle, metaVariant, imageURL string

		switch source {
		case "hicustom":
			// HiCustom line item: price comes from the cached BlankProduct (by SKU).
			// `Handle` carries the blank SKU for hicustom items (see transformBlankProduct).
			sku := strings.TrimSpace(item.Handle)
			if sku == "" {
				return 0, "", "", nil, nil, fmt.Errorf("hicustom line item sku is required")
			}
			if db == nil {
				return 0, "", "", nil, nil, fmt.Errorf("hicustom checkout requires a database connection")
			}
			bp, err := blankProductPrice(db, sku)
			if err != nil {
				return 0, "", "", nil, nil, fmt.Errorf("failed to fetch blank product '%s': %w", sku, err)
			}
			if !bp.Available {
				return 0, "", "", nil, nil, fmt.Errorf("blank product '%s' is currently unavailable", bp.Title)
			}
			title = bp.Title
			lineCurrency = strings.ToLower(strings.TrimSpace(bp.CurrencyCode))
			linePrice = bp.Price
			metaHandle = sku
			// customProductId travels via VariantID field reuse so the webhook
			// can push the exact design to HiCustom. TODO: add a dedicated field.
			metaVariant = strings.TrimSpace(item.VariantID)

		default: // "shopify"
			handle := strings.TrimSpace(item.Handle)
			if handle == "" {
				return 0, "", "", nil, nil, fmt.Errorf("line item handle is required")
			}
			product, err := client.FetchProductByHandle(handle)
			if err != nil {
				return 0, "", "", nil, nil, fmt.Errorf("failed to fetch product '%s': %w", handle, err)
			}
			variant, err := findCheckoutVariant(product, item.VariantID)
			if err != nil {
				return 0, "", "", nil, nil, err
			}
			if !variant.AvailableForSale {
				return 0, "", "", nil, nil, fmt.Errorf("variant '%s' is currently unavailable", variant.Title)
			}
			title = product.Title
			lineCurrency = strings.ToLower(strings.TrimSpace(variant.Price.CurrencyCode))
			linePrice = variant.Price.Amount
			metaHandle = product.Handle
			metaVariant = variant.ID
			if variant.Image != nil {
				imageURL = variant.Image.URL
			} else if len(product.Images) > 0 {
				imageURL = product.Images[0].URL
			}
		}

		if lineCurrency == "" {
			return 0, "", "", nil, nil, fmt.Errorf("product '%s' is missing currency code", title)
		}
		if currency == "" {
			currency = lineCurrency
		} else if currency != lineCurrency {
			return 0, "", "", nil, nil, fmt.Errorf("all items in a checkout must use the same currency")
		}

		unitAmount, err := parseAmountToMinorUnits(linePrice)
		if err != nil {
			return 0, "", "", nil, nil, fmt.Errorf("invalid price for product '%s': %w", title, err)
		}

		totalAmount += unitAmount * int64(item.Quantity)
		totalQuantity += item.Quantity
		itemDescriptions = append(itemDescriptions, fmt.Sprintf("%s x%d", title, item.Quantity))

		// source-tagged so the Stripe webhook can route fulfillment by pipeline.
		// Parsed by payments.ParseItemsFromMetadata.
		metadata[fmt.Sprintf("item_%d", index+1)] = fmt.Sprintf(
			"source=%s | handle=%s | variant=%s | qty:%d",
			source, metaHandle, metaVariant, item.Quantity,
		)
		orderItems = append(orderItems, models.ShopOrderItem{
			ID:              uuid.NewString(),
			Source:          source,
			Handle:          metaHandle,
			VariantID:       metaVariant,
			Title:           title,
			ImageURL:        imageURL,
			Quantity:        item.Quantity,
			UnitAmountMinor: unitAmount,
			Currency:        strings.ToUpper(lineCurrency),
		})
	}

	metadata["total_items"] = strconv.Itoa(totalQuantity)

	description := fmt.Sprintf("Pawrd order (%d item(s))", totalQuantity)
	if len(itemDescriptions) > 0 {
		description = "Pawrd: " + strings.Join(itemDescriptions, ", ")
	}

	return totalAmount, currency, description, metadata, orderItems, nil
}

// hongKongDistricts maps each delivery region to its canonical districts.
// iOS must send exactly these strings (region + district pickers).
var hongKongDistricts = map[string][]string{
	"Hong Kong Island": {"Central and Western", "Wan Chai", "Eastern", "Southern"},
	"Kowloon":          {"Yau Tsim Mong", "Sham Shui Po", "Kowloon City", "Wong Tai Sin", "Kwun Tong"},
	"New Territories":  {"Kwai Tsing", "Tsuen Wan", "Tuen Mun", "Yuen Long", "North", "Tai Po", "Sha Tin", "Sai Kung", "Islands"},
}

const (
	maxShippingRecipientLen = 100
	maxShippingPhoneLen     = 32
	maxShippingAddressLen   = 200
	maxShippingDistrictLen  = 50
	maxShippingRegionLen    = 50
)

func validateHongKongShipping(shipping ShopCheckoutShippingRequest) error {
	recipient := strings.TrimSpace(shipping.RecipientName)
	address1 := strings.TrimSpace(shipping.Address1)
	district := strings.TrimSpace(shipping.District)
	region := strings.TrimSpace(shipping.Region)

	if recipient == "" || address1 == "" || district == "" || region == "" {
		return fmt.Errorf("complete Hong Kong shipping address is required")
	}
	if len(recipient) > maxShippingRecipientLen || len(address1) > maxShippingAddressLen ||
		len(district) > maxShippingDistrictLen || len(region) > maxShippingRegionLen ||
		len(strings.TrimSpace(shipping.Phone)) > maxShippingPhoneLen {
		return fmt.Errorf("shipping fields exceed the maximum length")
	}

	districts, regionKnown := hongKongDistricts[region]
	if !regionKnown {
		return fmt.Errorf("unknown Hong Kong region '%s'", region)
	}
	districtKnown := false
	for _, d := range districts {
		if d == district {
			districtKnown = true
			break
		}
	}
	if !districtKnown {
		return fmt.Errorf("unknown district '%s' for region '%s'", district, region)
	}

	phone := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(shipping.Phone))
	phone = strings.TrimPrefix(phone, "+852")
	if len(phone) != 8 {
		return fmt.Errorf("Hong Kong phone number must contain 8 digits")
	}
	for _, char := range phone {
		if char < '0' || char > '9' {
			return fmt.Errorf("Hong Kong phone number must contain 8 digits")
		}
	}
	if phone[0] < '2' || phone[0] > '9' {
		return fmt.Errorf("Hong Kong phone number must start with 2-9")
	}
	return nil
}

func findCheckoutVariant(product *shopify.Product, variantID string) (*shopify.Variant, error) {
	trimmedVariantID := strings.TrimSpace(variantID)
	if trimmedVariantID != "" {
		for index := range product.Variants {
			if product.Variants[index].ID == trimmedVariantID {
				return &product.Variants[index], nil
			}
		}
		return nil, fmt.Errorf("variant '%s' was not found for product '%s'", trimmedVariantID, product.Title)
	}

	if len(product.Variants) == 0 {
		return nil, fmt.Errorf("product '%s' has no purchasable variants", product.Title)
	}

	return &product.Variants[0], nil
}

func parseAmountToMinorUnits(amount string) (int64, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil {
		return 0, err
	}

	return int64(math.Round(parsed * 100)), nil
}
