package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrFulfillmentRequestIneligible = errors.New("shop order is not eligible for a fulfillment request")

// AutoRequestingFulfiller optionally follows Shopify order creation by
// submitting its requestable fulfillment orders to the assigned service.
// Production keeps this off by default; enabling it is an explicit operational
// decision through SHOPIFY_AUTO_REQUEST_FULFILLMENT=true.
type AutoRequestingFulfiller struct {
	downstream Fulfiller
	db         *gorm.DB
	requester  shopify.AdminFulfillmentRequester
	enabled    bool
}

func NewAutoRequestingFulfiller(
	downstream Fulfiller,
	db *gorm.DB,
	requester shopify.AdminFulfillmentRequester,
	enabled bool,
) *AutoRequestingFulfiller {
	return &AutoRequestingFulfiller{
		downstream: downstream, db: db, requester: requester, enabled: enabled,
	}
}

func (f *AutoRequestingFulfiller) Fulfill(req FulfillmentRequest) error {
	if f == nil || f.downstream == nil {
		return errors.New("order fulfiller is not configured")
	}
	if err := f.downstream.Fulfill(req); err != nil {
		return err
	}
	if !f.enabled {
		return nil
	}
	if f.db == nil || f.requester == nil {
		return errors.New("automatic Shopify fulfillment requests are enabled but not configured")
	}
	var order models.ShopOrder
	if err := f.db.Select("id").
		Where("payment_intent_id = ?", req.PaymentIntentID).First(&order).Error; err != nil {
		return fmt.Errorf("load Shopify order for fulfillment request: %w", err)
	}
	result, err := RequestOrderFulfillmentIfEligible(
		context.Background(),
		f.db,
		f.requester,
		order.ID,
	)
	if err != nil {
		return err
	}
	_ = result
	return nil
}

func (f *AutoRequestingFulfiller) ReconcileShopifyOrder(
	ctx context.Context,
	paymentIntentID string,
) (bool, error) {
	if f == nil || f.downstream == nil {
		return false, errors.New("order fulfiller is not configured")
	}
	reconciler, ok := f.downstream.(ShopifyOrderReconciler)
	if !ok {
		return false, errors.New("order fulfiller does not support Shopify reconciliation")
	}
	return reconciler.ReconcileShopifyOrder(ctx, paymentIntentID)
}

// RequestOrderFulfillmentIfEligible is the common operator/automatic gate for
// third-party fulfillment. It serializes the transition against pending
// refunds and records the request state before calling Shopify, so a canceled,
// refunded, disputed or returning order cannot be submitted accidentally.
func RequestOrderFulfillmentIfEligible(
	ctx context.Context,
	db *gorm.DB,
	requester shopify.AdminFulfillmentRequester,
	orderID string,
) (*shopify.AdminFulfillmentRequestResult, error) {
	if db == nil || requester == nil {
		return nil, errors.New("Shopify fulfillment operations are unavailable")
	}
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, fmt.Errorf("%w: order ID is required", ErrFulfillmentRequestIneligible)
	}

	var order models.ShopOrder
	now := time.Now().UTC()
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&order, "id = ?", orderID).Error; err != nil {
			return err
		}
		if err := validateOrderForFulfillmentRequest(order); err != nil {
			return err
		}
		var pendingRefunds int64
		if err := tx.Model(&models.ShopRefund{}).
			Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusPending).
			Count(&pendingRefunds).Error; err != nil {
			return err
		}
		if pendingRefunds > 0 {
			return fmt.Errorf("%w: a refund is pending", ErrFulfillmentRequestIneligible)
		}
		if strings.EqualFold(order.FulfillmentRequestStatus, "submitting") &&
			now.Sub(order.UpdatedAt) < 5*time.Minute {
			return fmt.Errorf("%w: a fulfillment request is already being submitted", ErrFulfillmentRequestIneligible)
		}
		return tx.Model(&order).Updates(map[string]any{
			"fulfillment_request_status": "submitting",
			"fulfillment_request_error":  "",
		}).Error
	}); err != nil {
		return nil, err
	}

	result, err := requester.RequestOrderFulfillment(ctx, order.ShopifyOrderGID())
	if err != nil {
		status := "request_unknown"
		if errors.Is(err, shopify.ErrFulfillmentRequestBlocked) {
			status = "blocked"
		}
		if fulfillmentResultHasExternalOutcome(result) {
			status = "reconciliation_required"
		}
		_ = db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
			"fulfillment_request_status": status,
			"fulfillment_request_error":  fulfillmentRequestAuditError(err),
		}).Error
		if errors.Is(err, shopify.ErrFulfillmentRequestBlocked) {
			return result, fmt.Errorf("%w: %v", ErrFulfillmentRequestIneligible, err)
		}
		return result, err
	}
	if result == nil {
		err := errors.New("Shopify has not exposed a successful requestable fulfillment order")
		_ = db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
			"fulfillment_request_status": "blocked",
			"fulfillment_request_error":  fulfillmentRequestAuditError(err),
		}).Error
		return nil, err
	}
	if reason := blockingFulfillmentResultReason(result); reason != "" {
		err := fmt.Errorf("Shopify fulfillment result requires reconciliation: %s", reason)
		status := "blocked"
		if fulfillmentResultHasExternalOutcome(result) {
			status = "reconciliation_required"
		}
		_ = db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
			"fulfillment_request_status": status,
			"fulfillment_request_error":  fulfillmentRequestAuditError(err),
		}).Error
		return result, fmt.Errorf("%w: %v", ErrFulfillmentRequestIneligible, err)
	}
	if !fulfillmentResultHasExternalOutcome(result) && !fulfillmentResultNeedsNoRequest(result) {
		err := errors.New("Shopify has not exposed a successful requestable fulfillment order")
		_ = db.Model(&models.ShopOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
			"fulfillment_request_status": "blocked",
			"fulfillment_request_error":  fulfillmentRequestAuditError(err),
		}).Error
		return result, err
	}

	// Re-read after the external call. A simultaneous cancellation/dispute
	// cannot unsend a request, but it is surfaced as reconciliation_required
	// instead of being reported as a clean completion.
	var latest models.ShopOrder
	if err := db.First(&latest, "id = ?", order.ID).Error; err != nil {
		return nil, err
	}
	if err := validateOrderForFulfillmentRequest(latest); err != nil {
		_ = db.Model(&latest).Updates(map[string]any{
			"fulfillment_request_status": "reconciliation_required",
			"fulfillment_request_error":  err.Error(),
		}).Error
		return nil, err
	}

	requestStatus := "not_required"
	if fulfillmentResultHasExternalOutcome(result) {
		requestStatus = "submitted"
	}
	for _, item := range append(result.Requested, result.AlreadyRequested...) {
		if strings.EqualFold(item.RequestStatus, "ACCEPTED") {
			requestStatus = "accepted"
			break
		}
	}
	updates := map[string]any{
		"fulfillment_request_status": requestStatus,
		"fulfillment_request_error":  "",
		"fulfillment_requested_at":   nil,
	}
	if fulfillmentResultHasExternalOutcome(result) {
		requestedAt := time.Now().UTC()
		updates["fulfillment_requested_at"] = &requestedAt
	}
	if err := db.Model(&latest).Updates(updates).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func fulfillmentResultHasExternalOutcome(result *shopify.AdminFulfillmentRequestResult) bool {
	return result != nil && len(result.Requested)+len(result.AlreadyRequested) > 0
}

