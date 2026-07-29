package handlers

import (
	"encoding/json"
	"errors"
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
)

// NewPaymentsWebhookHandler handles Stripe webhook events. In Phase B it processes
// `payment_intent.succeeded` and dispatches fulfillment via the payments.Fulfiller.
// This closes the reliability gap where payment success was only known client-side
// (iOS PaymentSheet onCompletion) — the server now has an authoritative trigger for
// order fulfillment. See docs/hicustom_integration_design.md §13.
func NewPaymentsWebhookHandler(cfg *config.Config, db *gorm.DB, fulfiller payments.Fulfiller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		if r.Method == http.MethodOptions {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		const maxBody = 1 << 20 // 1 MiB, Stripe payloads are well under this
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

		// Handle the PaymentIntent lifecycle events that drive order status;
		// acknowledge everything else so Stripe stops retrying it.
		switch event.Type {
		case stripe.EventTypePaymentIntentSucceeded,
			stripe.EventTypePaymentIntentPaymentFailed,
			stripe.EventTypePaymentIntentCanceled:
			// handled below
		default:
			log.Printf("[stripe-webhook] ignoring event type %s (%s)", event.Type, event.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		var integrationEvent models.ShopIntegrationEvent
		err = db.Where("provider = ? AND external_event_id = ?", "stripe", event.ID).First(&integrationEvent).Error
		if err == nil && integrationEvent.Status == "completed" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			http.Error(w, "event store unavailable", http.StatusInternalServerError)
			return
		}
		if integrationEvent.ID == "" {
			integrationEvent = models.ShopIntegrationEvent{
				ID: uuid.NewString(), Provider: "stripe", ExternalEventID: event.ID,
				Topic: string(event.Type), Status: "processing",
			}
			if err := db.Create(&integrationEvent).Error; err != nil {
				http.Error(w, "event store unavailable", http.StatusInternalServerError)
				return
			}
		} else {
			_ = db.Model(&integrationEvent).Updates(map[string]any{"status": "processing", "last_error": ""}).Error
		}

		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			log.Printf("[stripe-webhook] unmarshal paymentintent failed: %v", err)
			http.Error(w, "invalid event data", http.StatusBadRequest)
			return
		}

		// Order lookup: by payment_intent_id first, then by the pawrd_order_id
		// metadata (covers the window where the intent id back-fill failed).
		// A real DB error → 500 so Stripe retries.
		order, err := findOrderForPaymentIntent(db, &pi)
		if err != nil {
			http.Error(w, "order store unavailable", http.StatusInternalServerError)
			return
		}

		// If the order was found via metadata and has no intent id yet, close
		// the reconciliation gap now so later lookups hit the primary path.
		if order != nil && order.PaymentIntentID == nil {
			if err := db.Model(&models.ShopOrder{}).Where("id = ? AND payment_intent_id IS NULL", order.ID).
				Update("payment_intent_id", pi.ID).Error; err != nil {
				log.Printf("[stripe-webhook] CRITICAL: back-fill of payment_intent_id=%s on order %s failed: %v", pi.ID, order.ID, err)
				http.Error(w, "order store unavailable", http.StatusInternalServerError)
				return
			}
			order.PaymentIntentID = &pi.ID
		}

		switch event.Type {
		case stripe.EventTypePaymentIntentSucceeded:
			handlePaymentSucceeded(w, db, fulfiller, &integrationEvent, &pi, order)
		case stripe.EventTypePaymentIntentPaymentFailed:
			handlePaymentTerminal(w, db, &integrationEvent, &pi, order, "payment_failed", "failed", safeStripeFailureReason(&pi))
		case stripe.EventTypePaymentIntentCanceled:
			handlePaymentTerminal(w, db, &integrationEvent, &pi, order, "canceled", "voided", "")
		}
	}
}

