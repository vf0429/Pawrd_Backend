package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/gorm"
)

type ShopCheckoutLineItemRequest struct {
	Handle    string `json:"handle"`
	VariantID string `json:"variantId"`
	Quantity  int    `json:"quantity"`
	Source    string `json:"source,omitempty"`
}

type ShopCheckoutCustomerRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Phone string `json:"phone"`
}

type ShopCheckoutShippingRequest struct {
	RecipientName string `json:"recipientName"`
	Phone         string `json:"phone"`
	Address1      string `json:"address1"`
	District      string `json:"district"`
	Region        string `json:"region"`
}

// ShopPaymentSheetRequest intentionally contains only the server-issued quote
// ID. Line items, customer data, shipping, discounts and totals are loaded from
// the sealed, user-bound quote snapshot.
type ShopPaymentSheetRequest struct {
	QuoteID string `json:"quoteId"`
}

const shopPaymentReplayWindow = 23 * time.Hour

type ShopPaymentSheetResponse struct {
	PaymentIntentClientSecret string                          `json:"paymentIntentClientSecret"`
	PublishableKey            string                          `json:"publishableKey"`
	MerchantDisplayName       string                          `json:"merchantDisplayName"`
	Amount                    int64                           `json:"amount"`
	Currency                  string                          `json:"currency"`
	OrderID                   string                          `json:"orderId"`
	PaymentIntentID           string                          `json:"paymentIntentId"`
	QuoteID                   string                          `json:"quoteId"`
	Amounts                   models.ShopQuoteAmounts         `json:"amounts"`
	SelectedDeliveryOption    *models.ShopQuoteDeliveryOption `json:"selectedDeliveryOption,omitempty"`
	Discount                  models.ShopQuoteDiscount        `json:"discount"`
}

type checkoutPaymentService interface {
	CreatePaymentIntent(payments.CreatePaymentIntentRequest) (*payments.CreatePaymentIntentResponse, error)
	CancelPaymentIntent(string) error
}

type checkoutPaymentServiceFactory func(*config.Config) (checkoutPaymentService, error)

func NewShopPaymentSheetHandler(cfg *config.Config, db *gorm.DB) http.HandlerFunc {
	return newShopPaymentSheetHandler(cfg, db, func(cfg *config.Config) (checkoutPaymentService, error) {
		return payments.NewStripeService(cfg)
	}, time.Now)
}

