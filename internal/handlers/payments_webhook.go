package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
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

		// Only fulfillment is wired here; acknowledge all other events so Stripe
		// stops retrying them.
		if event.Type != stripe.EventTypePaymentIntentSucceeded {
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

		customerEmail := pi.ReceiptEmail
		if customerEmail == "" {
			customerEmail = pi.Metadata["customer_email"]
		}

		req := payments.FulfillmentRequest{
			PaymentIntentID: pi.ID,
			CustomerName:    pi.Metadata["customer_name"],
			CustomerEmail:   customerEmail,
			CustomerPhone:   pi.Metadata["customer_phone"],
			Items:           payments.ParseItemsFromMetadata(pi.Metadata),
		}

		// Synchronous fulfillment is fine while branches are logs. Once the
		// HiCustom branch makes a real (slow) API call, enqueue and return 200
		// after enqueue — never block a webhook on a factory round-trip.
		if err := fulfiller.Fulfill(req); err != nil {
			_ = db.Model(&integrationEvent).Updates(map[string]any{"status": "failed", "last_error": err.Error()}).Error
			log.Printf("[stripe-webhook] fulfillment failed for %s: %v", pi.ID, err)
			// 500 → Stripe retries. We have not fulfilled, so retrying is correct.
			http.Error(w, "fulfillment failed", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		_ = db.Model(&integrationEvent).Updates(map[string]any{"status": "completed", "processed_at": &now, "last_error": ""}).Error

		log.Printf("[stripe-webhook] payment_intent.succeeded handled: %s (%d item(s))", pi.ID, len(req.Items))
		w.WriteHeader(http.StatusOK)
	}
}