// findOrderForPaymentIntent locates the checkout order for a PaymentIntent:
// by back-filled payment_intent_id first, then by pawrd_order_id metadata.
// (nil, nil) means no order exists — callers fall back to legacy metadata
// handling. A non-nil error is a REAL database error (→ 500, Stripe retries).
func findOrderForPaymentIntent(db *gorm.DB, pi *stripe.PaymentIntent) (*models.ShopOrder, error) {
	var order models.ShopOrder
	err := db.Where("payment_intent_id = ?", pi.ID).First(&order).Error
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
	err = db.Where("id = ?", orderID).First(&order).Error
	if err == nil {
		return &order, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return nil, nil
}

// safeStripeFailureReason extracts a display-safe failure reason from the
// PaymentIntent's last_payment_error (code + message — designed by Stripe for
// display; contains no card data).
func safeStripeFailureReason(pi *stripe.PaymentIntent) string {
	if pi.LastPaymentError == nil {
		return "payment failed"
	}
	reason := strings.TrimSpace(string(pi.LastPaymentError.Code))
	if msg := strings.TrimSpace(pi.LastPaymentError.Msg); msg != "" {
		if reason != "" {
			reason += ": "
		}
		reason += msg
	}
	if reason == "" {
		reason = "payment failed"
	}
	const max = 500
	if len(reason) > max {
		reason = reason[:max]
	}
	return reason
}

// nonRegressableOrderStatuses are paid-or-later states that failure/cancel
// events must never roll back (e.g. a failed event arriving after succeeded).
var nonRegressableOrderStatuses = []string{"paid", "processing", "fulfilled", "shipped", "delivered", "refunded"}

func handlePaymentSucceeded(w http.ResponseWriter, db *gorm.DB, fulfiller payments.Fulfiller, integrationEvent *models.ShopIntegrationEvent, pi *stripe.PaymentIntent, order *models.ShopOrder) {
	customerEmail := strings.TrimSpace(pi.ReceiptEmail)
	if customerEmail == "" {
		customerEmail = pi.Metadata["customer_email"]
	}
	customerName := pi.Metadata["customer_name"]
	customerPhone := pi.Metadata["customer_phone"]

	// The persisted order snapshot is authoritative for customer/shipping data
	// (checkout places no PII in PaymentIntent metadata). Metadata is only a
	// fallback for legacy pre-hardening intents without a persisted order.
	if order != nil {
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

	// Synchronous fulfillment is fine while branches are logs. Once the
	// HiCustom branch makes a real (slow) API call, enqueue and return 200
	// after enqueue — never block a webhook on a factory round-trip.
	if err := fulfiller.Fulfill(req); err != nil {
		_ = db.Model(integrationEvent).Updates(map[string]any{"status": "failed", "last_error": err.Error()}).Error
		log.Printf("[stripe-webhook] fulfillment failed for %s: %v", pi.ID, err)
		// 500 → Stripe retries. We have not fulfilled, so retrying is correct.
		http.Error(w, "fulfillment failed", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	_ = db.Model(integrationEvent).Updates(map[string]any{"status": "completed", "processed_at": &now, "last_error": ""}).Error

	log.Printf("[stripe-webhook] payment_intent.succeeded handled: %s (%d item(s))", pi.ID, len(req.Items))
	w.WriteHeader(http.StatusOK)
}

// handlePaymentTerminal applies payment_failed / canceled transitions,
// updating status and financial_status atomically in one conditional UPDATE.
// Idempotent: the UPDATE never regresses an order that already reached a
// paid-or-later state, and replays converge to the same status.
// financial_status vocabulary follows the codebase's Shopify-style lowercase
// convention (pending/paid/refunded): payment_failed → "failed",
// canceled → "voided".
func handlePaymentTerminal(w http.ResponseWriter, db *gorm.DB, integrationEvent *models.ShopIntegrationEvent, pi *stripe.PaymentIntent, order *models.ShopOrder, targetStatus, targetFinancialStatus, failureReason string) {
	if order == nil {
		// No persisted order (legacy intent) — nothing to transition.
		log.Printf("[stripe-webhook] %s for %s has no persisted order; acknowledging", targetStatus, pi.ID)
	} else {
		updates := map[string]any{"status": targetStatus, "financial_status": targetFinancialStatus}
		if failureReason != "" {
			updates["failure_reason"] = failureReason
		}
		res := db.Model(&models.ShopOrder{}).
			Where("id = ? AND status NOT IN ?", order.ID, nonRegressableOrderStatuses).
			Updates(updates)
		if res.Error != nil {
			_ = db.Model(integrationEvent).Updates(map[string]any{"status": "failed", "last_error": res.Error.Error()}).Error
			http.Error(w, "order store unavailable", http.StatusInternalServerError)
			return
		}
		if res.RowsAffected == 0 {
			log.Printf("[stripe-webhook] %s for %s ignored: order %s already %q (no regression)", targetStatus, pi.ID, order.ID, order.Status)
		}
	}

	now := time.Now().UTC()
	_ = db.Model(integrationEvent).Updates(map[string]any{"status": "completed", "processed_at": &now, "last_error": ""}).Error
	log.Printf("[stripe-webhook] %s handled: %s", targetStatus, pi.ID)
	w.WriteHeader(http.StatusOK)
}
