package shopify

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// StorefrontQuoteLineInput is the minimal authoritative merchandise input used
// by Shopify Cart. Client-provided titles and prices are intentionally absent.
type StorefrontQuoteLineInput struct {
	VariantID string
	Quantity  int
}

type StorefrontQuoteAddress struct {
	RecipientName string
	Phone         string
	Address1      string
	District      string
	Region        string
}

type StorefrontQuoteRequest struct {
	Lines        []StorefrontQuoteLineInput
	Email        string
	Phone        string
	Shipping     StorefrontQuoteAddress
	DiscountCode string
	BuyerIP      string
}

type StorefrontDeliverySelection struct {
	DeliveryGroupID      string
	DeliveryOptionHandle string
}

type StorefrontQuoteLine struct {
	VariantID        string
	Handle           string
	Title            string
	VariantTitle     string
	ImageURL         string
	Quantity         int
	UnitAmountMinor  int64
	RequiresShipping bool
}

type StorefrontDeliveryOption struct {
	DeliveryGroupID string
	Handle          string
	Code            string
	Title           string
	Description     string
	DeliveryMethod  string
	AmountMinor     int64
	Currency        string
}

// StorefrontQuote is a normalized Shopify Cart snapshot. Amounts are expressed
// in currency minor units and are expected to reconcile exactly:
//
//	subtotal - discount + shipping + tax = total
type StorefrontQuote struct {
	CartID                 string
	CartUpdatedAt          time.Time
	Currency               string
	Lines                  []StorefrontQuoteLine
	DeliveryOptions        []StorefrontDeliveryOption
	SelectedDeliveryOption *StorefrontDeliveryOption
	DiscountCode           string
	DiscountCodeApplicable bool
	DiscountTargetType     string
	SubtotalAmountMinor    int64
	DiscountAmountMinor    int64
	ShippingAmountMinor    int64
	TaxAmountMinor         int64
	TotalAmountMinor       int64
	Warnings               []string
}

// StorefrontQuoteClient is intentionally separate from catalog reads so
// checkout cannot silently fall back to mock catalog pricing.
type StorefrontQuoteClient interface {
	CreateCartQuote(context.Context, StorefrontQuoteRequest) (*StorefrontQuote, error)
	SelectCartDelivery(context.Context, string, StorefrontDeliverySelection, string) (*StorefrontQuote, error)
}

