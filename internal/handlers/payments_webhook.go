package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v83"
	"github.com/stripe/stripe-go/v83/webhook"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NewPaymentsWebhookHandler handles Stripe's authoritative payment and refund
// lifecycle. Fulfillment still runs only after payment_intent.succeeded; delayed
// payment and refund events update Pawrd's durable order mirror.
func NewPaymentsWebhookHandler(
	cfg *config.Config,
	db *gorm.DB,
	fulfiller payments.Fulfiller,
	refundMirrors ...payments.RefundMirrorEnqueuer,
) http.HandlerFunc {
	var refundMirror payments.RefundMirrorEnqueuer
	if len(refundMirrors) > 0 {
		refundMirror = refundMirrors[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		const maxBody = 1 << 20
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[stripe-webhook] read body failed: %v", err)
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if cfg.StripeWebhookSecret == "" {
			log.Printf("[stripe-webhook] STRIPE_WEBHOOK_SECRET not set; rejecting event")
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}
		event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), cfg.StripeWebhookSecret)
		if err != nil {
			log.Printf("[stripe-webhook] signature verification failed: %v", err)
			http.Error(w, "signature verification failed", http.StatusBadRequest)
			return
		}
		if !handledStripeShopEvent(event.Type) {
			log.Printf("[stripe-webhook] ignoring event type %s (%s)", event.Type, event.ID)
			w.WriteHeader(http.StatusOK)
			return
		}

		integrationEvent, alreadyCompleted, err := beginStripeIntegrationEvent(db, event)
		if err != nil {
			http.Error(w, "event store unavailable", http.StatusInternalServerError)
			return
		}
		if alreadyCompleted {
			w.WriteHeader(http.StatusOK)
			return
		}

		err = applyStripeShopEvent(db, fulfiller, refundMirror, event)
		if err != nil {
			_ = db.Model(integrationEvent).Updates(map[string]any{
				"status": "failed", "last_error": err.Error(),
			}).Error
			log.Printf("[stripe-webhook] %s failed for %s: %v", event.Type, event.ID, err)
			http.Error(w, "webhook processing failed", http.StatusInternalServerError)
			return
		}

		now := time.Now().UTC()
		_ = db.Model(integrationEvent).Updates(map[string]any{
			"status": "completed", "processed_at": &now, "last_error": "",
		}).Error
		log.Printf("[stripe-webhook] %s handled: %s", event.Type, event.ID)
		w.WriteHeader(http.StatusOK)
	}
}

func handledStripeShopEvent(eventType stripe.EventType) bool {
	switch eventType {
	case stripe.EventTypePaymentIntentSucceeded,
		stripe.EventTypePaymentIntentProcessing,
		stripe.EventTypePaymentIntentPaymentFailed,
		stripe.EventTypePaymentIntentCanceled,
		stripe.EventTypeRefundCreated,
		stripe.EventTypeRefundUpdated,
		stripe.EventTypeRefundFailed,
		stripe.EventTypeChargeRefunded,
		stripe.EventTypeChargeDisputeCreated,
		stripe.EventTypeChargeDisputeUpdated,
		stripe.EventTypeChargeDisputeClosed,
		stripe.EventTypeChargeDisputeFundsWithdrawn,
		stripe.EventTypeChargeDisputeFundsReinstated:
		return true
	default:
		return false
	}
}

