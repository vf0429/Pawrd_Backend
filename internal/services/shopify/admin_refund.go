package shopify

import (
	"context"
	"fmt"
	"math/big"
	"strings"
)

// AdminExternalRefundInput records a refund that Stripe has already completed.
// Shopify is an operational ledger only: the selected parent must be the
// manual/external Stripe transaction created with the Pawrd order.
type AdminExternalRefundInput struct {
	OrderID        string
	StripeRefundID string
	AmountMinor    int64
	Currency       string
	IdempotencyKey string
}

type AdminExternalRefundResult struct {
	RefundID      string
	TransactionID string
	AmountMinor   int64
	Currency      string
}

type AdminRefundMirrorClient interface {
	RecordExternalRefund(context.Context, AdminExternalRefundInput) (*AdminExternalRefundResult, error)
}

type adminExternalTransaction struct {
	ID                   string `json:"id"`
	Gateway              string `json:"gateway"`
	Kind                 string `json:"kind"`
	Status               string `json:"status"`
	ManualPaymentGateway bool   `json:"manualPaymentGateway"`
	AmountSet            struct {
		PresentmentMoney struct {
			Amount       string `json:"amount"`
			CurrencyCode string `json:"currencyCode"`
		} `json:"presentmentMoney"`
	} `json:"amountSet"`
}