func (c *Client) CreateCartQuote(ctx context.Context, req StorefrontQuoteRequest) (*StorefrontQuote, error) {
	lines := make([]map[string]any, 0, len(req.Lines))
	for _, line := range req.Lines {
		lines = append(lines, map[string]any{
			"merchandiseId": strings.TrimSpace(line.VariantID),
			"quantity":      line.Quantity,
		})
	}
	zoneCode, err := hongKongZoneCode(req.Shipping.Region)
	if err != nil {
		return nil, err
	}
	firstName, lastName := splitShippingName(req.Shipping.RecipientName)
	address := map[string]any{
		"address": map[string]any{
			"deliveryAddress": map[string]any{
				"firstName":   firstName,
				"lastName":    lastName,
				"phone":       normalizeHKPhone(req.Shipping.Phone),
				"address1":    strings.TrimSpace(req.Shipping.Address1),
				"address2":    strings.TrimSpace(req.Shipping.Region),
				"city":        strings.TrimSpace(req.Shipping.District),
				"countryCode": "HK",
				"zoneCode":    zoneCode,
			},
		},
		"oneTimeUse":         true,
		"selected":           true,
		"validationStrategy": "STRICT",
	}
	input := map[string]any{
		"lines": lines,
		"buyerIdentity": map[string]any{
			"countryCode": "HK",
			"email":       strings.TrimSpace(req.Email),
			"phone":       normalizeHKPhone(req.Phone),
		},
		"delivery": map[string]any{"addresses": []any{address}},
	}
	if discountCode := strings.TrimSpace(req.DiscountCode); discountCode != "" {
		input["discountCodes"] = []string{discountCode}
	}

	const mutation = `mutation PawrdCartQuote($input: CartInput!) {
	  cartCreate(input: $input) {
	    cart { ...PawrdQuoteCart }
	    userErrors { field message code }
	    warnings { code message }
	  }
	}
	` + storefrontQuoteCartFragment

	data, err := c.executeGraphQLContext(ctx, map[string]any{
		"query": mutation,
		"variables": map[string]any{
			"input": input,
		},
	}, req.BuyerIP)
	if err != nil {
		return nil, err
	}
	var payload struct {
		CartCreate quoteMutationPayload `json:"cartCreate"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Shopify cart quote: %w", err)
	}
	quote, err := normalizeQuoteMutation(payload.CartCreate, strings.TrimSpace(req.DiscountCode))
	if err != nil {
		return nil, err
	}
	if !sameStorefrontQuoteLines(req.Lines, quote.Lines) {
		return nil, fmt.Errorf(
			"Shopify adjusted the requested merchandise or quantity; review the cart and request a new quote",
		)
	}
	return quote, nil
}

func sameStorefrontQuoteLines(
	requested []StorefrontQuoteLineInput,
	quoted []StorefrontQuoteLine,
) bool {
	requestedQuantities := make(map[string]int, len(requested))
	for _, line := range requested {
		variantID := strings.TrimSpace(line.VariantID)
		if variantID == "" || line.Quantity <= 0 {
			return false
		}
		requestedQuantities[variantID] += line.Quantity
	}
	if len(requestedQuantities) == 0 {
		return false
	}

	quotedQuantities := make(map[string]int, len(quoted))
	for _, line := range quoted {
		variantID := strings.TrimSpace(line.VariantID)
		if variantID == "" || line.Quantity <= 0 {
			return false
		}
		quotedQuantities[variantID] += line.Quantity
	}
	if len(requestedQuantities) != len(quotedQuantities) {
		return false
	}
	for variantID, quantity := range requestedQuantities {
		if quotedQuantities[variantID] != quantity {
			return false
		}
	}
	return true
}

func (c *Client) SelectCartDelivery(
	ctx context.Context,
	cartID string,
	selection StorefrontDeliverySelection,
	buyerIP string,
) (*StorefrontQuote, error) {
	const mutation = `mutation PawrdSelectCartDelivery(
	  $cartId: ID!,
	  $selectedDeliveryOptions: [CartSelectedDeliveryOptionInput!]!
	) {
	  cartSelectedDeliveryOptionsUpdate(
	    cartId: $cartId,
	    selectedDeliveryOptions: $selectedDeliveryOptions
	  ) {
	    cart { ...PawrdQuoteCart }
	    userErrors { field message code }
	    warnings { code message }
	  }
	}
	` + storefrontQuoteCartFragment

	data, err := c.executeGraphQLContext(ctx, map[string]any{
		"query": mutation,
		"variables": map[string]any{
			"cartId": strings.TrimSpace(cartID),
			"selectedDeliveryOptions": []any{map[string]any{
				"deliveryGroupId":      strings.TrimSpace(selection.DeliveryGroupID),
				"deliveryOptionHandle": strings.TrimSpace(selection.DeliveryOptionHandle),
			}},
		},
	}, buyerIP)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Update quoteMutationPayload `json:"cartSelectedDeliveryOptionsUpdate"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Shopify selected delivery quote: %w", err)
	}
	return normalizeQuoteMutation(payload.Update, "")
}

func hongKongZoneCode(region string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "hong kong island", "hk", "港島", "香港島":
		return "HK", nil
	case "kowloon", "kln", "九龍":
		return "KLN", nil
	case "new territories", "nt", "新界":
		return "NT", nil
	default:
		return "", fmt.Errorf("unsupported Hong Kong shipping region %q", strings.TrimSpace(region))
	}
}

const storefrontQuoteCartFragment = `
fragment PawrdQuoteCart on Cart {
  id
  updatedAt
  discountCodes { code applicable }
  discountApplications {
    __typename
    targetType
    ... on CartCodeDiscountApplication { code }
  }
  cost {
    subtotalAmount { amount currencyCode }
    totalTaxAmount { amount currencyCode }
    totalAmount { amount currencyCode }
  }
  lines(first: 100) {
    nodes {
      id
      quantity
      cost {
        amountPerQuantity { amount currencyCode }
        subtotalAmount { amount currencyCode }
        totalAmount { amount currencyCode }
      }
      merchandise {
        ... on ProductVariant {
          id
          title
          availableForSale
          requiresShipping
          image { url }
          product { title handle }
        }
      }
    }
  }
  deliveryGroups(first: 10) {
    nodes {
      id
      deliveryOptions {
        handle
        code
        title
        description
        deliveryMethodType
        estimatedCost { amount currencyCode }
      }
      selectedDeliveryOption {
        handle
        code
        title
        description
        deliveryMethodType
        estimatedCost { amount currencyCode }
      }
    }
  }
}
`