func beginStripeIntegrationEvent(db *gorm.DB, event stripe.Event) (*models.ShopIntegrationEvent, bool, error) {
	var integrationEvent models.ShopIntegrationEvent
	err := db.Where("provider = ? AND external_event_id = ?", "stripe", event.ID).First(&integrationEvent).Error
	if err == nil && integrationEvent.Status == "completed" {
		return &integrationEvent, true, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if integrationEvent.ID == "" {
		integrationEvent = models.ShopIntegrationEvent{
			ID: uuid.NewString(), Provider: "stripe", ExternalEventID: event.ID,
			Topic: string(event.Type), Status: "processing",
		}
		if err := db.Create(&integrationEvent).Error; err != nil {
			return nil, false, err
		}
	} else if err := db.Model(&integrationEvent).Updates(map[string]any{
		"status": "processing", "last_error": "",
	}).Error; err != nil {
		return nil, false, err
	}
	return &integrationEvent, false, nil
}

func applyStripeShopEvent(
	db *gorm.DB,
	fulfiller payments.Fulfiller,
	refundMirror payments.RefundMirrorEnqueuer,
	event stripe.Event,
) error {
	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		if fulfiller == nil {
			return errors.New("payment fulfiller is not configured")
		}
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return fmt.Errorf("decode PaymentIntent: %w", err)
		}
		expired, err := validateSucceededShopPayment(db, pi, event.Created)
		if err != nil {
			return err
		}
		if expired {
			// Stripe accepted the payment after the selected Shopify quote
			// expired. Preserve the paid-but-canceled order for an operator
			// refund, but never send stale merchandise/pricing to fulfillment.
			return nil
		}
		// Customer data for fulfillment comes from the durable order snapshot
		// (checkout places no PII in PaymentIntent metadata). Metadata is only
		// a fallback for legacy pre-hardening intents without an order row.
		customerEmail := strings.TrimSpace(pi.ReceiptEmail)
		if customerEmail == "" {
			customerEmail = pi.Metadata["customer_email"]
		}
		customerName := pi.Metadata["customer_name"]
		customerPhone := pi.Metadata["customer_phone"]
		if order, lookupErr := findShopOrderForPaymentIntent(db, &pi); lookupErr != nil {
			return lookupErr
		} else if order != nil {
			customerName = order.CustomerName
			customerPhone = order.CustomerPhone
			if strings.TrimSpace(order.CustomerEmail) != "" {
				customerEmail = order.CustomerEmail
			}
		}
		req := payments.FulfillmentRequest{
			PaymentIntentID: pi.ID,
			CustomerName:    customerName,
			CustomerEmail:   customerEmail,
			CustomerPhone:   customerPhone,
			Items:           payments.ParseItemsFromMetadata(pi.Metadata),
		}
		if err := fulfiller.Fulfill(req); err != nil {
			return fmt.Errorf("fulfill %s: %w", pi.ID, err)
		}
		return nil

	case stripe.EventTypePaymentIntentProcessing,
		stripe.EventTypePaymentIntentPaymentFailed,
		stripe.EventTypePaymentIntentCanceled:
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return fmt.Errorf("decode PaymentIntent: %w", err)
		}
		return applyPaymentIntentLifecycle(db, event.Type, pi)

	case stripe.EventTypeRefundCreated,
		stripe.EventTypeRefundUpdated,
		stripe.EventTypeRefundFailed:
		var refund stripe.Refund
		if err := json.Unmarshal(event.Data.Raw, &refund); err != nil {
			return fmt.Errorf("decode Refund: %w", err)
		}
		return applyStripeRefundWebhook(db, refundMirror, event.Type, refund, "", event.Created)

	case stripe.EventTypeChargeRefunded:
		var charge stripe.Charge
		if err := json.Unmarshal(event.Data.Raw, &charge); err != nil {
			return fmt.Errorf("decode Charge: %w", err)
		}
		return applyChargeRefundedWebhook(db, refundMirror, charge, event.Created)

	case stripe.EventTypeChargeDisputeCreated,
		stripe.EventTypeChargeDisputeUpdated,
		stripe.EventTypeChargeDisputeClosed,
		stripe.EventTypeChargeDisputeFundsWithdrawn,
		stripe.EventTypeChargeDisputeFundsReinstated:
		var dispute stripe.Dispute
		if err := json.Unmarshal(event.Data.Raw, &dispute); err != nil {
			return fmt.Errorf("decode Dispute: %w", err)
		}
		return applyStripeDisputeWebhook(db, dispute, event.Created)
	}
	return nil
}