// RecordExternalRefund creates only a Shopify external REFUND transaction.
// Stripe has already moved the money before this method is called. Omitting
// refundLineItems and shipping deliberately prevents inventory restocking or
// a second product/shipping reimbursement calculation in Shopify.
func (c *AdminClient) RecordExternalRefund(
	ctx context.Context,
	input AdminExternalRefundInput,
) (*AdminExternalRefundResult, error) {
	orderID := strings.TrimSpace(input.OrderID)
	if !strings.HasPrefix(orderID, "gid://shopify/Order/") {
		return nil, fmt.Errorf("shopify refund mirror requires an order GID")
	}
	if strings.TrimSpace(input.StripeRefundID) == "" {
		return nil, fmt.Errorf("shopify refund mirror requires a Stripe refund ID")
	}
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if len(currency) != 3 {
		return nil, fmt.Errorf("shopify refund mirror requires an ISO currency")
	}
	if input.AmountMinor <= 0 {
		return nil, fmt.Errorf("shopify refund mirror requires a positive amount")
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 255 {
		return nil, fmt.Errorf("shopify refund mirror requires a valid idempotency key")
	}

	parent, err := c.externalStripeRefundParent(ctx, orderID, input.AmountMinor, currency)
	if err != nil {
		return nil, err
	}
	amount, err := shopifyMoneyFromMinor(input.AmountMinor, currency)
	if err != nil {
		return nil, err
	}
	const mutation = `mutation RecordPawrdExternalRefund(
	  $input: RefundInput!,
	  $idempotencyKey: String!
	) {
	  refundCreate(input: $input) @idempotent(key: $idempotencyKey) {
	    refund {
	      id
	      totalRefundedSet {
	        presentmentMoney { amount currencyCode }
	      }
	      transactions(first: 10) {
	        nodes {
	          id gateway kind status
	          amountSet {
	            presentmentMoney { amount currencyCode }
	          }
	        }
	      }
	    }
	    userErrors { field message }
	  }
	}`
	var data struct {
		RefundCreate struct {
			Refund *struct {
				ID               string `json:"id"`
				TotalRefundedSet struct {
					PresentmentMoney struct {
						Amount       string `json:"amount"`
						CurrencyCode string `json:"currencyCode"`
					} `json:"presentmentMoney"`
				} `json:"totalRefundedSet"`
				Transactions struct {
					Nodes []adminExternalTransaction `json:"nodes"`
				} `json:"transactions"`
			} `json:"refund"`
			UserErrors []struct {
				Field   []string `json:"field"`
				Message string   `json:"message"`
			} `json:"userErrors"`
		} `json:"refundCreate"`
	}
	variables := map[string]any{
		"idempotencyKey": idempotencyKey,
		"input": map[string]any{
			"orderId":  orderID,
			"currency": currency,
			"notify":   false,
			"note": fmt.Sprintf(
				"External Stripe refund %s already completed; mirrored by Pawrd",
				strings.TrimSpace(input.StripeRefundID),
			),
			"transactions": []map[string]any{{
				"orderId":  orderID,
				"parentId": parent.ID,
				"kind":     "REFUND",
				"gateway":  parent.Gateway,
				"amount":   amount,
			}},
		},
	}
	if err := c.execute(ctx, mutation, variables, &data); err != nil {
		// The request might have reached Shopify before a transport failure.
		// The durable worker always retries this exact mutation with the same
		// idempotency key, so it cannot create a second external transaction.
		return nil, err
	}
	if len(data.RefundCreate.UserErrors) > 0 {
		return nil, fmt.Errorf("shopify refundCreate: %s", data.RefundCreate.UserErrors[0].Message)
	}
	if data.RefundCreate.Refund == nil || strings.TrimSpace(data.RefundCreate.Refund.ID) == "" {
		return nil, fmt.Errorf("shopify refundCreate returned no refund")
	}
	total := data.RefundCreate.Refund.TotalRefundedSet.PresentmentMoney
	if err := validateShopifyMoney(total.Amount, total.CurrencyCode, input.AmountMinor, currency); err != nil {
		return nil, fmt.Errorf("shopify refund total mismatch: %w", err)
	}
	for _, transaction := range data.RefundCreate.Refund.Transactions.Nodes {
		if !strings.EqualFold(transaction.Kind, "REFUND") {
			continue
		}
		money := transaction.AmountSet.PresentmentMoney
		if err := validateShopifyMoney(money.Amount, money.CurrencyCode, input.AmountMinor, currency); err != nil {
			continue
		}
		if !strings.EqualFold(transaction.Gateway, parent.Gateway) {
			continue
		}
		if !strings.EqualFold(transaction.Status, "SUCCESS") {
			return nil, fmt.Errorf(
				"shopify external refund transaction %s is %s",
				transaction.ID,
				transaction.Status,
			)
		}
		return &AdminExternalRefundResult{
			RefundID:      data.RefundCreate.Refund.ID,
			TransactionID: transaction.ID,
			AmountMinor:   input.AmountMinor,
			Currency:      currency,
		}, nil
	}
	return nil, fmt.Errorf("shopify refundCreate returned no exact successful external refund transaction")
}

func (c *AdminClient) externalStripeRefundParent(
	ctx context.Context,
	orderID string,
	amountMinor int64,
	currency string,
) (*adminExternalTransaction, error) {
	const query = `query PawrdExternalStripeRefundParent($orderId: ID!) {
	  order(id: $orderId) {
	    id
	    transactions(first: 100) {
	      id gateway kind status manualPaymentGateway
	      amountSet {
	        presentmentMoney { amount currencyCode }
	      }
	    }
	  }
	}`
	var data struct {
		Order *struct {
			ID           string                     `json:"id"`
			Transactions []adminExternalTransaction `json:"transactions"`
		} `json:"order"`
	}
	if err := c.execute(ctx, query, map[string]any{"orderId": orderID}, &data); err != nil {
		return nil, err
	}
	if data.Order == nil {
		return nil, fmt.Errorf("shopify refund mirror order was not found")
	}
	for _, preferredKind := range []string{"CAPTURE", "SALE"} {
		for i := range data.Order.Transactions {
			transaction := &data.Order.Transactions[i]
			if !strings.EqualFold(transaction.Kind, preferredKind) ||
				!strings.EqualFold(transaction.Status, "SUCCESS") ||
				!strings.EqualFold(strings.TrimSpace(transaction.Gateway), "Stripe") ||
				!transaction.ManualPaymentGateway {
				continue
			}
			money := transaction.AmountSet.PresentmentMoney
			parentAmount, err := shopifyMoneyToMinor(money.Amount, money.CurrencyCode)
			if err != nil ||
				!strings.EqualFold(strings.TrimSpace(money.CurrencyCode), currency) ||
				parentAmount < amountMinor {
				continue
			}
			return transaction, nil
		}
	}
	return nil, fmt.Errorf("shopify order has no eligible external Stripe SALE/CAPTURE transaction")
}

func validateShopifyMoney(amount, actualCurrency string, expectedMinor int64, expectedCurrency string) error {
	if !strings.EqualFold(strings.TrimSpace(actualCurrency), strings.TrimSpace(expectedCurrency)) {
		return fmt.Errorf(
			"expected %s, got %s",
			strings.ToUpper(expectedCurrency),
			strings.ToUpper(actualCurrency),
		)
	}
	actualMinor, err := shopifyMoneyToMinor(amount, actualCurrency)
	if err != nil {
		return err
	}
	if actualMinor != expectedMinor {
		return fmt.Errorf("expected %d minor units, got %d", expectedMinor, actualMinor)
	}
	return nil
}

func shopifyMoneyFromMinor(amountMinor int64, currency string) (string, error) {
	if amountMinor < 0 {
		return "", fmt.Errorf("money amount cannot be negative")
	}
	scale := currencyMinorUnit(strings.ToUpper(strings.TrimSpace(currency)))
	divisor := int64(1)
	for i := 0; i < scale; i++ {
		divisor *= 10
	}
	if scale == 0 {
		return fmt.Sprintf("%d", amountMinor), nil
	}
	return fmt.Sprintf("%d.%0*d", amountMinor/divisor, scale, amountMinor%divisor), nil
}

func shopifyMoneyToMinor(amount, currency string) (int64, error) {
	value, ok := new(big.Rat).SetString(strings.TrimSpace(amount))
	if !ok || value.Sign() < 0 {
		return 0, fmt.Errorf("invalid Shopify money amount %q", amount)
	}
	scale := currencyMinorUnit(strings.ToUpper(strings.TrimSpace(currency)))
	multiplier := int64(1)
	for i := 0; i < scale; i++ {
		multiplier *= 10
	}
	value.Mul(value, big.NewRat(multiplier, 1))
	if !value.IsInt() || !value.Num().IsInt64() {
		return 0, fmt.Errorf("Shopify money %q cannot be represented in minor units", amount)
	}
	return value.Num().Int64(), nil
}

func currencyMinorUnit(currency string) int {
	switch currency {
	case "BIF", "CLP", "DJF", "GNF", "JPY", "KMF", "KRW", "MGA", "PYG",
		"RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "JOD", "KWD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}
