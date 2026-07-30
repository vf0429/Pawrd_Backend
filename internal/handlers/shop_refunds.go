package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type shopRefundRequest struct {
	AmountMinor int64  `json:"amountMinor"`
	Reason      string `json:"reason"`
	Confirmed   bool   `json:"confirmed"`
}

type shopRefundDTO struct {
	ID                         string     `json:"id"`
	OrderID                    string     `json:"orderId"`
	StripeRefundID             string     `json:"stripeRefundId,omitempty"`
	AmountMinor                int64      `json:"amountMinor"`
	Currency                   string     `json:"currency"`
	Reason                     string     `json:"reason"`
	Status                     string     `json:"status"`
	StripeStatus               string     `json:"stripeStatus,omitempty"`
	FailureReason              string     `json:"failureReason,omitempty"`
	ShopifyMirrorStatus        string     `json:"shopifyMirrorStatus,omitempty"`
	ShopifyRefundID            string     `json:"shopifyRefundId,omitempty"`
	ShopifyRefundTransactionID string     `json:"shopifyRefundTransactionId,omitempty"`
	ShopifyMirrorError         string     `json:"shopifyMirrorError,omitempty"`
	IdempotencyKey             string     `json:"idempotencyKey"`
	RequestedBy                string     `json:"requestedBy"`
	CompletedAt                *time.Time `json:"completedAt,omitempty"`
	ShopifyMirroredAt          *time.Time `json:"shopifyMirroredAt,omitempty"`
	CreatedAt                  time.Time  `json:"createdAt"`
}

var (
	errShopRefundNotFound       = errors.New("shop order not found")
	errShopRefundNotEligible    = errors.New("shop order is not eligible for refund")
	errShopRefundOverLimit      = errors.New("refund amount exceeds the remaining refundable amount")
	errShopRefundKeyConflict    = errors.New("idempotency key was already used for a different refund")
	errShopRefundStateUncertain = errors.New("refund amount requires reconciliation before another refund")
)

// NewShopOrderRefundHandler creates an explicit full or partial Stripe refund.
// It is deliberately an operator-only endpoint and fails closed when
// SHOP_ADMIN_KEY has not been configured by the caller.
func NewShopOrderRefundHandler(
	db *gorm.DB,
	refunder payments.Refunder,
	adminKey string,
	refundMirrors ...payments.RefundMirrorEnqueuer,
) http.HandlerFunc {
	expectedAdminKey := strings.TrimSpace(adminKey)
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
		if expectedAdminKey == "" {
			http.Error(w, "shop refund service is not configured", http.StatusServiceUnavailable)
			return
		}
		if !constantTimeStringEqual(expectedAdminKey, r.Header.Get("X-Shop-Admin-Key")) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if refunder == nil {
			http.Error(w, "Stripe refund service is unavailable", http.StatusServiceUnavailable)
			return
		}

		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey == "" {
			idempotencyKey = strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
		}
		if idempotencyKey == "" || len(idempotencyKey) > 255 {
			http.Error(w, "a valid Idempotency-Key header is required", http.StatusBadRequest)
			return
		}

		var req shopRefundRequest
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := ensureJSONBodyConsumed(decoder); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		req.Reason = strings.ToLower(strings.TrimSpace(req.Reason))
		if req.AmountMinor <= 0 || !req.Confirmed || !validShopRefundReason(req.Reason) {
			http.Error(w, "positive amountMinor, a valid reason, and confirmed=true are required", http.StatusBadRequest)
			return
		}

		refundRecord, shouldCallStripe, err := reserveShopRefund(
			db,
			strings.TrimSpace(r.PathValue("orderID")),
			idempotencyKey,
			shopRefundActor(r),
			req,
		)
		if err != nil {
			writeShopRefundError(w, err)
			return
		}
		if !shouldCallStripe {
			if err := enqueueSucceededShopifyRefundMirror(r.Context(), refundMirror, refundRecord); err != nil {
				http.Error(w, "refund succeeded but Shopify mirror could not be queued", http.StatusInternalServerError)
				return
			}
			statusCode := http.StatusOK
			if refundRecord.Status == models.ShopRefundStatusPending {
				statusCode = http.StatusAccepted
			}
			writeJSON(w, statusCode, makeShopRefundDTO(*refundRecord))
			return
		}

		if refundRecord.StripeFirstSubmittedAt == nil {
			firstSubmittedAt := time.Now().UTC()
			if err := db.Model(&models.ShopRefund{}).
				Where("id = ? AND stripe_first_submitted_at IS NULL", refundRecord.ID).
				Update("stripe_first_submitted_at", &firstSubmittedAt).Error; err != nil {
				http.Error(w, "failed to persist refund submission audit", http.StatusInternalServerError)
				return
			}
			refundRecord.StripeFirstSubmittedAt = &firstSubmittedAt
		}
		result, stripeErr := refunder.CreateRefund(r.Context(), payments.CreateRefundRequest{
			PaymentIntentID: refundRecord.PaymentIntentID,
			AmountMinor:     refundRecord.AmountMinor,
			Reason:          refundRecord.Reason,
			IdempotencyKey:  refundRecord.IdempotencyKey,
			PawrdRefundID:   refundRecord.ID,
			PawrdOrderID:    refundRecord.OrderID,
		})
		if stripeErr != nil {
			_ = db.Model(&models.ShopRefund{}).
				Where("id = ? AND status = ? AND stripe_refund_id IS NULL",
					refundRecord.ID,
					models.ShopRefundStatusPending,
				).
				Updates(map[string]any{
					// A transport error is ambiguous: Stripe may have accepted the
					// refund before the response was lost. Keep the pending
					// reservation so a different idempotency key cannot refund the
					// same money again. Operators can safely retry the exact same
					// request because the Stripe idempotency key is unchanged.
					"status": models.ShopRefundStatusPending, "stripe_status": "request_unknown",
					"failure_reason": stripeErr.Error(),
				}).Error
			http.Error(w, "Stripe refund request failed", http.StatusBadGateway)
			return
		}
		if err := applyStripeRefundResult(db, refundRecord.ID, result); err != nil {
			http.Error(w, "refund was submitted but local state could not be updated", http.StatusInternalServerError)
			return
		}
		if err := db.First(refundRecord, "id = ?", refundRecord.ID).Error; err != nil {
			http.Error(w, "failed to load refund", http.StatusInternalServerError)
			return
		}
		if err := enqueueSucceededShopifyRefundMirror(r.Context(), refundMirror, refundRecord); err != nil {
			http.Error(w, "refund succeeded but Shopify mirror could not be queued", http.StatusInternalServerError)
			return
		}
		statusCode := http.StatusCreated
		if refundRecord.Status == models.ShopRefundStatusPending {
			statusCode = http.StatusAccepted
		}
		writeJSON(w, statusCode, makeShopRefundDTO(*refundRecord))
	}
}