type quoteMutationPayload struct {
	Cart       *rawQuoteCart `json:"cart"`
	UserErrors []struct {
		Field   []string `json:"field"`
		Message string   `json:"message"`
		Code    string   `json:"code"`
	} `json:"userErrors"`
	Warnings []rawQuoteWarning `json:"warnings"`
}

type rawQuoteWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type rawQuoteMoney struct {
	Amount       string `json:"amount"`
	CurrencyCode string `json:"currencyCode"`
}

type rawQuoteDeliveryOption struct {
	Handle             string        `json:"handle"`
	Code               string        `json:"code"`
	Title              string        `json:"title"`
	Description        string        `json:"description"`
	DeliveryMethodType string        `json:"deliveryMethodType"`
	EstimatedCost      rawQuoteMoney `json:"estimatedCost"`
}

type rawQuoteCart struct {
	ID            string    `json:"id"`
	UpdatedAt     time.Time `json:"updatedAt"`
	DiscountCodes []struct {
		Code       string `json:"code"`
		Applicable bool   `json:"applicable"`
	} `json:"discountCodes"`
	DiscountApplications []struct {
		TypeName   string `json:"__typename"`
		Code       string `json:"code"`
		TargetType string `json:"targetType"`
	} `json:"discountApplications"`
	Cost struct {
		SubtotalAmount rawQuoteMoney  `json:"subtotalAmount"`
		TotalTaxAmount *rawQuoteMoney `json:"totalTaxAmount"`
		TotalAmount    rawQuoteMoney  `json:"totalAmount"`
	} `json:"cost"`
	Lines struct {
		Nodes []struct {
			ID       string `json:"id"`
			Quantity int    `json:"quantity"`
			Cost     struct {
				AmountPerQuantity rawQuoteMoney `json:"amountPerQuantity"`
				SubtotalAmount    rawQuoteMoney `json:"subtotalAmount"`
				TotalAmount       rawQuoteMoney `json:"totalAmount"`
			} `json:"cost"`
			Merchandise struct {
				ID               string `json:"id"`
				Title            string `json:"title"`
				AvailableForSale bool   `json:"availableForSale"`
				RequiresShipping bool   `json:"requiresShipping"`
				Image            *struct {
					URL string `json:"url"`
				} `json:"image"`
				Product struct {
					Title  string `json:"title"`
					Handle string `json:"handle"`
				} `json:"product"`
			} `json:"merchandise"`
		} `json:"nodes"`
	} `json:"lines"`
	DeliveryGroups struct {
		Nodes []struct {
			ID                     string                   `json:"id"`
			DeliveryOptions        []rawQuoteDeliveryOption `json:"deliveryOptions"`
			SelectedDeliveryOption *rawQuoteDeliveryOption  `json:"selectedDeliveryOption"`
		} `json:"nodes"`
	} `json:"deliveryGroups"`
}

func normalizeQuoteMutation(payload quoteMutationPayload, requestedDiscountCode string) (*StorefrontQuote, error) {
	if len(payload.UserErrors) > 0 {
		return nil, fmt.Errorf("Shopify cart: %s", strings.TrimSpace(payload.UserErrors[0].Message))
	}
	if err := rejectUnsafeQuoteWarnings(payload.Warnings); err != nil {
		return nil, err
	}
	if payload.Cart == nil {
		return nil, fmt.Errorf("Shopify cart returned no cart")
	}
	return normalizeStorefrontQuote(payload.Cart, requestedDiscountCode, payload.Warnings)
}

func rejectUnsafeQuoteWarnings(warnings []rawQuoteWarning) error {
	for _, warning := range warnings {
		code := strings.ToUpper(strings.TrimSpace(warning.Code))
		// Discount warnings are represented again by discountCodes.applicable and
		// become a non-chargeable discount_invalid quote. Every other warning is
		// treated as an unsafe automatic cart adjustment. In particular, Shopify
		// reports stock caps/removals and buyer-location unavailability as
		// successful mutations with warnings rather than userErrors.
		if strings.HasPrefix(code, "DISCOUNT_") {
			continue
		}
		message := strings.TrimSpace(warning.Message)
		if message == "" {
			message = code
		}
		if message == "" {
			message = "an unknown automatic cart adjustment"
		}
		return fmt.Errorf("Shopify adjusted the cart and it must be quoted again: %s", message)
	}
	return nil
}