// findShopOrderForPaymentIntent locates the checkout order for a PaymentIntent:
// by back-filled payment_intent_id first, then by the pawrd_order_id metadata
// (covers the window where the intent-id back-fill failed after checkout).
// When found via metadata, the intent id is back-filled immediately so later
// lookups hit the primary path. (nil, nil) means no order exists.
func findShopOrderForPaymentIntent(db *gorm.DB, pi *stripe.PaymentIntent) (*models.ShopOrder, error) {
	paymentIntentID := strings.TrimSpace(pi.ID)
	var order models.ShopOrder
	err := db.Where("payment_intent_id = ?", paymentIntentID).First(&order).Error
	if err == nil {
		return &order, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	orderID := strings.TrimSpace(pi.Metadata["pawrd_order_id"])
	if orderID == "" {
		return nil, nil
	}
	if err := db.Where("id = ?", orderID).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if order.PaymentIntentID == nil {
		if err := db.Model(&models.ShopOrder{}).
			Where("id = ? AND payment_intent_id IS NULL", order.ID).
			Update("payment_intent_id", paymentIntentID).Error; err != nil {
			log.Printf("[stripe-webhook] CRITICAL: back-fill of payment_intent_id=%s on order %s failed: %v", paymentIntentID, order.ID, err)
			return nil, err
		}
		order.PaymentIntentID = &pi.ID
	}
	return &order, nil
}

// safeStripeFailureReason extracts a display-safe failure reason from the
// PaymentIntent's last_payment_error (code + message — designed by Stripe for
// display; contains no card data).
func safeStripeFailureReason(pi *stripe.PaymentIntent) string {
	if pi.LastPaymentError == nil {
		return "Stripe payment failed"
	}
	reason := strings.TrimSpace(string(pi.LastPaymentError.Code))
	if msg := strings.TrimSpace(pi.LastPaymentError.Msg); msg != "" {
		if reason != "" {
			reason += ": "
		}
		reason += msg
	}
	if reason == "" {
		reason = "Stripe payment failed"
	}
	const max = 500
	if len(reason) > max {
		reason = reason[:max]
	}
	return reason
}

// validateSucceededShopPayment binds the signed Stripe object back to the
// exact Pawrd order and sealed Shopify quote that created it. A valid Stripe
// signature proves who sent the event, but it does not prove that its amount,
// currency or metadata still match the local order.
func validateSucceededShopPayment(db *gorm.DB, pi stripe.PaymentIntent, eventCreated int64) (bool, error) {
	paymentIntentID := strings.TrimSpace(pi.ID)
	if paymentIntentID == "" {
		return false, errors.New("PaymentIntent event is missing an ID")
	}
	if db == nil {
		return false, errors.New("shop payment storage is unavailable")
	}

	order, err := findShopOrderForPaymentIntent(db, &pi)
	if err != nil {
		return false, err
	}
	if order == nil {
		return false, fmt.Errorf("PaymentIntent %s has no Pawrd order", paymentIntentID)
	}
	expectedCurrency := strings.ToLower(strings.TrimSpace(order.Currency))
	if order.TotalAmountMinor <= 0 ||
		pi.Amount != order.TotalAmountMinor ||
		pi.AmountReceived != order.TotalAmountMinor ||
		strings.ToLower(strings.TrimSpace(string(pi.Currency))) != expectedCurrency {
		return false, fmt.Errorf(
			"PaymentIntent %s amount/currency does not match Pawrd order %s",
			paymentIntentID,
			order.ID,
		)
	}
	if strings.TrimSpace(pi.Metadata["pawrd_order_id"]) != order.ID {
		return false, fmt.Errorf("PaymentIntent %s metadata does not match Pawrd order %s", paymentIntentID, order.ID)
	}

	quoteID := strings.TrimSpace(pi.Metadata["pawrd_quote_id"])
	if quoteID == "" {
		return false, fmt.Errorf("PaymentIntent %s is missing its Pawrd quote ID", paymentIntentID)
	}
	var quote models.ShopCheckoutQuote
	if err := db.Where(
		"id = ? AND user_id = ? AND order_id = ? AND payment_intent_id = ?",
		quoteID,
		order.UserID,
		order.ID,
		paymentIntentID,
	).First(&quote).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, fmt.Errorf("PaymentIntent %s is not bound to quote %s", paymentIntentID, quoteID)
		}
		return false, err
	}
	if !strings.EqualFold(
		strings.TrimSpace(pi.Metadata["pawrd_quote_version"]),
		strings.TrimSpace(quote.SnapshotSHA256),
	) {
		return false, fmt.Errorf("PaymentIntent %s quote version does not match quote %s", paymentIntentID, quoteID)
	}
	snapshot, err := quote.DecodeAndVerifySnapshot()
	if err != nil {
		return false, fmt.Errorf("verify quote %s for PaymentIntent %s: %w", quote.ID, paymentIntentID, err)
	}
	if quote.TotalAmountMinor != order.TotalAmountMinor ||
		!strings.EqualFold(quote.Currency, order.Currency) ||
		snapshot.Amounts.TotalAmountMinor != order.TotalAmountMinor ||
		!strings.EqualFold(snapshot.Currency, order.Currency) {
		return false, fmt.Errorf("quote %s does not match Pawrd order %s", quote.ID, order.ID)
	}

	metadataExpiry := strings.TrimSpace(pi.Metadata["pawrd_quote_expires_at"])
	if metadataExpiry != "" {
		parsedExpiry, parseErr := time.Parse(time.RFC3339Nano, metadataExpiry)
		if parseErr != nil || !parsedExpiry.Equal(quote.ExpiresAt) {
			return false, fmt.Errorf("PaymentIntent %s quote expiry metadata is invalid", paymentIntentID)
		}
	}
	if eventCreated <= 0 {
		return false, fmt.Errorf("PaymentIntent %s event is missing its creation time", paymentIntentID)
	}
	// Stripe Event.created is an integer Unix timestamp. Quote expiry is stored
	// with microsecond precision, so comparing time.Time values would incorrectly
	// treat an event in the same Unix second as a fractional expiry as timely.
	// Fail closed for that entire boundary second: eventCreated == ExpiresAt.Unix
	// is expired even when ExpiresAt has a fractional component.
	if eventCreated >= quote.ExpiresAt.UTC().Unix() {
		if order.ShopifyOrderGID() != "" {
			return false, fmt.Errorf("expired PaymentIntent %s is already linked to a Shopify order", paymentIntentID)
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			var locked models.ShopOrder
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&locked, "id = ?", order.ID).Error; err != nil {
				return err
			}
			if locked.ShopifyOrderGID() != "" {
				return fmt.Errorf(
					"expired PaymentIntent %s became linked to a Shopify order",
					paymentIntentID,
				)
			}
			switch strings.ToLower(strings.TrimSpace(locked.DisputeStatus)) {
			case "", "won", "prevented", "warning_closed":
			default:
				// The dispute already owns the money lifecycle. Acknowledge the
				// old succeeded event without fulfilling, refunding, or
				// regressing the dispute state.
				return nil
			}
			updates := map[string]any{"fulfillment_status": "CANCELLED"}
			switch strings.ToLower(strings.TrimSpace(locked.FinancialStatus)) {
			case "refunded":
				// A repeated succeeded event must not regress the completed
				// refund's business status or audit reason.
				locked.FulfillmentStatus = "CANCELLED"
			case "partially_refunded":
				// Preserve Stripe's more advanced money state.
				updates["status"] = "canceled"
				updates["failure_reason"] = "Payment completed after the Shopify quote expired; automatic refund queued"
				locked.Status = "canceled"
				locked.FulfillmentStatus = "CANCELLED"
				locked.FailureReason = "Payment completed after the Shopify quote expired; automatic refund queued"
			default:
				updates["status"] = "canceled"
				updates["financial_status"] = "paid"
				updates["failure_reason"] = "Payment completed after the Shopify quote expired; automatic refund queued"
				locked.FinancialStatus = "paid"
				locked.Status = "canceled"
				locked.FulfillmentStatus = "CANCELLED"
				locked.FailureReason = "Payment completed after the Shopify quote expired; automatic refund queued"
			}
			if err := tx.Model(&locked).Updates(updates).Error; err != nil {
				return err
			}
			refund, err := payments.EnsureSystemCompensationRefund(
				tx,
				&locked,
				models.ShopRefundReasonQuoteExpired,
				time.Now().UTC(),
			)
			if err != nil || refund == nil {
				return err
			}
			var job models.ShopCompensationRefundJob
			jobErr := tx.Where("refund_id = ?", refund.ID).First(&job).Error
			if jobErr != nil && !errors.Is(jobErr, gorm.ErrRecordNotFound) {
				return jobErr
			}
			if refund.Status == models.ShopRefundStatusFailed ||
				job.Status == models.ShopCompensationRefundJobFailed {
				return tx.Model(&locked).Updates(map[string]any{
					"status": "refund_reconciliation_required",
					"failure_reason": "Automatic Stripe refund requires reconciliation: " +
						strings.TrimSpace(refund.FailureReason),
				}).Error
			}
			return nil
		}); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func applyStripeDisputeWebhook(db *gorm.DB, dispute stripe.Dispute, eventCreated int64) error {
	paymentIntentID := ""
	if dispute.PaymentIntent != nil {
		paymentIntentID = strings.TrimSpace(dispute.PaymentIntent.ID)
	}
	if paymentIntentID == "" && dispute.Charge != nil && dispute.Charge.PaymentIntent != nil {
		paymentIntentID = strings.TrimSpace(dispute.Charge.PaymentIntent.ID)
	}
	if paymentIntentID == "" {
		return nil
	}
	status := strings.ToLower(strings.TrimSpace(string(dispute.Status)))
	return db.Transaction(func(tx *gorm.DB) error {
		var order models.ShopOrder
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&order, "payment_intent_id = ?", paymentIntentID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if shouldIgnoreStripeDisputeTransition(order, dispute.ID, status, eventCreated) {
			return nil
		}

		updates := map[string]any{
			"dispute_id": dispute.ID, "dispute_status": status,
			"dispute_reason":        strings.ToLower(strings.TrimSpace(string(dispute.Reason))),
			"disputed_amount_minor": dispute.Amount,
		}
		if eventCreated > 0 {
			updates["dispute_event_created"] = eventCreated
		}
		if strings.EqualFold(order.FinancialStatus, "refunded") {
			return tx.Model(&order).Updates(updates).Error
		}
		switch status {
		case "won", "prevented", "warning_closed":
			if strings.EqualFold(order.FinancialStatus, "disputed") ||
				strings.EqualFold(order.FinancialStatus, "dispute_lost") {
				switch {
				case order.TotalAmountMinor > 0 && order.RefundedAmountMinor >= order.TotalAmountMinor:
					updates["financial_status"] = "refunded"
					updates["status"] = "refunded"
				case order.RefundedAmountMinor > 0:
					updates["financial_status"] = "partially_refunded"
				default:
					updates["financial_status"] = "paid"
				}
			}
			if _, alreadySet := updates["status"]; !alreadySet &&
				strings.HasPrefix(strings.ToLower(order.Status), "payment_dispute") {
				updates["status"] = "processing"
			}
			updates["failure_reason"] = ""
		case "lost":
			updates["financial_status"] = "dispute_lost"
			updates["status"] = "payment_dispute_lost"
			updates["failure_reason"] = fmt.Sprintf("Stripe dispute %s was lost", dispute.ID)
		default:
			updates["financial_status"] = "disputed"
			updates["status"] = "payment_disputed"
			updates["failure_reason"] = fmt.Sprintf("Stripe dispute %s requires review", dispute.ID)
		}
		return tx.Model(&order).Updates(updates).Error
	})
}