func constantTimeStringEqual(expected, supplied string) bool {
	if len(expected) == 0 || len(expected) != len(supplied) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func ensureJSONBodyConsumed(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func validShopRefundReason(reason string) bool {
	switch reason {
	case "requested_by_customer", "duplicate", "fraudulent":
		return true
	default:
		return false
	}
}

func shopRefundActor(r *http.Request) string {
	actor := strings.TrimSpace(r.Header.Get("X-Shop-Admin-Actor"))
	if actor == "" {
		return "shop-admin"
	}
	if len(actor) > 64 {
		return actor[:64]
	}
	return actor
}

func reserveShopRefund(
	db *gorm.DB,
	orderID string,
	idempotencyKey string,
	actor string,
	req shopRefundRequest,
) (*models.ShopRefund, bool, error) {
	if orderID == "" {
		return nil, false, errShopRefundNotFound
	}
	var result models.ShopRefund
	shouldCallStripe := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var existing models.ShopRefund
		err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error
		if err == nil {
			if existing.OrderID != orderID || existing.AmountMinor != req.AmountMinor || existing.Reason != req.Reason {
				return errShopRefundKeyConflict
			}
			result = existing
			// A failed request, or a pending record created before Stripe returned
			// an ID, is safe to retry with the exact same Stripe idempotency key.
			retryable := (existing.Status == models.ShopRefundStatusFailed &&
				existing.StripeStatus == "request_failed") ||
				(existing.Status == models.ShopRefundStatusPending &&
					existing.StripeRefundID == nil)
			if retryable {
				if shopRefundRetryWindowExpired(existing, time.Now().UTC()) {
					return errShopRefundStateUncertain
				}
				if existing.Status == models.ShopRefundStatusFailed {
					var order models.ShopOrder
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
						First(&order, "id = ?", existing.OrderID).Error; err != nil {
						return err
					}
					if !shopRefundEligible(order) {
						return errShopRefundNotEligible
					}
					var reserved struct {
						Amount int64
					}
					if err := tx.Model(&models.ShopRefund{}).
						Select("COALESCE(SUM(amount_minor), 0) AS amount").
						Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusPending).
						Scan(&reserved).Error; err != nil {
						return err
					}
					if existing.AmountMinor > order.TotalAmountMinor-order.RefundedAmountMinor-reserved.Amount {
						return errShopRefundOverLimit
					}
				}
				if err := tx.Model(&existing).Updates(map[string]any{
					"status": models.ShopRefundStatusPending, "stripe_status": "",
					"failure_reason": "",
				}).Error; err != nil {
					return err
				}
				result.Status = models.ShopRefundStatusPending
				result.StripeStatus = ""
				result.FailureReason = ""
				shouldCallStripe = true
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var order models.ShopOrder
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, "id = ?", orderID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errShopRefundNotFound
		}
		if err != nil {
			return err
		}
		if order.PaymentIntentIDValue() == "" || !shopRefundEligible(order) {
			return errShopRefundNotEligible
		}
		if strings.EqualFold(order.FinancialStatus, "refunded") {
			return errShopRefundOverLimit
		}
		if strings.EqualFold(order.FinancialStatus, "partially_refunded") && order.RefundedAmountMinor <= 0 {
			return errShopRefundStateUncertain
		}

		var reserved struct {
			Amount int64
		}
		if err := tx.Model(&models.ShopRefund{}).
			Select("COALESCE(SUM(amount_minor), 0) AS amount").
			Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusPending).
			Scan(&reserved).Error; err != nil {
			return err
		}
		remaining := order.TotalAmountMinor - order.RefundedAmountMinor - reserved.Amount
		if req.AmountMinor > remaining {
			return errShopRefundOverLimit
		}

		result = models.ShopRefund{
			ID: uuid.NewString(), OrderID: order.ID, PaymentIntentID: order.PaymentIntentIDValue(),
			IdempotencyKey: idempotencyKey, AmountMinor: req.AmountMinor,
			Currency: strings.ToUpper(order.Currency), Reason: req.Reason,
			Status: models.ShopRefundStatusPending, RequestedBy: actor,
		}
		if err := tx.Create(&result).Error; err != nil {
			return err
		}
		shouldCallStripe = true
		return nil
	})
	return &result, shouldCallStripe, err
}