func normalizeStorefrontQuote(
	cart *rawQuoteCart,
	requestedDiscountCode string,
	rawWarnings []rawQuoteWarning,
) (*StorefrontQuote, error) {
	if strings.TrimSpace(cart.ID) == "" {
		return nil, fmt.Errorf("Shopify cart is missing an ID")
	}
	subtotal, currency, err := quoteMoneyMinor(cart.Cost.SubtotalAmount)
	if err != nil {
		return nil, fmt.Errorf("invalid Shopify cart subtotal: %w", err)
	}
	total, totalCurrency, err := quoteMoneyMinor(cart.Cost.TotalAmount)
	if err != nil {
		return nil, fmt.Errorf("invalid Shopify cart total: %w", err)
	}
	if err := requireQuoteCurrency(currency, totalCurrency); err != nil {
		return nil, err
	}
	var tax int64
	if cart.Cost.TotalTaxAmount != nil {
		tax, totalCurrency, err = quoteMoneyMinor(*cart.Cost.TotalTaxAmount)
		if err != nil {
			return nil, fmt.Errorf("invalid Shopify cart tax: %w", err)
		}
		if err := requireQuoteCurrency(currency, totalCurrency); err != nil {
			return nil, err
		}
	}
	if tax != 0 {
		return nil, fmt.Errorf("tax-bearing Shopify carts are not supported by Pawrd Hong Kong checkout")
	}

	result := &StorefrontQuote{
		CartID:              strings.TrimSpace(cart.ID),
		CartUpdatedAt:       cart.UpdatedAt,
		Currency:            currency,
		SubtotalAmountMinor: subtotal,
		TaxAmountMinor:      tax,
		TotalAmountMinor:    total,
	}
	for _, warning := range rawWarnings {
		message := strings.TrimSpace(warning.Message)
		if message != "" {
			result.Warnings = append(result.Warnings, message)
		}
	}

	for _, rawLine := range cart.Lines.Nodes {
		variant := rawLine.Merchandise
		if strings.TrimSpace(variant.ID) == "" {
			return nil, fmt.Errorf("Shopify cart contains unsupported non-variant merchandise")
		}
		if rawLine.Quantity <= 0 {
			return nil, fmt.Errorf("Shopify cart contains an invalid quantity")
		}
		if !variant.AvailableForSale {
			return nil, fmt.Errorf("variant %q is currently unavailable", variant.Title)
		}
		if !variant.RequiresShipping {
			return nil, fmt.Errorf("variant %q is not configured as a shippable physical product", variant.Title)
		}
		unitAmount, lineCurrency, err := quoteMoneyMinor(rawLine.Cost.AmountPerQuantity)
		if err != nil {
			return nil, fmt.Errorf("invalid Shopify cart line price: %w", err)
		}
		if err := requireQuoteCurrency(currency, lineCurrency); err != nil {
			return nil, err
		}
		imageURL := ""
		if variant.Image != nil {
			imageURL = variant.Image.URL
		}
		result.Lines = append(result.Lines, StorefrontQuoteLine{
			VariantID:        variant.ID,
			Handle:           variant.Product.Handle,
			Title:            variant.Product.Title,
			VariantTitle:     variant.Title,
			ImageURL:         imageURL,
			Quantity:         rawLine.Quantity,
			UnitAmountMinor:  unitAmount,
			RequiresShipping: variant.RequiresShipping,
		})
	}
	if len(result.Lines) == 0 {
		return nil, fmt.Errorf("Shopify cart contains no purchasable lines")
	}

	if len(cart.DeliveryGroups.Nodes) > 1 {
		return nil, fmt.Errorf("multiple Shopify delivery groups are not supported by Pawrd checkout")
	}
	for _, group := range cart.DeliveryGroups.Nodes {
		for _, rawOption := range group.DeliveryOptions {
			option, err := normalizeDeliveryOption(group.ID, rawOption, currency)
			if err != nil {
				return nil, err
			}
			result.DeliveryOptions = append(result.DeliveryOptions, option)
		}
		if group.SelectedDeliveryOption != nil {
			option, err := normalizeDeliveryOption(group.ID, *group.SelectedDeliveryOption, currency)
			if err != nil {
				return nil, err
			}
			result.SelectedDeliveryOption = &option
			result.ShippingAmountMinor = option.AmountMinor
		}
	}

	requestedDiscountCode = strings.TrimSpace(requestedDiscountCode)
	if requestedDiscountCode == "" && len(cart.DiscountCodes) == 1 {
		requestedDiscountCode = strings.TrimSpace(cart.DiscountCodes[0].Code)
	}
	result.DiscountCode = requestedDiscountCode
	for _, code := range cart.DiscountCodes {
		if strings.EqualFold(strings.TrimSpace(code.Code), requestedDiscountCode) {
			result.DiscountCode = strings.TrimSpace(code.Code)
			result.DiscountCodeApplicable = code.Applicable
		}
	}
	for _, application := range cart.DiscountApplications {
		if application.TypeName != "CartCodeDiscountApplication" {
			return nil, fmt.Errorf("automatic or combined Shopify discounts are not supported; use a single discount code")
		}
		if result.DiscountCode == "" || !strings.EqualFold(application.Code, result.DiscountCode) {
			return nil, fmt.Errorf("combined Shopify discount codes are not supported")
		}
		result.DiscountTargetType = strings.ToUpper(strings.TrimSpace(application.TargetType))
		switch result.DiscountTargetType {
		case "LINE_ITEM", "SHIPPING_LINE":
		default:
			return nil, fmt.Errorf("unsupported Shopify discount target %q", application.TargetType)
		}
	}

	discount := result.SubtotalAmountMinor + result.ShippingAmountMinor + result.TaxAmountMinor - result.TotalAmountMinor
	if discount < 0 {
		return nil, fmt.Errorf("Shopify quote amounts do not reconcile")
	}
	result.DiscountAmountMinor = discount
	if result.DiscountCode == "" && result.DiscountAmountMinor > 0 {
		return nil, fmt.Errorf("automatic Shopify discounts are not supported; use a single discount code")
	}
	if result.DiscountCode != "" && !result.DiscountCodeApplicable && result.DiscountAmountMinor > 0 {
		return nil, fmt.Errorf("Shopify returned a discount amount for an inapplicable code")
	}
	if got := result.SubtotalAmountMinor - result.DiscountAmountMinor + result.ShippingAmountMinor + result.TaxAmountMinor; got != result.TotalAmountMinor {
		return nil, fmt.Errorf("Shopify quote amounts do not reconcile")
	}
	return result, nil
}