func newShopPaymentSheetHandler(
	cfg *config.Config,
	db *gorm.DB,
	paymentFactory checkoutPaymentServiceFactory,
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
		if db == nil {
			http.Error(w, "Shop checkout storage is unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := cfg.ValidateShopCheckoutConfig(); err != nil {
			http.Error(w, "Shop checkout is not configured", http.StatusServiceUnavailable)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		var req ShopPaymentSheetRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "Invalid checkout payload", http.StatusBadRequest)
			return
		}
		quoteID := strings.TrimSpace(req.QuoteID)
		if quoteID == "" {
			http.Error(w, "A selected Shopify quoteId is required", http.StatusBadRequest)
			return
		}

		var quoteRecord models.ShopCheckoutQuote
		if err := db.Where("id = ? AND user_id = ?", quoteID, strings.TrimSpace(claims.UserID)).First(&quoteRecord).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "Shop quote not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to load shop quote", http.StatusInternalServerError)
			return
		}
		currentTime := now().UTC()
		snapshot, err := quoteRecord.DecodeAndVerifySnapshot()
		if err != nil {
			log.Printf("[shop-checkout] quote integrity failure quote=%s user=%s: %v", quoteID, claims.UserID, err)
			http.Error(w, "Shop quote is invalid; request a new quote", http.StatusConflict)
			return
		}
		quoteVersion := strings.ToLower(strings.TrimSpace(quoteRecord.SnapshotSHA256))
		if !strings.EqualFold(strings.TrimSpace(snapshot.Customer.Email), strings.TrimSpace(claims.Email)) {
			http.Error(w, "Shop quote does not belong to the authenticated account", http.StatusForbidden)
			return
		}
		if snapshot.Amounts.TotalAmountMinor <= 0 || snapshot.Currency != "HKD" {
			http.Error(w, "Shop quote contains an invalid payment total", http.StatusConflict)
			return
		}
		if quoteSnapshotRequiresShipping(snapshot) && snapshot.SelectedDeliveryOption == nil {
			http.Error(w, "Select a Shopify delivery option before payment", http.StatusConflict)
			return
		}

		orderID := shopOrderIDForQuote(quoteID)
		if quoteRecord.ConsumedAt != nil || quoteRecord.Status == models.ShopQuoteStatusConsumed {
			if quoteRecord.ConsumedAt == nil ||
				quoteRecord.Status != models.ShopQuoteStatusConsumed ||
				strings.TrimSpace(quoteRecord.OrderID) != orderID ||
				strings.TrimSpace(quoteRecord.PaymentIntentID) == "" ||
				!currentTime.Before(quoteRecord.ConsumedAt.Add(shopPaymentReplayWindow)) {
				http.Error(w, "Shop quote has already been used", http.StatusConflict)
				return
			}
			var existingOrder models.ShopOrder
			if err := db.Where(
				"id = ? AND user_id = ? AND payment_intent_id = ?",
				orderID,
				strings.TrimSpace(claims.UserID),
				strings.TrimSpace(quoteRecord.PaymentIntentID),
			).First(&existingOrder).Error; err != nil ||
				existingOrder.Status != "pending_payment" {
				http.Error(w, "Shop quote has already been used", http.StatusConflict)
				return
			}
			stripeService, err := paymentFactory(cfg)
			if err != nil {
				http.Error(w, "Stripe configuration error", http.StatusInternalServerError)
				return
			}
			intent, err := stripeService.CreatePaymentIntent(
				shopPaymentIntentRequest(snapshot, quoteID, quoteVersion, orderID, claims.UserID),
			)
			if err != nil {
				http.Error(w, "Failed to recover payment intent: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if strings.TrimSpace(intent.PaymentIntentID) != strings.TrimSpace(quoteRecord.PaymentIntentID) {
				log.Printf(
					"[shop-checkout] idempotent replay mismatch quote=%s expected_pi=%s actual_pi=%s",
					quoteID,
					quoteRecord.PaymentIntentID,
					intent.PaymentIntentID,
				)
				http.Error(w, "Shop quote payment could not be recovered", http.StatusConflict)
				return
			}
			writeShopPaymentSheetResponse(w, snapshot, quoteID, orderID, intent)
			return
		}
		if !currentTime.Before(quoteRecord.ExpiresAt) {
			http.Error(w, "Shop quote has expired", http.StatusGone)
			return
		}
		if quoteRecord.Status != models.ShopQuoteStatusReady {
			if quoteRecord.Status == models.ShopQuoteStatusDiscountInvalid {
				http.Error(w, "Discount code is not applicable; request a new quote without it", http.StatusConflict)
			} else {
				http.Error(w, "Select a Shopify delivery option before payment", http.StatusConflict)
			}
			return
		}

		stripeService, err := paymentFactory(cfg)
		if err != nil {
			http.Error(w, "Stripe configuration error", http.StatusInternalServerError)
			return
		}
		intent, err := stripeService.CreatePaymentIntent(
			shopPaymentIntentRequest(snapshot, quoteID, quoteVersion, orderID, claims.UserID),
		)
		if err != nil {
			http.Error(w, "Failed to create payment intent: "+err.Error(), http.StatusInternalServerError)
			return
		}

		order := shopOrderFromQuote(snapshot, orderID, intent.PaymentIntentID, claims.UserID)
		if err := persistCheckoutOrderWithQuote(
			db,
			&order,
			quoteID,
			quoteVersion,
			claims.UserID,
			currentTime,
			// The stable quote-derived Stripe idempotency key allows a failed
			// local transaction to reuse this unconfirmed intent on retry.
			// Cancelling it here would make the same key return a cancelled PI.
			nil,
		); err != nil {
			http.Error(w, "Failed to persist checkout order", http.StatusInternalServerError)
			return
		}

		writeShopPaymentSheetResponse(w, snapshot, quoteID, orderID, intent)
	}
}