func shopRefundRetryWindowExpired(refund models.ShopRefund, now time.Time) bool {
	firstSubmittedAt := refund.StripeFirstSubmittedAt
	if firstSubmittedAt == nil || firstSubmittedAt.IsZero() {
		if refund.CreatedAt.IsZero() {
			return true
		}
		firstSubmittedAt = &refund.CreatedAt
	}
	return !now.Before(firstSubmittedAt.UTC().Add(payments.StripeRefundIdempotencyRetryWindow))
}

func shopReturnApproved(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "OPEN", "APPROVED", "CLOSED":
		return true
	default:
		return false
	}
}

func shopRefundEligible(order models.ShopOrder) bool {
	if strings.EqualFold(strings.TrimSpace(order.FulfillmentRequestStatus), "submitting") {
		return false
	}
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
	return shopReturnApproved(order.ReturnStatus) ||
		strings.EqualFold(strings.TrimSpace(order.Status), "canceled") ||
		strings.EqualFold(
			strings.TrimSpace(order.Status),
			"refund_reconciliation_required",
		)
}

func applyStripeRefundResult(db *gorm.DB, refundID string, result *payments.CreateRefundResponse) error {
	if result == nil || strings.TrimSpace(result.RefundID) == "" {
		return errors.New("Stripe refund response is missing an ID")
	}
	stripeRefundID := strings.TrimSpace(result.RefundID)
	stripeStatus := strings.ToLower(strings.TrimSpace(result.Status))
	status := models.ShopRefundStatusPending
	var completedAt *time.Time
	switch stripeStatus {
	case "succeeded":
		status = models.ShopRefundStatusSucceeded
		now := time.Now().UTC()
		completedAt = &now
	case "failed", "canceled":
		status = models.ShopRefundStatusFailed
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var refundRecord models.ShopRefund
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&refundRecord, "id = ?", refundID).Error; err != nil {
			return err
		}
		if refundRecord.StripeRefundID != nil &&
			strings.TrimSpace(*refundRecord.StripeRefundID) != "" &&
			strings.TrimSpace(*refundRecord.StripeRefundID) != stripeRefundID {
			return errors.New("Stripe refund response ID does not match the durable reservation")
		}
		if shouldIgnoreStripeRefundTransition(refundRecord, status, 0) {
			// A webhook can commit a terminal state before the synchronous API
			// response is persisted. Never let an older pending/failed response
			// regress that authoritative terminal state.
			return nil
		}
		updates := map[string]any{
			"stripe_refund_id": &stripeRefundID, "stripe_status": stripeStatus,
			"status": status, "failure_reason": strings.TrimSpace(result.FailureReason),
			"completed_at": completedAt,
		}
		if status == models.ShopRefundStatusSucceeded &&
			refundRecord.ShopifyMirrorStatus != models.ShopRefundMirrorStatusSucceeded {
			updates["shopify_mirror_status"] = models.ShopRefundMirrorStatusPending
			updates["shopify_mirror_error"] = ""
		}
		if err := tx.Model(&refundRecord).Updates(updates).Error; err != nil {
			return err
		}
		return recalculateShopOrderRefundState(tx, refundRecord.OrderID, nil)
	})
}

