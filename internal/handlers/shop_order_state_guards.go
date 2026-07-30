package handlers

import (
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
)

var shopOrderInactiveDisputeStatuses = []string{
	"", "won", "prevented", "warning_closed",
}

var shopOrderProtectedBusinessStatuses = []string{
	"canceled", "cancelled", "payment_canceled",
	"refund_pending", "partially_refunded", "refunded",
	"payment_disputed", "payment_dispute_lost",
	"reconciliation_required", "refund_reconciliation_required",
}

var shopOrderProtectedFinancialStatuses = []string{
	"refunded", "disputed", "dispute_lost",
}

// shopOrderStatusUnlessProtected allows logistics/return integrations to keep
// their own fields current without hiding an authoritative money, dispute, or
// operator-reconciliation lifecycle from the customer.
func shopOrderStatusUnlessProtected(proposed string) any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
			THEN status
			ELSE ?
		END`,
		shopOrderProtectedBusinessStatuses,
		shopOrderProtectedFinancialStatuses,
		shopOrderInactiveDisputeStatuses,
		proposed,
	)
}

func shopOrderCancellationRequestStatus() any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
				OR UPPER(COALESCE(fulfillment_status, '')) NOT IN ('', 'UNFULFILLED')
				OR COALESCE(tracking_number, '') <> ''
				OR LOWER(COALESCE(fulfillment_request_status, '')) IN ('submitting', 'submitted', 'accepted')
			THEN status
			ELSE 'cancellation_requested'
		END`,
		shopOrderProtectedBusinessStatuses,
		shopOrderProtectedFinancialStatuses,
		shopOrderInactiveDisputeStatuses,
	)
}

func shopOrderLogisticsStatusUnlessProtected(proposed string) any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(status, '')) LIKE 'return_%'
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
			THEN status
			ELSE ?
		END`,
		shopOrderProtectedBusinessStatuses,
		[]string{"delivered", "received"},
		shopOrderProtectedFinancialStatuses,
		shopOrderInactiveDisputeStatuses,
		proposed,
	)
}

func shopOrderFailureUnlessProtected(proposed string) any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(status, '')) LIKE 'return_%'
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
			THEN failure_reason
			ELSE ?
		END`,
		shopOrderProtectedBusinessStatuses,
		shopOrderProtectedFinancialStatuses,
		shopOrderInactiveDisputeStatuses,
		proposed,
	)
}

func shopOrderAllowsCustomerLifecycle(order models.ShopOrder) bool {
	switch strings.ToLower(strings.TrimSpace(order.FinancialStatus)) {
	case "paid", "partially_refunded":
	default:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(order.DisputeStatus)) {
	case "", "won", "prevented", "warning_closed":
	default:
		return false
	}
	status := strings.ToLower(strings.TrimSpace(order.Status))
	for _, blocked := range shopOrderProtectedBusinessStatuses {
		if status == blocked && status != "partially_refunded" {
			return false
		}
	}
	switch strings.ToUpper(strings.TrimSpace(order.FulfillmentStatus)) {
	case "CANCELED", "CANCELLED":
		return false
	}
	return true
}