func shopOrderIDForQuote(quoteID string) string {
	return uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("https://pawrd.com/shop/orders/"+strings.TrimSpace(quoteID)),
	).String()
}

func shopPaymentIntentIdempotencyKey(quoteID, quoteVersion string) string {
	return "pawrd-shop-quote:" +
		strings.TrimSpace(quoteID) +
		":" +
		strings.ToLower(strings.TrimSpace(quoteVersion))
}

func shopPaymentIntentRequest(
	snapshot models.ShopQuoteSnapshot,
	quoteID string,
	quoteVersion string,
	orderID string,
	userID string,
) payments.CreatePaymentIntentRequest {
	metadata, description := checkoutMetadata(snapshot, quoteID, quoteVersion, orderID, userID)
	return payments.CreatePaymentIntentRequest{
		Amount:         snapshot.Amounts.TotalAmountMinor,
		Currency:       strings.ToLower(snapshot.Currency),
		Description:    description,
		ReceiptEmail:   snapshot.Customer.Email,
		Metadata:       metadata,
		StatementNote:  "PAWRD",
		IdempotencyKey: shopPaymentIntentIdempotencyKey(quoteID, quoteVersion),
	}
}

func writeShopPaymentSheetResponse(
	w http.ResponseWriter,
	snapshot models.ShopQuoteSnapshot,
	quoteID string,
	orderID string,
	intent *payments.CreatePaymentIntentResponse,
) {
	writeJSON(w, http.StatusOK, ShopPaymentSheetResponse{
		PaymentIntentClientSecret: intent.ClientSecret,
		PublishableKey:            intent.PublishableKey,
		MerchantDisplayName:       "Pawrd",
		Amount:                    snapshot.Amounts.TotalAmountMinor,
		Currency:                  strings.ToLower(snapshot.Currency),
		OrderID:                   orderID,
		PaymentIntentID:           intent.PaymentIntentID,
		QuoteID:                   quoteID,
		Amounts:                   snapshot.Amounts,
		SelectedDeliveryOption:    snapshot.SelectedDeliveryOption,
		Discount:                  snapshot.Discount,
	})
}

type paymentIntentCancelFunc func(string) error

func persistCheckoutOrder(db *gorm.DB, order *models.ShopOrder, cancelPaymentIntent paymentIntentCancelFunc) error {
	if err := db.Create(order).Error; err != nil {
		logCheckoutPersistenceFailure(order, err, cancelPaymentIntent)
		return err
	}
	return nil
}

