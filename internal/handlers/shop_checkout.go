package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/vf0429/Petwell_Backend/internal/config"
	"github.com/vf0429/Petwell_Backend/internal/services/payments"
	"github.com/vf0429/Petwell_Backend/internal/services/shopify"
)

type ShopCheckoutLineItemRequest struct {
	Handle    string `json:"handle"`
	VariantID string `json:"variantId"`
	Quantity  int    `json:"quantity"`
}

type ShopCheckoutCustomerRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type ShopPaymentSheetRequest struct {
	LineItems []ShopCheckoutLineItemRequest `json:"lineItems"`
	Customer  ShopCheckoutCustomerRequest   `json:"customer"`
}

type ShopPaymentSheetResponse struct {
	PaymentIntentClientSecret string `json:"paymentIntentClientSecret"`
	PublishableKey            string `json:"publishableKey"`
	MerchantDisplayName       string `json:"merchantDisplayName"`
	Amount                    int64  `json:"amount"`
	Currency                  string `json:"currency"`
}

func NewShopPaymentSheetHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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

		shopifyClient, err := newShopifyClient(cfg)
		if err != nil {
			http.Error(w, "Shopify configuration error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		amount, currency, description, metadata, err := buildCheckoutPaymentData(shopifyClient, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

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
			StatementNote: "PETWELL",
		})
		if err != nil {
			http.Error(w, "Failed to create payment intent: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ShopPaymentSheetResponse{
			PaymentIntentClientSecret: intent.ClientSecret,
			PublishableKey:            intent.PublishableKey,
			MerchantDisplayName:       "PetWell",
			Amount:                    amount,
			Currency:                  strings.ToLower(currency),
		})
	}
}

func buildCheckoutPaymentData(client ShopifyClient, req ShopPaymentSheetRequest) (int64, string, string, map[string]string, error) {
	var totalAmount int64
	var currency string
	var totalQuantity int
	var itemDescriptions []string
	metadata := map[string]string{}

	for index, item := range req.LineItems {
		handle := strings.TrimSpace(item.Handle)
		if handle == "" {
			return 0, "", "", nil, fmt.Errorf("line item handle is required")
		}
		if item.Quantity <= 0 {
			return 0, "", "", nil, fmt.Errorf("quantity must be greater than zero")
		}

		product, err := client.FetchProductByHandle(handle)
		if err != nil {
			return 0, "", "", nil, fmt.Errorf("failed to fetch product '%s': %w", handle, err)
		}

		variant, err := findCheckoutVariant(product, item.VariantID)
		if err != nil {
			return 0, "", "", nil, err
		}
		if !variant.AvailableForSale {
			return 0, "", "", nil, fmt.Errorf("variant '%s' is currently unavailable", variant.Title)
		}

		lineCurrency := strings.ToLower(strings.TrimSpace(variant.Price.CurrencyCode))
		if lineCurrency == "" {
			return 0, "", "", nil, fmt.Errorf("product '%s' is missing currency code", product.Title)
		}
		if currency == "" {
			currency = lineCurrency
		} else if currency != lineCurrency {
			return 0, "", "", nil, fmt.Errorf("all items in a checkout must use the same currency")
		}

		unitAmount, err := parseAmountToMinorUnits(variant.Price.Amount)
		if err != nil {
			return 0, "", "", nil, fmt.Errorf("invalid price for product '%s': %w", product.Title, err)
		}

		totalAmount += unitAmount * int64(item.Quantity)
		totalQuantity += item.Quantity
		itemDescriptions = append(itemDescriptions, fmt.Sprintf("%s x%d", product.Title, item.Quantity))
		metadata[fmt.Sprintf("item_%d", index+1)] = fmt.Sprintf("%s | %s | qty:%d", product.Handle, variant.ID, item.Quantity)
	}

	metadata["customer_name"] = strings.TrimSpace(req.Customer.Name)
	metadata["customer_phone"] = strings.TrimSpace(req.Customer.Phone)
	metadata["total_items"] = strconv.Itoa(totalQuantity)

	description := fmt.Sprintf("PetWell order (%d item(s))", totalQuantity)
	if len(itemDescriptions) > 0 {
		description = "PetWell: " + strings.Join(itemDescriptions, ", ")
	}

	return totalAmount, currency, description, metadata, nil
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