func normalizeDeliveryOption(groupID string, raw rawQuoteDeliveryOption, currency string) (StorefrontDeliveryOption, error) {
	amount, optionCurrency, err := quoteMoneyMinor(raw.EstimatedCost)
	if err != nil {
		return StorefrontDeliveryOption{}, fmt.Errorf("invalid Shopify delivery price: %w", err)
	}
	if err := requireQuoteCurrency(currency, optionCurrency); err != nil {
		return StorefrontDeliveryOption{}, err
	}
	if strings.TrimSpace(raw.Handle) == "" {
		return StorefrontDeliveryOption{}, fmt.Errorf("Shopify delivery option is missing a handle")
	}
	return StorefrontDeliveryOption{
		DeliveryGroupID: strings.TrimSpace(groupID),
		Handle:          strings.TrimSpace(raw.Handle),
		Code:            strings.TrimSpace(raw.Code),
		Title:           strings.TrimSpace(raw.Title),
		Description:     strings.TrimSpace(raw.Description),
		DeliveryMethod:  strings.TrimSpace(raw.DeliveryMethodType),
		AmountMinor:     amount,
		Currency:        currency,
	}, nil
}

func quoteMoneyMinor(money rawQuoteMoney) (int64, string, error) {
	currency := strings.ToUpper(strings.TrimSpace(money.CurrencyCode))
	if currency != "HKD" {
		return 0, "", fmt.Errorf("Pawrd Hong Kong checkout requires HKD, got %q", currency)
	}
	rat, ok := new(big.Rat).SetString(strings.TrimSpace(money.Amount))
	if !ok || rat.Sign() < 0 {
		return 0, "", fmt.Errorf("invalid money amount %q", money.Amount)
	}
	rat.Mul(rat, big.NewRat(100, 1))
	if !rat.IsInt() || !rat.Num().IsInt64() {
		return 0, "", fmt.Errorf("money amount %q cannot be represented in HKD cents", money.Amount)
	}
	return rat.Num().Int64(), currency, nil
}

func requireQuoteCurrency(expected, actual string) error {
	if strings.ToUpper(strings.TrimSpace(expected)) != strings.ToUpper(strings.TrimSpace(actual)) {
		return fmt.Errorf("Shopify quote returned mixed currencies")
	}
	return nil
}

func splitShippingName(name string) (string, string) {
	parts := strings.Fields(strings.TrimSpace(name))
	if len(parts) <= 1 {
		return strings.TrimSpace(name), ""
	}
	return strings.Join(parts[:len(parts)-1], " "), parts[len(parts)-1]
}

func normalizeHKPhone(phone string) string {
	phone = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(phone))
	if strings.HasPrefix(phone, "+852") {
		return phone
	}
	return "+852" + phone
}