func persistCheckoutOrderWithQuote(
	db *gorm.DB,
	order *models.ShopOrder,
	quoteID string,
	expectedQuoteVersion string,
	userID string,
	now time.Time,
	cancelPaymentIntent paymentIntentCancelFunc,
) error {
	err := db.Transaction(func(tx *gorm.DB) error {
		claimedAt := now.UTC()
		result := tx.Model(&models.ShopCheckoutQuote{}).
			Where(
				"id = ? AND user_id = ? AND status = ? AND consumed_at IS NULL AND expires_at > ? AND snapshot_sha256 = ?",
				quoteID,
				userID,
				models.ShopQuoteStatusReady,
				claimedAt,
				strings.ToLower(strings.TrimSpace(expectedQuoteVersion)),
			).
			Updates(map[string]any{
				"status":            models.ShopQuoteStatusConsumed,
				"consumed_at":       claimedAt,
				"order_id":          order.ID,
				"payment_intent_id": order.PaymentIntentID,
				"updated_at":        claimedAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("selected quote is no longer available")
		}
		return tx.Create(order).Error
	})
	if err != nil {
		logCheckoutPersistenceFailure(order, err, cancelPaymentIntent)
		return err
	}
	return nil
}

func logCheckoutPersistenceFailure(
	order *models.ShopOrder,
	err error,
	cancelPaymentIntent paymentIntentCancelFunc,
) {
	log.Printf(
		"[shop-checkout] persist order failed order=%s payment_intent=%s: %v",
		order.ID,
		order.PaymentIntentID,
		err,
	)
	if cancelPaymentIntent == nil {
		return
	}
	if cancelErr := cancelPaymentIntent(order.PaymentIntentID); cancelErr != nil {
		log.Printf(
			"[shop-checkout] cancel orphan payment intent failed payment_intent=%s: %v",
			order.PaymentIntentID,
			cancelErr,
		)
	}
}

func shopOrderFromQuote(
	snapshot models.ShopQuoteSnapshot,
	orderID string,
	paymentIntentID string,
	userID string,
) models.ShopOrder {
	order := models.ShopOrder{
		ID:               orderID,
		UserID:           strings.TrimSpace(userID),
		PaymentIntentID:  strings.TrimSpace(paymentIntentID),
		Status:           "pending_payment",
		FinancialStatus:  "pending",
		Currency:         strings.ToUpper(snapshot.Currency),
		TotalAmountMinor: snapshot.Amounts.TotalAmountMinor,
		CustomerName:     strings.TrimSpace(snapshot.Shipping.RecipientName),
		CustomerEmail:    strings.TrimSpace(snapshot.Customer.Email),
		CustomerPhone:    strings.TrimSpace(snapshot.Shipping.Phone),
		ShippingAddress1: strings.TrimSpace(snapshot.Shipping.Address1),
		ShippingDistrict: strings.TrimSpace(snapshot.Shipping.District),
		ShippingRegion:   strings.TrimSpace(snapshot.Shipping.Region),
		ShippingCountry:  "Hong Kong",
		Items:            make([]models.ShopOrderItem, 0, len(snapshot.LineItems)),
	}
	for _, line := range snapshot.LineItems {
		order.Items = append(order.Items, models.ShopOrderItem{
			ID:              uuid.NewString(),
			OrderID:         orderID,
			Source:          "shopify",
			Handle:          line.Handle,
			VariantID:       line.VariantID,
			Title:           line.Title,
			ImageURL:        line.ImageURL,
			Quantity:        line.Quantity,
			UnitAmountMinor: line.UnitAmountMinor,
			Currency:        strings.ToUpper(snapshot.Currency),
		})
	}
	return order
}

func checkoutMetadata(
	snapshot models.ShopQuoteSnapshot,
	quoteID string,
	quoteVersion string,
	orderID string,
	userID string,
) (map[string]string, string) {
	metadata := map[string]string{
		"pawrd_order_id":         orderID,
		"pawrd_quote_id":         strings.TrimSpace(quoteID),
		"pawrd_quote_version":    strings.ToLower(strings.TrimSpace(quoteVersion)),
		"user_id":                strings.TrimSpace(userID),
		"customer_email":         strings.TrimSpace(snapshot.Customer.Email),
		"customer_name":          strings.TrimSpace(snapshot.Customer.Name),
		"pawrd_quote_expires_at": snapshot.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	totalQuantity := 0
	descriptions := make([]string, 0, len(snapshot.LineItems))
	for index, line := range snapshot.LineItems {
		totalQuantity += line.Quantity
		metadata[fmt.Sprintf("item_%d", index+1)] = fmt.Sprintf(
			"source=shopify | handle=%s | variant=%s | qty:%d",
			line.Handle,
			line.VariantID,
			line.Quantity,
		)
		descriptions = append(descriptions, fmt.Sprintf("%s x%d", line.Title, line.Quantity))
	}
	metadata["total_items"] = fmt.Sprintf("%d", totalQuantity)
	description := "Pawrd order"
	if len(descriptions) > 0 {
		description = "Pawrd: " + strings.Join(descriptions, ", ")
	}
	if len(description) > 450 {
		description = description[:450]
	}
	return metadata, description
}

func quoteSnapshotRequiresShipping(snapshot models.ShopQuoteSnapshot) bool {
	for _, line := range snapshot.LineItems {
		if line.RequiresShipping {
			return true
		}
	}
	return false
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
