package handlers

import (
	"encoding/json"
	"fmt"
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

		customerEmail := strings.TrimSpace(req.Customer.Email)
		if customerEmail == "" {
			http.Error(w, "Customer email is required", http.StatusBadRequest)
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
		metadata["pawrd_order_id"] = orderID
		metadata["user_id"] = userID
		metadata["customer_email"] = customerEmail

		stripeService, err := payments.NewStripeService(cfg)
		if err != nil {
			http.Error(w, "Stripe configuration error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		intent, err := stripeService.CreatePaymentIntent(payments.CreatePaymentIntentRequest{
			Amount:        amount,
			Currency:      currency,
			Description:   description,
			ReceiptEmail:  customerEmail,
			Metadata:      metadata,
			StatementNote: "PAWRD",
		})
		if err != nil {
			http.Error(w, "Failed to create payment intent: "+err.Error(), http.StatusInternalServerError)
			return
		}
		order := models.ShopOrder{
			ID:               orderID,
			UserID:           userID,
			PaymentIntentID:  intent.PaymentIntentID,
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
			http.Error(w, "Failed to persist checkout order", http.StatusInternalServerError)
			return
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

	metadata["customer_name"] = strings.TrimSpace(req.Customer.Name)
	metadata["customer_phone"] = strings.TrimSpace(req.Customer.Phone)
	metadata["total_items"] = strconv.Itoa(totalQuantity)

	description := fmt.Sprintf("Pawrd order (%d item(s))", totalQuantity)
	if len(itemDescriptions) > 0 {
		description = "Pawrd: " + strings.Join(itemDescriptions, ", ")
	}

	return totalAmount, currency, description, metadata, orderItems, nil
}

func validateHongKongShipping(shipping ShopCheckoutShippingRequest) error {
	if strings.TrimSpace(shipping.RecipientName) == "" || strings.TrimSpace(shipping.Address1) == "" ||
		strings.TrimSpace(shipping.District) == "" || strings.TrimSpace(shipping.Region) == "" {
		return fmt.Errorf("complete Hong Kong shipping address is required")
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