func fulfillmentResultNeedsNoRequest(result *shopify.AdminFulfillmentRequestResult) bool {
	if result == nil || len(result.Skipped) == 0 {
		return false
	}
	for _, item := range result.Skipped {
		if !item.TerminalNoRequest {
			return false
		}
	}
	return true
}

func blockingFulfillmentResultReason(result *shopify.AdminFulfillmentRequestResult) string {
	if result == nil {
		return ""
	}
	reasons := make([]string, 0)
	for _, item := range result.Skipped {
		if item.TerminalNoRequest {
			continue
		}
		orderID := strings.TrimSpace(item.FulfillmentOrderID)
		if orderID == "" {
			orderID = "unknown fulfillment order"
		}
		reason := strings.TrimSpace(item.SkipReason)
		if reason == "" {
			reason = fmt.Sprintf(
				"status=%s requestStatus=%s was skipped without a terminal reason",
				strings.TrimSpace(item.Status),
				strings.TrimSpace(item.RequestStatus),
			)
		}
		reasons = append(reasons, orderID+": "+reason)
	}
	return strings.Join(reasons, "; ")
}

func fulfillmentRequestAuditError(err error) string {
	if err == nil {
		return ""
	}
	const maxCharacters = 1000
	characters := []rune(strings.TrimSpace(err.Error()))
	if len(characters) > maxCharacters {
		characters = characters[:maxCharacters]
	}
	return string(characters)
}

func validateOrderForFulfillmentRequest(order models.ShopOrder) error {
	if order.ShopifyOrderGID() == "" {
		return fmt.Errorf("%w: Shopify order has not been created", ErrFulfillmentRequestIneligible)
	}
	if !strings.EqualFold(strings.TrimSpace(order.FinancialStatus), "paid") ||
		order.RefundedAmountMinor > 0 {
		return fmt.Errorf("%w: order is not fully paid and unrefunded", ErrFulfillmentRequestIneligible)
	}
	switch strings.ToLower(strings.TrimSpace(order.DisputeStatus)) {
	case "", "won", "prevented", "warning_closed":
	default:
		return fmt.Errorf("%w: order has an active or lost dispute", ErrFulfillmentRequestIneligible)
	}
	if strings.TrimSpace(order.ReturnStatus) != "" {
		return fmt.Errorf("%w: order has a return lifecycle", ErrFulfillmentRequestIneligible)
	}
	status := strings.ToLower(strings.TrimSpace(order.Status))
	if status == "canceled" || status == "refunded" ||
		status == "cancellation_requested" ||
		strings.Contains(status, "dispute") ||
		strings.HasPrefix(status, "return_") ||
		status == "reconciliation_required" {
		return fmt.Errorf("%w: order lifecycle blocks fulfillment", ErrFulfillmentRequestIneligible)
	}
	if strings.EqualFold(strings.TrimSpace(order.FulfillmentStatus), "CANCELLED") {
		return fmt.Errorf("%w: Shopify fulfillment was canceled", ErrFulfillmentRequestIneligible)
	}
	return nil
}