// observedAmount is Stripe's cumulative amount_refunded from charge.refunded.
// When present it is the authoritative total; otherwise succeeded refund rows
// are summed.
func recalculateShopOrderRefundState(tx *gorm.DB, orderID string, observedAmount *int64) error {
	var order models.ShopOrder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, "id = ?", orderID).Error; err != nil {
		return err
	}
	refundedAmount := int64(0)
	if observedAmount != nil {
		refundedAmount = *observedAmount
		// Stripe's cumulative amount_refunded is monotonic, but webhook
		// delivery is not ordered. Never let an older partial charge.refunded
		// event reduce a newer known full refund.
		if order.RefundedAmountMinor > refundedAmount {
			refundedAmount = order.RefundedAmountMinor
		}
	} else {
		var aggregate struct {
			Amount int64
		}
		if err := tx.Model(&models.ShopRefund{}).
			Select("COALESCE(SUM(amount_minor), 0) AS amount").
			Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusSucceeded).
			Scan(&aggregate).Error; err != nil {
			return err
		}
		refundedAmount = aggregate.Amount
		// A charge.refunded event may have established an authoritative
		// cumulative amount even when Stripe did not expand every historical
		// Refund object. Do not lose that known amount on a later single-refund
		// update.
		if order.RefundedAmountMinor > refundedAmount {
			refundedAmount = order.RefundedAmountMinor
		}
	}
	if refundedAmount < 0 {
		refundedAmount = 0
	}
	if refundedAmount > order.TotalAmountMinor {
		refundedAmount = order.TotalAmountMinor
	}

	updates := map[string]any{"refunded_amount_minor": refundedAmount}
	switch {
	case refundedAmount >= order.TotalAmountMinor && order.TotalAmountMinor > 0:
		updates["financial_status"] = "refunded"
		updates["status"] = "refunded"
	case refundedAmount > 0:
		updates["financial_status"] = "partially_refunded"
		// A partial money refund must not erase the return/fulfillment state.
	default:
		if strings.EqualFold(order.FinancialStatus, "refunded") ||
			strings.EqualFold(order.FinancialStatus, "partially_refunded") {
			updates["financial_status"] = "paid"
		}
	}
	return tx.Model(&order).Updates(updates).Error
}

func writeShopRefundError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errShopRefundNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errShopRefundNotEligible),
		errors.Is(err, errShopRefundOverLimit),
		errors.Is(err, errShopRefundKeyConflict),
		errors.Is(err, errShopRefundStateUncertain):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "failed to reserve refund", http.StatusInternalServerError)
	}
}

func makeShopRefundDTO(refund models.ShopRefund) shopRefundDTO {
	stripeRefundID := ""
	if refund.StripeRefundID != nil {
		stripeRefundID = *refund.StripeRefundID
	}
	shopifyRefundID := ""
	if refund.ShopifyRefundID != nil {
		shopifyRefundID = *refund.ShopifyRefundID
	}
	return shopRefundDTO{
		ID: refund.ID, OrderID: refund.OrderID, StripeRefundID: stripeRefundID,
		AmountMinor: refund.AmountMinor, Currency: refund.Currency, Reason: refund.Reason,
		Status: refund.Status, StripeStatus: refund.StripeStatus, FailureReason: refund.FailureReason,
		ShopifyMirrorStatus:        refund.ShopifyMirrorStatus,
		ShopifyRefundID:            shopifyRefundID,
		ShopifyRefundTransactionID: refund.ShopifyRefundTransactionID,
		ShopifyMirrorError:         refund.ShopifyMirrorError,
		IdempotencyKey:             refund.IdempotencyKey, RequestedBy: refund.RequestedBy,
		CompletedAt: refund.CompletedAt, ShopifyMirroredAt: refund.ShopifyMirroredAt,
		CreatedAt: refund.CreatedAt,
	}
}

func enqueueSucceededShopifyRefundMirror(
	ctx context.Context,
	enqueuer payments.RefundMirrorEnqueuer,
	refund *models.ShopRefund,
) error {
	if enqueuer == nil || refund == nil || refund.Status != models.ShopRefundStatusSucceeded {
		return nil
	}
	return enqueuer.EnqueueRefundMirror(ctx, refund.ID)
}
