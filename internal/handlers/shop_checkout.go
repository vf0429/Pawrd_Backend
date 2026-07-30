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
	}, time.Now, currentShopAccountEmail)
}

func newShopPaymentSheetHandler(
	cfg *config.Config,
	db *gorm.DB,
	paymentFactory checkoutPaymentServiceFactory,
	now func() time.Time,
	accountEmailResolver shopAccountEmailResolver,
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
		accountEmail, ok := resolveShopAccountEmail(
			w,
			r,
			claims.UserID,
			accountEmailResolver,
		)
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
		if !strings.EqualFold(
			strings.TrimSpace(snapshot.Customer.Email),
			strings.TrimSpace(accountEmail),
		) {
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

		// Stripe service init BEFORE any durable mutation: a config failure means
		// no payment attempt was ever possible, so neither the quote nor an order
		// row is touched (Phase 4 review, option a).
		stripeService, err := paymentFactory(cfg)
		if err != nil {
			http.Error(w, "Stripe configuration error", http.StatusInternalServerError)
			return
		}

		if quoteRecord.ConsumedAt != nil || quoteRecord.Status == models.ShopQuoteStatusConsumed {
			resumeConsumedQuotePayment(
				w, r, db, stripeService, snapshot, &quoteRecord,
				quoteID, quoteVersion, orderID, claims.UserID, currentTime,
			)
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

		// Step 1 — atomically consume the quote AND persist the durable order
		// (full immutable shipping snapshot, NULL payment_intent_id) in ONE
		// transaction, before Stripe is ever called.
		order := shopOrderFromQuote(snapshot, orderID, claims.UserID)
		if err := persistCheckoutOrderWithQuote(
			db,
			&order,
			quoteID,
			quoteVersion,
			claims.UserID,
			currentTime,
		); err != nil {
			http.Error(w, "Failed to persist checkout order", http.StatusInternalServerError)
			return
		}

		// Step 2 — create the Stripe PaymentIntent. The stable quote-derived
		// idempotency key means a retry after a lost response returns the SAME
		// intent instead of double-charging.
		intent, err := stripeService.CreatePaymentIntent(
			shopPaymentIntentRequest(snapshot, quoteID, quoteVersion, orderID),
		)
		if err != nil {
			// The order stays as the durable record of the attempt.
			markCheckoutPaymentFailed(db, orderID, "stripe payment intent creation failed: "+err.Error())
			http.Error(w, "Failed to create payment intent: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Step 3 — fail closed if the quote changed while Stripe was running.
		if err := ensureQuoteVersionUnchanged(db, quoteID, quoteVersion); err != nil {
			if errors.Is(err, errQuoteVersionChanged) {
				markCheckoutPaymentFailed(db, orderID, "quote changed during payment setup")
				log.Printf("[shop-checkout] CRITICAL: quote %s changed during payment setup for order %s", quoteID, orderID)
				http.Error(w, "Shop quote changed during payment setup; request a new quote", http.StatusConflict)
				return
			}
			http.Error(w, "Failed to verify shop quote", http.StatusInternalServerError)
			return
		}

		// Step 4 — back-fill the intent id on order + quote. Failure leaves a
		// reconcilable order (intent carries pawrd_order_id); log loudly and let
		// the checkout proceed — the webhook closes the gap.
		if err := backfillCheckoutPaymentIntent(db, orderID, quoteID, quoteVersion, intent.PaymentIntentID); err != nil {
			log.Printf("[shop-checkout] CRITICAL: payment intent %s created for order %s but back-fill failed: %v — reconcile via pawrd_order_id metadata",
				intent.PaymentIntentID, orderID, err)
		}

		writeShopPaymentSheetResponse(w, snapshot, quoteID, orderID, intent)
	}
}

// resumeConsumedQuotePayment handles replays and recovery for an already
// consumed quote within the replay window:
//   - order pending_payment WITH intent id  → idempotent replay (recreate the
//     same Stripe params; the idempotency key returns the same intent).
//   - order pending_payment WITHOUT intent id, or payment_failed → resume:
//     (re)create the intent and back-fill. This covers Stripe creation
//     failures and back-fill failures — no stuck consumed quotes.
//   - anything else (paid/canceled/...) → 409.
func resumeConsumedQuotePayment(
	w http.ResponseWriter,
	r *http.Request,
	db *gorm.DB,
	stripeService checkoutPaymentService,
	snapshot models.ShopQuoteSnapshot,
	quoteRecord *models.ShopCheckoutQuote,
	quoteID string,
	quoteVersion string,
	orderID string,
	userID string,
	currentTime time.Time,
) {
	if quoteRecord.ConsumedAt == nil ||
		quoteRecord.Status != models.ShopQuoteStatusConsumed ||
		strings.TrimSpace(quoteRecord.OrderID) != orderID ||
		!currentTime.Before(quoteRecord.ConsumedAt.Add(shopPaymentReplayWindow)) {
		http.Error(w, "Shop quote has already been used", http.StatusConflict)
		return
	}

	var order models.ShopOrder
	if err := db.Where("id = ? AND user_id = ?", orderID, strings.TrimSpace(userID)).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "Shop quote has already been used", http.StatusConflict)
			return
		}
		http.Error(w, "Failed to load checkout order", http.StatusInternalServerError)
		return
	}

	resumable := (order.Status == "pending_payment" && order.PaymentIntentID == nil) ||
		order.Status == "payment_failed"
	replayable := order.Status == "pending_payment" && order.PaymentIntentID != nil
	if !resumable && !replayable {
		http.Error(w, "Shop quote has already been used", http.StatusConflict)
		return
	}

	intent, err := stripeService.CreatePaymentIntent(
		shopPaymentIntentRequest(snapshot, quoteID, quoteVersion, orderID),
	)
	if err != nil {
		markCheckoutPaymentFailed(db, orderID, "stripe payment intent creation failed: "+err.Error())
		http.Error(w, "Failed to recover payment intent: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := ensureQuoteVersionUnchanged(db, quoteID, quoteVersion); err != nil {
		if errors.Is(err, errQuoteVersionChanged) {
			markCheckoutPaymentFailed(db, orderID, "quote changed during payment setup")
			log.Printf("[shop-checkout] CRITICAL: quote %s changed during payment recovery for order %s", quoteID, orderID)
			http.Error(w, "Shop quote changed during payment setup; request a new quote", http.StatusConflict)
			return
		}
		http.Error(w, "Failed to verify shop quote", http.StatusInternalServerError)
		return
	}

	if replayable {
		if strings.TrimSpace(intent.PaymentIntentID) != order.PaymentIntentIDValue() {
			log.Printf(
				"[shop-checkout] idempotent replay mismatch quote=%s expected_pi=%s actual_pi=%s",
				quoteID,
				order.PaymentIntentIDValue(),
				intent.PaymentIntentID,
			)
			http.Error(w, "Shop quote payment could not be recovered", http.StatusConflict)
			return
		}
		writeShopPaymentSheetResponse(w, snapshot, quoteID, orderID, intent)
		return
	}

	// Resume: attach the intent and reopen the order for payment.
	if err := db.Model(&models.ShopOrder{}).Where("id = ?", orderID).Updates(map[string]any{
		"status": "pending_payment", "financial_status": "pending", "failure_reason": "",
	}).Error; err != nil {
		log.Printf("[shop-checkout] CRITICAL: resume update failed for order %s: %v", orderID, err)
	}
	if err := backfillCheckoutPaymentIntent(db, orderID, quoteID, quoteVersion, intent.PaymentIntentID); err != nil {
		log.Printf("[shop-checkout] CRITICAL: payment intent %s created for order %s but back-fill failed: %v — reconcile via pawrd_order_id metadata",
			intent.PaymentIntentID, orderID, err)
	}
	writeShopPaymentSheetResponse(w, snapshot, quoteID, orderID, intent)
}

var errQuoteVersionChanged = errors.New("quote version changed during payment setup")

// ensureQuoteVersionUnchanged fails closed when the quote was mutated while
// Stripe was creating the intent: that intent is bound to the old quote
// version and could never pass the webhook's quote-integrity validation.
func ensureQuoteVersionUnchanged(db *gorm.DB, quoteID, quoteVersion string) error {
	var current models.ShopCheckoutQuote
	if err := db.Select("snapshot_sha256").Where("id = ?", quoteID).First(&current).Error; err != nil {
		return err
	}
	if !strings.EqualFold(
		strings.TrimSpace(current.SnapshotSHA256),
		strings.ToLower(strings.TrimSpace(quoteVersion)),
	) {
		return errQuoteVersionChanged
	}
	return nil
}

// markCheckoutPaymentFailed records a Stripe-attempt failure on the durable
// order (payment_failed + financial_status=failed, one statement).
func markCheckoutPaymentFailed(db *gorm.DB, orderID, reason string) {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if err := db.Model(&models.ShopOrder{}).Where("id = ?", orderID).
		Updates(map[string]any{"status": "payment_failed", "financial_status": "failed", "failure_reason": reason}).Error; err != nil {
		log.Printf("[shop-checkout] CRITICAL: order %s could not be marked payment_failed: %v", orderID, err)
	}
}

// backfillCheckoutPaymentIntent attaches the created intent id to the order
// and the consumed quote in one transaction. The quote update is CAS-guarded
// on the version the intent was created for: if the quote changed mid-flight
// the back-fill fails and reconciliation falls back to pawrd_order_id.
func backfillCheckoutPaymentIntent(db *gorm.DB, orderID, quoteID, quoteVersion, paymentIntentID string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ShopOrder{}).Where("id = ?", orderID).
			Update("payment_intent_id", paymentIntentID).Error; err != nil {
			return err
		}
		result := tx.Model(&models.ShopCheckoutQuote{}).
			Where(
				"id = ? AND order_id = ? AND snapshot_sha256 = ?",
				quoteID,
				orderID,
				strings.ToLower(strings.TrimSpace(quoteVersion)),
			).
			Update("payment_intent_id", paymentIntentID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("quote %s changed while the payment intent was being created", quoteID)
		}
		return nil
	})
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
) payments.CreatePaymentIntentRequest {
	metadata, description := checkoutMetadata(snapshot, quoteID, quoteVersion, orderID)
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

func persistCheckoutOrderWithQuote(
	db *gorm.DB,
	order *models.ShopOrder,
	quoteID string,
	expectedQuoteVersion string,
	userID string,
	now time.Time,
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
				"status":      models.ShopQuoteStatusConsumed,
				"consumed_at": claimedAt,
				"order_id":    order.ID,
				"updated_at":  claimedAt,
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
		log.Printf(
			"[shop-checkout] persist order failed order=%s payment_intent=%s: %v",
			order.ID,
			order.PaymentIntentIDValue(),
			err,
		)
		return err
	}
	return nil
}