func shouldIgnoreStripeDisputeTransition(
	order models.ShopOrder,
	disputeID string,
	incomingStatus string,
	eventCreated int64,
) bool {
	if order.DisputeEventCreated > 0 && eventCreated > 0 &&
		eventCreated < order.DisputeEventCreated {
		return true
	}
	if strings.TrimSpace(order.DisputeID) == "" ||
		!strings.EqualFold(strings.TrimSpace(order.DisputeID), strings.TrimSpace(disputeID)) {
		return false
	}
	existingStatus := strings.ToLower(strings.TrimSpace(order.DisputeStatus))
	if !terminalStripeDisputeStatus(existingStatus) {
		return false
	}
	// A closed dispute is immutable for the same Stripe dispute ID. Conflicting
	// terminal snapshots without a strictly separate dispute are held for
	// reconciliation instead of guessing which money outcome is authoritative.
	return !strings.EqualFold(existingStatus, incomingStatus)
}

func terminalStripeDisputeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "won", "lost", "prevented", "warning_closed":
		return true
	default:
		return false
	}
}

func applyPaymentIntentLifecycle(db *gorm.DB, eventType stripe.EventType, pi stripe.PaymentIntent) error {
	if strings.TrimSpace(pi.ID) == "" {
		return errors.New("PaymentIntent event is missing an ID")
	}
	updates := map[string]any{}
	switch eventType {
	case stripe.EventTypePaymentIntentProcessing:
		updates["status"] = "payment_processing"
		updates["financial_status"] = "pending"
		updates["failure_reason"] = ""
	case stripe.EventTypePaymentIntentPaymentFailed:
		updates["status"] = "payment_failed"
		updates["financial_status"] = "failed"
		updates["failure_reason"] = safeStripeFailureReason(&pi)
	case stripe.EventTypePaymentIntentCanceled:
		updates["status"] = "payment_canceled"
		updates["financial_status"] = "voided"
		failure := "Stripe payment was canceled"
		if pi.CancellationReason != "" {
			failure += ": " + string(pi.CancellationReason)
		}
		updates["failure_reason"] = failure
	}

	// Locate the order (metadata fallback covers a missing intent-id
	// back-fill). No order → acknowledge; there is nothing to transition.
	order, err := findShopOrderForPaymentIntent(db, &pi)
	if err != nil {
		return err
	}
	if order == nil {
		log.Printf("[stripe-webhook] %s for %s has no persisted order; acknowledging", eventType, pi.ID)
		return nil
	}

	// Ignore a delayed/out-of-order non-success event after the order has
	// already reached a paid or refunded financial state. Status and
	// financial_status move atomically in this single conditional UPDATE.
	return db.Model(&models.ShopOrder{}).
		Where("id = ? AND COALESCE(LOWER(financial_status), '') NOT IN ?", order.ID,
			[]string{"paid", "partially_refunded", "refunded", "disputed", "dispute_lost"}).
		Where("COALESCE(LOWER(dispute_status), '') IN ?",
			[]string{"", "won", "prevented", "warning_closed"}).
		Updates(updates).Error
}

