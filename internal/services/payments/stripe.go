package payments

import (
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/paymentintent"
	"github.com/vf0429/Petwell_Backend/internal/config"
)

type StripeService struct {
	publishableKey string
}

type CreatePaymentIntentRequest struct {
	Amount        int64
	Currency      string
	Description   string
	ReceiptEmail  string
	Metadata      map[string]string
	StatementNote string
}

type CreatePaymentIntentResponse struct {
	ClientSecret    string
	PaymentIntentID string
	PublishableKey  string
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

	params := &stripe.PaymentIntentParams{
		Amount:      stripe.Int64(req.Amount),
		Currency:    stripe.String(currency),
		Description: stripe.String(req.Description),
	}
	params.AutomaticPaymentMethods = &stripe.PaymentIntentAutomaticPaymentMethodsParams{
		Enabled: stripe.Bool(true),
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