func shopOrderFromQuote(
	snapshot models.ShopQuoteSnapshot,
	orderID string,
	userID string,
) models.ShopOrder {
	order := models.ShopOrder{
		ID:               orderID,
		UserID:           strings.TrimSpace(userID),
		PaymentIntentID:  nil,
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
) (map[string]string, string) {
	// Metadata is limited to no-PII reconciliation fields: order id, quote
	// id/version/expiry and item lines. No customer_* and no user_id — the
	// webhook resolves identity through the order row.
	metadata := map[string]string{
		"pawrd_order_id":         orderID,
		"pawrd_quote_id":         strings.TrimSpace(quoteID),
		"pawrd_quote_version":    strings.ToLower(strings.TrimSpace(quoteVersion)),
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

	if _, err := normalizeHongKongPhone(shipping.Phone); err != nil {
		return err
	}
	return nil
}

func validateHongKongPhone(rawPhone string) error {
	_, err := normalizeHongKongPhone(rawPhone)
	return err
}

// normalizeHongKongPhone is the ONE place a Hong Kong delivery phone is
// canonicalized: spaces/dashes/parens stripped, optional +852/852 prefix
// tolerated, exactly 8 digits leading 2-9 required, canonical +852XXXXXXXX
// produced. Callers use the returned value for every downstream write
// (Shopify request, sealed quote snapshot, persisted order).
func normalizeHongKongPhone(raw string) (string, error) {
	phone := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	phone = strings.TrimPrefix(phone, "+852")
	if strings.HasPrefix(phone, "852") && len(phone) == 11 {
		phone = phone[3:]
	}
	if len(phone) != 8 {
		return "", fmt.Errorf("Hong Kong phone number must contain 8 digits")
	}
	for _, char := range phone {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("Hong Kong phone number must contain 8 digits")
		}
	}
	if phone[0] < '2' || phone[0] > '9' {
		return "", fmt.Errorf("Hong Kong phone number must start with 2-9")
	}
	return "+852" + phone, nil
}
