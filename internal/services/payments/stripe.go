package payments

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v83"
	"github.com/stripe/stripe-go/v83/paymentintent"
	"github.com/stripe/stripe-go/v83/refund"
	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
)

// Stripe guarantees idempotency-key retention for at least 24 hours. Pawrd
// stops automatic money retries one hour before that minimum boundary so a
// pruned key can never turn a retry into a second refund.
const StripeRefundIdempotencyRetryWindow = 23 * time.Hour

type StripeService struct {
	publishableKey string
}

type CreatePaymentIntentRequest struct {
	Amount         int64
	Currency       string
	Description    string
	ReceiptEmail   string
	Metadata       map[string]string
	StatementNote  string
	IdempotencyKey string
}

type CreatePaymentIntentResponse struct {
	ClientSecret    string
	PaymentIntentID string
	PublishableKey  string
}

type CreateRefundRequest struct {
	PaymentIntentID string
	AmountMinor     int64
	Reason          string
	IdempotencyKey  string
	PawrdRefundID   string
	PawrdOrderID    string
}

type CreateRefundResponse struct {
	RefundID      string
	Status        string
	FailureReason string
}

// Refunder is intentionally small so the admin refund handler can be tested
// without making a Stripe request.
type Refunder interface {
	CreateRefund(context.Context, CreateRefundRequest) (*CreateRefundResponse, error)
}

func NewStripeService(cfg *config.Config) (*StripeService, error) {
	if err := cfg.ValidateStripeConfig(); err != nil {
		return nil, err
	}

	stripe.Key = cfg.StripeSecretKey

	return &StripeService{
		publishableKey: cfg.StripePublishableKey,
	}, nil
}

func (s *StripeService) CreatePaymentIntent(req CreatePaymentIntentRequest) (*CreatePaymentIntentResponse, error) {
	if req.Amount <= 0 {
		return nil, fmt.Errorf("payment amount must be greater than zero")
	}

	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	if currency == "" {
		return nil, fmt.Errorf("payment currency is required")
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("payment idempotency key is required")
	}
	if len(idempotencyKey) > 255 {
		return nil, fmt.Errorf("payment idempotency key is too long")
	}

	params := &stripe.PaymentIntentParams{
		Amount:             stripe.Int64(req.Amount),
		Currency:           stripe.String(currency),
		Description:        stripe.String(req.Description),
		PaymentMethodTypes: stripe.StringSlice([]string{"card"}),
	}

	if receiptEmail := strings.TrimSpace(req.ReceiptEmail); receiptEmail != "" {
		params.ReceiptEmail = stripe.String(receiptEmail)
	}

	if len(req.Metadata) > 0 {
		params.Metadata = map[string]string{}
		for key, value := range req.Metadata {
			trimmedKey := strings.TrimSpace(key)
			trimmedValue := strings.TrimSpace(value)
			if trimmedKey == "" || trimmedValue == "" {
				continue
			}
			params.Metadata[trimmedKey] = trimmedValue
		}
	}

	if suffix := strings.TrimSpace(req.StatementNote); suffix != "" {
		params.StatementDescriptorSuffix = stripe.String(suffix)
	}
	params.SetIdempotencyKey(idempotencyKey)

	intent, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("create payment intent: %w", err)
	}

	return &CreatePaymentIntentResponse{
		ClientSecret:    intent.ClientSecret,
		PaymentIntentID: intent.ID,
		PublishableKey:  s.publishableKey,
	}, nil
}

// CancelPaymentIntent compensates for a local checkout failure after Stripe has
// already created an intent. It prevents an orphan intent from being confirmed
// without a durable Pawrd order to receive the payment webhook.
func (s *StripeService) CancelPaymentIntent(paymentIntentID string) error {
	paymentIntentID = strings.TrimSpace(paymentIntentID)
	if paymentIntentID == "" {
		return fmt.Errorf("payment intent ID is required")
	}

	_, err := paymentintent.Cancel(paymentIntentID, &stripe.PaymentIntentCancelParams{
		CancellationReason: stripe.String(stripe.PaymentIntentCancellationReasonAbandoned),
	})
	if err != nil {
		return fmt.Errorf("cancel payment intent: %w", err)
	}
	return nil
}

// CreateRefund refunds an explicit amount from a PaymentIntent. Pawrd's
// database idempotency key is forwarded to Stripe so a timeout can be retried
// without creating a second refund.
func (s *StripeService) CreateRefund(ctx context.Context, req CreateRefundRequest) (*CreateRefundResponse, error) {
	paymentIntentID := strings.TrimSpace(req.PaymentIntentID)
	if paymentIntentID == "" {
		return nil, fmt.Errorf("payment intent ID is required")
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("refund amount must be greater than zero")
	}
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("refund idempotency key is required")
	}

	reason := strings.ToLower(strings.TrimSpace(req.Reason))
	stripeReason := reason
	switch reason {
	case models.ShopRefundReasonQuoteExpired,
		models.ShopRefundReasonFulfillmentFailed:
		// Stripe supports only its fixed refund-reason enum. Keep the precise
		// system reason in Pawrd and Stripe metadata while mapping the API field
		// to the closest legal value.
		stripeReason = string(stripe.RefundReasonRequestedByCustomer)
	}
	switch stripe.RefundReason(stripeReason) {
	case stripe.RefundReasonDuplicate, stripe.RefundReasonFraudulent, stripe.RefundReasonRequestedByCustomer:
	default:
		return nil, fmt.Errorf("unsupported Stripe refund reason %q", reason)
	}

	params := &stripe.RefundParams{
		Amount:        stripe.Int64(req.AmountMinor),
		PaymentIntent: stripe.String(paymentIntentID),
		Reason:        stripe.String(stripeReason),
		Metadata: map[string]string{
			"pawrd_refund_id": strings.TrimSpace(req.PawrdRefundID),
			"pawrd_order_id":  strings.TrimSpace(req.PawrdOrderID),
			"pawrd_reason":    reason,
		},
	}
	params.Context = ctx
	params.SetIdempotencyKey(idempotencyKey)

	result, err := refund.New(params)
	if err != nil {
		return nil, fmt.Errorf("create Stripe refund: %w", err)
	}
	return &CreateRefundResponse{
		RefundID:      result.ID,
		Status:        string(result.Status),
		FailureReason: string(result.FailureReason),
	}, nil
}