func applyStripeRefundWebhook(
	db *gorm.DB,
	refundMirror payments.RefundMirrorEnqueuer,
	eventType stripe.EventType,
	stripeRefund stripe.Refund,
	fallbackPaymentIntentID string,
	eventCreated int64,
) error {
	stripeRefundID := strings.TrimSpace(stripeRefund.ID)
	if stripeRefundID == "" {
		return errors.New("Refund event is missing an ID")
	}
	paymentIntentID := strings.TrimSpace(fallbackPaymentIntentID)
	if stripeRefund.PaymentIntent != nil && strings.TrimSpace(stripeRefund.PaymentIntent.ID) != "" {
		paymentIntentID = strings.TrimSpace(stripeRefund.PaymentIntent.ID)
	}

	var succeededRefundID string
	err := db.Transaction(func(tx *gorm.DB) error {
		var refundRecord models.ShopRefund
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("stripe_refund_id = ?", stripeRefundID).First(&refundRecord).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if pawrdRefundID := strings.TrimSpace(stripeRefund.Metadata["pawrd_refund_id"]); pawrdRefundID != "" {
				err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					First(&refundRecord, "id = ?", pawrdRefundID).Error
			}
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var order models.ShopOrder
		if refundRecord.ID != "" {
			if paymentIntentID != "" && refundRecord.PaymentIntentID != paymentIntentID {
				return fmt.Errorf("Refund %s PaymentIntent does not match Pawrd refund %s", stripeRefundID, refundRecord.ID)
			}
			if refundRecord.StripeRefundID != nil &&
				strings.TrimSpace(*refundRecord.StripeRefundID) != "" &&
				strings.TrimSpace(*refundRecord.StripeRefundID) != stripeRefundID {
				return fmt.Errorf(
					"Refund %s does not match Pawrd refund %s Stripe ID",
					stripeRefundID,
					refundRecord.ID,
				)
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&order, "id = ?", refundRecord.OrderID).Error; err != nil {
				return err
			}
			if paymentIntentID == "" {
				paymentIntentID = refundRecord.PaymentIntentID
			}
		} else {
			if paymentIntentID == "" {
				return nil // Unrelated Stripe refund with no Pawrd order reference.
			}
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&order, "payment_intent_id = ?", paymentIntentID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			refundRecord = models.ShopRefund{
				ID: uuid.NewString(), OrderID: order.ID, PaymentIntentID: paymentIntentID,
				IdempotencyKey: "stripe:" + stripeRefundID, RequestedBy: "stripe-webhook",
				Status: models.ShopRefundStatusPending,
			}
		}

		localStatus, completedAt := localShopRefundStatus(eventType, stripeRefund.Status)
		if refundRecord.ID != "" &&
			shouldIgnoreStripeRefundTransition(refundRecord, localStatus, eventCreated) {
			return nil
		}
		reason := strings.TrimSpace(string(stripeRefund.Reason))
		if refundRecord.Reason == models.ShopRefundReasonQuoteExpired ||
			refundRecord.Reason == models.ShopRefundReasonFulfillmentFailed {
			// Stripe's fixed reason enum is only a transport mapping; retain
			// Pawrd's precise system compensation cause.
			reason = refundRecord.Reason
		}
		if reason == "" {
			reason = refundRecord.Reason
		}
		if reason == "" {
			reason = "external"
		}
		currency := strings.ToUpper(strings.TrimSpace(string(stripeRefund.Currency)))
		if currency == "" {
			currency = strings.ToUpper(order.Currency)
		}
		amount := stripeRefund.Amount
		if amount <= 0 {
			amount = refundRecord.AmountMinor
		}
		refundRecord.StripeRefundID = &stripeRefundID
		refundRecord.AmountMinor = amount
		refundRecord.Currency = currency
		refundRecord.Reason = reason
		refundRecord.Status = localStatus
		refundRecord.StripeStatus = strings.ToLower(strings.TrimSpace(string(stripeRefund.Status)))
		if eventCreated > 0 {
			refundRecord.StripeEventCreated = eventCreated
		}
		refundRecord.FailureReason = strings.TrimSpace(string(stripeRefund.FailureReason))
		refundRecord.CompletedAt = completedAt
		if localStatus == models.ShopRefundStatusSucceeded &&
			refundRecord.ShopifyMirrorStatus != models.ShopRefundMirrorStatusSucceeded {
			refundRecord.ShopifyMirrorStatus = models.ShopRefundMirrorStatusPending
			refundRecord.ShopifyMirrorError = ""
		}

		if refundRecord.CreatedAt.IsZero() {
			if err := tx.Create(&refundRecord).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&refundRecord).Error; err != nil {
			return err
		}
		if err := recalculateShopOrderRefundState(tx, refundRecord.OrderID, nil); err != nil {
			return err
		}
		if localStatus == models.ShopRefundStatusSucceeded {
			succeededRefundID = refundRecord.ID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if refundMirror != nil && succeededRefundID != "" {
		if err := refundMirror.EnqueueRefundMirror(context.Background(), succeededRefundID); err != nil {
			return fmt.Errorf("enqueue Shopify refund mirror: %w", err)
		}
	}
	return nil
}

func localShopRefundStatus(eventType stripe.EventType, stripeStatus stripe.RefundStatus) (string, *time.Time) {
	normalized := strings.ToLower(strings.TrimSpace(string(stripeStatus)))
	if eventType == stripe.EventTypeRefundFailed || normalized == "failed" || normalized == "canceled" {
		return models.ShopRefundStatusFailed, nil
	}
	if normalized == "succeeded" {
		now := time.Now().UTC()
		return models.ShopRefundStatusSucceeded, &now
	}
	return models.ShopRefundStatusPending, nil
}

func shouldIgnoreStripeRefundTransition(
	existing models.ShopRefund,
	incomingStatus string,
	eventCreated int64,
) bool {
	if existing.StripeEventCreated > 0 && eventCreated > 0 &&
		eventCreated < existing.StripeEventCreated {
		return true
	}
	existingStatus := strings.ToLower(strings.TrimSpace(existing.Status))
	incomingStatus = strings.ToLower(strings.TrimSpace(incomingStatus))
	switch existingStatus {
	case models.ShopRefundStatusSucceeded:
		return incomingStatus != models.ShopRefundStatusSucceeded
	case models.ShopRefundStatusFailed:
		return incomingStatus == models.ShopRefundStatusPending
	default:
		return false
	}
}

func applyChargeRefundedWebhook(
	db *gorm.DB,
	refundMirror payments.RefundMirrorEnqueuer,
	charge stripe.Charge,
	eventCreated int64,
) error {
	paymentIntentID := ""
	if charge.PaymentIntent != nil {
		paymentIntentID = strings.TrimSpace(charge.PaymentIntent.ID)
	}
	if paymentIntentID == "" {
		return nil
	}

	// charge.refunded includes refund objects in normal Stripe deliveries. Upsert
	// them first so externally-created refunds are auditable in Pawrd.
	if charge.Refunds != nil {
		for _, refund := range charge.Refunds.Data {
			if refund == nil {
				continue
			}
			if err := applyStripeRefundWebhook(
				db,
				refundMirror,
				stripe.EventTypeRefundUpdated,
				*refund,
				paymentIntentID,
				eventCreated,
			); err != nil {
				return err
			}
		}
	}

	var order models.ShopOrder
	err := db.First(&order, "payment_intent_id = ?", paymentIntentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	observedAmount := charge.AmountRefunded
	return db.Transaction(func(tx *gorm.DB) error {
		return recalculateShopOrderRefundState(tx, order.ID, &observedAmount)
	})
}
