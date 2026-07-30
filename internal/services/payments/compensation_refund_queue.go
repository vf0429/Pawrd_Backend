package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultCompensationRefundLease       = 2 * time.Minute
	defaultCompensationRefundPoll        = 2 * time.Second
	defaultCompensationRefundReconcile   = 5 * time.Minute
	defaultCompensationRefundBatch       = 10
	defaultCompensationRefundMaxAttempts = 8
)

var (
	errCompensationRefundPaused                 = errors.New("compensation refund is paused")
	errCompensationRefundNotRequired            = errors.New("compensation refund is no longer required")
	errCompensationRefundShopifyOrderExists     = errors.New("compensation refund is unsafe because a Shopify order exists")
	errCompensationRefundReconciliationRequired = errors.New("compensation refund requires reconciliation")
	errCompensationRefundLeaseLost              = errors.New("compensation refund lease was lost before the Stripe call")
)

// DurableCompensationRefundQueue executes system-created Stripe refunds. The
// reservation and job are committed in the same database transaction before
// this worker is involved, so a webhook never waits for Stripe and there is no
// enqueue crash window.
type DurableCompensationRefundQueue struct {
	db                *gorm.DB
	refunder          Refunder
	refundMirror      RefundMirrorEnqueuer
	now               func() time.Time
	wake              chan struct{}
	lease             time.Duration
	pollInterval      time.Duration
	reconcileInterval time.Duration
	maxAttempts       int
}

func NewDurableCompensationRefundQueue(
	db *gorm.DB,
	refunder Refunder,
	refundMirror RefundMirrorEnqueuer,
) *DurableCompensationRefundQueue {
	return &DurableCompensationRefundQueue{
		db: db, refunder: refunder, refundMirror: refundMirror,
		now: time.Now, wake: make(chan struct{}, 1),
		lease:             defaultCompensationRefundLease,
		pollInterval:      defaultCompensationRefundPoll,
		reconcileInterval: defaultCompensationRefundReconcile,
		maxAttempts:       defaultCompensationRefundMaxAttempts,
	}
}

// EnsureSystemCompensationRefund reserves the remaining paid amount and
// creates its execution job atomically. The caller must pass a transaction
// that already owns an UPDATE lock on order.
func EnsureSystemCompensationRefund(
	tx *gorm.DB,
	order *models.ShopOrder,
	reason string,
	now time.Time,
) (*models.ShopRefund, error) {
	if tx == nil || order == nil {
		return nil, errors.New("system compensation requires a database transaction and order")
	}
	reason = strings.ToLower(strings.TrimSpace(reason))
	switch reason {
	case models.ShopRefundReasonQuoteExpired,
		models.ShopRefundReasonFulfillmentFailed:
	default:
		return nil, fmt.Errorf("unsupported system compensation reason %q", reason)
	}
	if strings.TrimSpace(order.ID) == "" ||
		strings.TrimSpace(order.PaymentIntentIDValue()) == "" ||
		order.TotalAmountMinor <= 0 ||
		len(strings.TrimSpace(order.Currency)) != 3 {
		return nil, errors.New("system compensation order has invalid payment data")
	}

	idempotencyKey := systemCompensationIdempotencyKey(reason, order.PaymentIntentIDValue())
	var existing models.ShopRefund
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("idempotency_key = ?", idempotencyKey).
		First(&existing).Error
	switch {
	case err == nil:
		if existing.OrderID != order.ID ||
			existing.PaymentIntentID != order.PaymentIntentIDValue() ||
			existing.Reason != reason ||
			existing.AmountMinor <= 0 {
			return nil, errors.New("system compensation idempotency record does not match the order")
		}
		if existing.Status == models.ShopRefundStatusPending &&
			(existing.StripeRefundID == nil || strings.TrimSpace(*existing.StripeRefundID) == "") {
			if err := ensureCompensationRefundJob(tx, existing.ID, now); err != nil {
				return nil, err
			}
		}
		return &existing, nil
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}

	if order.RefundedAmountMinor >= order.TotalAmountMinor {
		return nil, nil
	}
	var pending struct {
		Amount int64
	}
	if err := tx.Model(&models.ShopRefund{}).
		Select("COALESCE(SUM(amount_minor), 0) AS amount").
		Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusPending).
		Scan(&pending).Error; err != nil {
		return nil, err
	}
	amount := order.TotalAmountMinor - order.RefundedAmountMinor - pending.Amount
	if amount <= 0 {
		// Another durable refund already reserves every remaining minor unit.
		return nil, nil
	}

	refund := models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID,
		PaymentIntentID: order.PaymentIntentIDValue(),
		IdempotencyKey:  idempotencyKey,
		AmountMinor:     amount,
		Currency:        strings.ToUpper(strings.TrimSpace(order.Currency)),
		Reason:          reason,
		Status:          models.ShopRefundStatusPending,
		RequestedBy:     "system:" + reason,
	}
	if err := tx.Create(&refund).Error; err != nil {
		return nil, err
	}
	if err := ensureCompensationRefundJob(tx, refund.ID, now); err != nil {
		return nil, err
	}
	return &refund, nil
}

func systemCompensationIdempotencyKey(reason, paymentIntentID string) string {
	sum := sha256.Sum256([]byte(
		strings.ToLower(strings.TrimSpace(reason)) + "\x00" +
			strings.TrimSpace(paymentIntentID),
	))
	return "pawrd-system-refund:" + hex.EncodeToString(sum[:])
}

func ensureCompensationRefundJob(tx *gorm.DB, refundID string, now time.Time) error {
	var job models.ShopCompensationRefundJob
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("refund_id = ?", refundID).First(&job).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return tx.Create(&models.ShopCompensationRefundJob{
			ID: uuid.NewString(), RefundID: refundID,
			Status:        models.ShopCompensationRefundJobPending,
			NextAttemptAt: now.UTC(),
		}).Error
	case err != nil:
		return err
	case job.Status == models.ShopCompensationRefundJobProcessing &&
		job.LockedUntil != nil && job.LockedUntil.After(now):
		return nil
	case job.Status == models.ShopCompensationRefundJobPending,
		job.Status == models.ShopCompensationRefundJobRetrying,
		job.Status == models.ShopCompensationRefundJobFailed:
		// Keep the worker's backoff and terminal reconciliation state. In
		// particular, don't retry an ambiguous Stripe request forever after
		// Stripe's idempotency retention window.
		return nil
	default:
		return tx.Model(&job).Updates(map[string]any{
			"status":          models.ShopCompensationRefundJobPending,
			"next_attempt_at": now.UTC(), "locked_until": nil,
			"lease_owner": "", "last_error": "", "completed_at": nil,
		}).Error
	}
}

func (q *DurableCompensationRefundQueue) Run(ctx context.Context) {
	if q == nil || q.db == nil || q.refunder == nil {
		log.Printf("[compensation-refund-queue] disabled: database or Stripe refunder is missing")
		return
	}
	pollTicker := time.NewTicker(q.pollInterval)
	reconcileTicker := time.NewTicker(q.reconcileInterval)
	defer pollTicker.Stop()
	defer reconcileTicker.Stop()

	q.processAndLog(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
			q.processAndLog(ctx)
		case <-pollTicker.C:
			q.processAndLog(ctx)
		case <-reconcileTicker.C:
			if _, err := q.ReconcilePendingCompensations(ctx, defaultCompensationRefundBatch); err != nil {
				log.Printf("[compensation-refund-queue] reconciliation failed: %v", err)
			}
			q.processAndLog(ctx)
		}
	}
}

func (q *DurableCompensationRefundQueue) ProcessPending(ctx context.Context, limit int) (int, error) {
	if q == nil || q.db == nil || q.refunder == nil {
		return 0, errors.New("compensation refund queue is not fully configured")
	}
	if limit <= 0 {
		limit = defaultCompensationRefundBatch
	}
	processed := 0
	for processed < limit {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		job, err := q.claimNext(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return processed, nil
		}
		if err != nil {
			return processed, err
		}
		processed++
		q.processClaimed(ctx, job)
	}
	return processed, nil
}

func (q *DurableCompensationRefundQueue) claimNext(
	ctx context.Context,
) (*models.ShopCompensationRefundJob, error) {
	now := q.now().UTC()
	var candidates []models.ShopCompensationRefundJob
	if err := q.db.WithContext(ctx).
		Where("next_attempt_at <= ? AND ((status IN ?) OR (status = ? AND (locked_until IS NULL OR locked_until <= ?)))",
			now,
			[]string{
				models.ShopCompensationRefundJobPending,
				models.ShopCompensationRefundJobRetrying,
			},
			models.ShopCompensationRefundJobProcessing,
			now,
		).
		Order("next_attempt_at ASC, created_at ASC").Limit(5).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		leaseOwner := uuid.NewString()
		lockedUntil := now.Add(q.lease)
		result := q.db.WithContext(ctx).Model(&models.ShopCompensationRefundJob{}).
			Where("id = ? AND next_attempt_at <= ? AND ((status IN ?) OR (status = ? AND (locked_until IS NULL OR locked_until <= ?)))",
				candidate.ID,
				now,
				[]string{
					models.ShopCompensationRefundJobPending,
					models.ShopCompensationRefundJobRetrying,
				},
				models.ShopCompensationRefundJobProcessing,
				now,
			).
			Updates(map[string]any{
				"status":       models.ShopCompensationRefundJobProcessing,
				"locked_until": lockedUntil,
				"lease_owner":  leaseOwner,
				"attempts":     gorm.Expr("attempts + 1"),
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			continue
		}
		var claimed models.ShopCompensationRefundJob
		if err := q.db.WithContext(ctx).First(&claimed, "id = ?", candidate.ID).Error; err != nil {
			return nil, err
		}
		if claimed.LeaseOwner == leaseOwner {
			return &claimed, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (q *DurableCompensationRefundQueue) processClaimed(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
) {
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go q.renewLease(job.ID, job.LeaseOwner, stopHeartbeat, heartbeatDone)
	result, err := q.executeRefund(ctx, job)
	close(stopHeartbeat)
	<-heartbeatDone

	now := q.now().UTC()
	if errors.Is(err, errCompensationRefundLeaseLost) {
		return
	}
	if errors.Is(err, errCompensationRefundPaused) {
		q.persistPaused(ctx, job, err, now)
		return
	}
	if errors.Is(err, errCompensationRefundNotRequired) ||
		errors.Is(err, errCompensationRefundShopifyOrderExists) {
		if completeErr := q.persistNotRequired(ctx, job, err, now); completeErr != nil {
			q.persistFailure(ctx, job, completeErr, now)
		}
		return
	}
	if errors.Is(err, errCompensationRefundReconciliationRequired) {
		q.persistReconciliationRequired(ctx, job, err, now)
		return
	}
	if err == nil {
		err = q.persistResult(ctx, job, result, now)
	}
	if err != nil {
		q.persistFailure(ctx, job, err, now)
		return
	}

	if result != nil && strings.EqualFold(strings.TrimSpace(result.Status), "succeeded") &&
		q.refundMirror != nil {
		if mirrorErr := q.refundMirror.EnqueueRefundMirror(context.Background(), job.RefundID); mirrorErr != nil {
			// The independent refund-mirror reconciler will recover this narrow
			// post-Stripe crash/error window.
			log.Printf(
				"[compensation-refund-queue] enqueue Shopify mirror refund=%s: %v",
				job.RefundID,
				mirrorErr,
			)
		}
	}
}

func (q *DurableCompensationRefundQueue) executeRefund(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
) (*CreateRefundResponse, error) {
	var refund models.ShopRefund
	executionNow := q.now().UTC()
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var currentJob models.ShopCompensationRefundJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "lease_owner", "attempts").
			First(&currentJob, "id = ?", job.ID).Error; err != nil {
			return err
		}
		if currentJob.Status != models.ShopCompensationRefundJobProcessing ||
			currentJob.LeaseOwner != job.LeaseOwner {
			return errCompensationRefundLeaseLost
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&refund, "id = ?", job.RefundID).Error; err != nil {
			return err
		}
		if !isSystemCompensationReason(refund.Reason) {
			return fmt.Errorf("refund %s is not a system compensation", refund.ID)
		}
		switch refund.Status {
		case models.ShopRefundStatusSucceeded, models.ShopRefundStatusFailed:
			return nil
		}
		if refund.StripeRefundID != nil && strings.TrimSpace(*refund.StripeRefundID) != "" {
			return nil
		}

		var order models.ShopOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&order, "id = ?", refund.OrderID).Error; err != nil {
			return err
		}
		if strings.TrimSpace(order.PaymentIntentIDValue()) != strings.TrimSpace(refund.PaymentIntentID) {
			return errors.New("compensation refund payment does not match its order")
		}
		if order.ShopifyOrderGID() != "" {
			if currentJob.Attempts > 1 {
				return fmt.Errorf(
					"%w: Shopify order mapping appeared after a prior Stripe attempt",
					errCompensationRefundReconciliationRequired,
				)
			}
			return fmt.Errorf(
				"%w: order %s is mapped to %s",
				errCompensationRefundShopifyOrderExists,
				order.ID,
				order.ShopifyOrderGID(),
			)
		}

		refundedAmount, err := compensatedOrderRefundedAmount(tx, order)
		if err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(order.FinancialStatus), "refunded") ||
			(order.TotalAmountMinor > 0 && refundedAmount >= order.TotalAmountMinor) {
			return fmt.Errorf(
				"%w: order has no remaining refundable balance",
				errCompensationRefundNotRequired,
			)
		}

		switch strings.ToLower(strings.TrimSpace(order.DisputeStatus)) {
		case "", "won", "prevented", "warning_closed":
		default:
			return fmt.Errorf(
				"%w: order has an active or lost payment dispute",
				errCompensationRefundPaused,
			)
		}
		switch strings.ToLower(strings.TrimSpace(order.FinancialStatus)) {
		case "paid", "partially_refunded":
		default:
			return fmt.Errorf(
				"%w: order financial state %q does not safely permit a refund",
				errCompensationRefundPaused,
				order.FinancialStatus,
			)
		}

		remaining := order.TotalAmountMinor - refundedAmount
		if order.TotalAmountMinor <= 0 || remaining <= 0 {
			return fmt.Errorf(
				"%w: order has no remaining refundable balance",
				errCompensationRefundNotRequired,
			)
		}
		if refund.AmountMinor <= 0 {
			return fmt.Errorf(
				"%w: reserved refund amount %d is invalid",
				errCompensationRefundReconciliationRequired,
				refund.AmountMinor,
			)
		}
		if refund.AmountMinor > remaining {
			if currentJob.Attempts > 1 {
				return fmt.Errorf(
					"%w: reserved refund amount %d exceeds latest refundable balance %d after a prior Stripe attempt",
					errCompensationRefundReconciliationRequired,
					refund.AmountMinor,
					remaining,
				)
			}
			// No Stripe call has occurred yet: atomically release the part of
			// the reservation already refunded elsewhere, then submit exactly
			// the newly remaining balance.
			if err := tx.Model(&refund).Updates(map[string]any{
				"amount_minor":   remaining,
				"failure_reason": "",
			}).Error; err != nil {
				return err
			}
			refund.AmountMinor = remaining
		}
		firstSubmittedAt := refund.StripeFirstSubmittedAt
		if currentJob.Attempts > 1 {
			if firstSubmittedAt == nil || firstSubmittedAt.IsZero() {
				firstSubmittedAt = &refund.CreatedAt
			}
			if firstSubmittedAt.IsZero() ||
				!executionNow.Before(
					firstSubmittedAt.UTC().Add(StripeRefundIdempotencyRetryWindow),
				) {
				return fmt.Errorf(
					"%w: Stripe idempotency retry window expired after a prior attempt",
					errCompensationRefundReconciliationRequired,
				)
			}
		}
		if refund.StripeFirstSubmittedAt == nil {
			if firstSubmittedAt == nil || firstSubmittedAt.IsZero() {
				firstSubmittedAt = &executionNow
			}
			if err := tx.Model(&refund).
				Update("stripe_first_submitted_at", firstSubmittedAt).Error; err != nil {
				return err
			}
			refund.StripeFirstSubmittedAt = firstSubmittedAt
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch refund.Status {
	case models.ShopRefundStatusSucceeded, models.ShopRefundStatusFailed:
		return &CreateRefundResponse{
			RefundID: stringValue(refund.StripeRefundID),
			Status:   refund.StripeStatus,
		}, nil
	}
	if refund.StripeRefundID != nil && strings.TrimSpace(*refund.StripeRefundID) != "" {
		// Stripe has accepted the request and its webhook owns the remaining
		// asynchronous lifecycle. Never create a second refund.
		return &CreateRefundResponse{
			RefundID: strings.TrimSpace(*refund.StripeRefundID),
			Status:   refund.StripeStatus,
		}, nil
	}
	return q.refunder.CreateRefund(ctx, CreateRefundRequest{
		PaymentIntentID: refund.PaymentIntentID,
		AmountMinor:     refund.AmountMinor,
		Reason:          refund.Reason,
		IdempotencyKey:  refund.IdempotencyKey,
		PawrdRefundID:   refund.ID,
		PawrdOrderID:    refund.OrderID,
	})
}

func compensatedOrderRefundedAmount(tx *gorm.DB, order models.ShopOrder) (int64, error) {
	var aggregate struct {
		Amount int64
	}
	if err := tx.Model(&models.ShopRefund{}).
		Select("COALESCE(SUM(amount_minor), 0) AS amount").
		Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusSucceeded).
		Scan(&aggregate).Error; err != nil {
		return 0, err
	}
	refundedAmount := aggregate.Amount
	if order.RefundedAmountMinor > refundedAmount {
		refundedAmount = order.RefundedAmountMinor
	}
	if refundedAmount < 0 {
		refundedAmount = 0
	}
	if order.TotalAmountMinor > 0 && refundedAmount > order.TotalAmountMinor {
		refundedAmount = order.TotalAmountMinor
	}
	return refundedAmount, nil
}

func (q *DurableCompensationRefundQueue) persistResult(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
	result *CreateRefundResponse,
	now time.Time,
) error {
	if result == nil || strings.TrimSpace(result.RefundID) == "" {
		return errors.New("Stripe compensation refund returned no refund ID")
	}
	stripeRefundID := strings.TrimSpace(result.RefundID)
	stripeStatus := strings.ToLower(strings.TrimSpace(result.Status))
	refundStatus := models.ShopRefundStatusPending
	jobStatus := models.ShopCompensationRefundJobCompleted
	jobError := ""
	var completedAt *time.Time
	switch stripeStatus {
	case "succeeded":
		refundStatus = models.ShopRefundStatusSucceeded
		completedAt = &now
	case "failed", "canceled":
		refundStatus = models.ShopRefundStatusFailed
		jobStatus = models.ShopCompensationRefundJobFailed
		jobError = strings.TrimSpace(result.FailureReason)
		if jobError == "" {
			jobError = "Stripe compensation refund " + stripeStatus
		}
	}

	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var refund models.ShopRefund
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&refund, "id = ?", job.RefundID).Error; err != nil {
			return err
		}
		if refund.StripeRefundID != nil &&
			strings.TrimSpace(*refund.StripeRefundID) != "" &&
			strings.TrimSpace(*refund.StripeRefundID) != stripeRefundID {
			return errors.New("Stripe compensation refund ID does not match the durable reservation")
		}
		applyRefundResult := true
		switch strings.ToLower(strings.TrimSpace(refund.Status)) {
		case models.ShopRefundStatusSucceeded:
			if refundStatus != models.ShopRefundStatusSucceeded {
				// A Stripe webhook can commit success before the API response is
				// persisted. Never let an older pending/failed response regress it.
				applyRefundResult = false
				jobStatus = models.ShopCompensationRefundJobCompleted
				jobError = ""
			}
		case models.ShopRefundStatusFailed:
			if refundStatus == models.ShopRefundStatusPending {
				applyRefundResult = false
				jobStatus = models.ShopCompensationRefundJobFailed
				jobError = refund.FailureReason
			}
		}

		jobUpdate := tx.Model(&models.ShopCompensationRefundJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopCompensationRefundJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status": jobStatus, "locked_until": nil, "lease_owner": "",
				"last_error":   truncateCompensationError(jobError),
				"completed_at": &now,
			})
		if jobUpdate.Error != nil {
			return jobUpdate.Error
		}
		if jobUpdate.RowsAffected != 1 {
			return errors.New("compensation refund job lease was lost before completion")
		}
		if !applyRefundResult {
			return nil
		}
		updates := map[string]any{
			"stripe_refund_id": &stripeRefundID,
			"stripe_status":    stripeStatus,
			"status":           refundStatus,
			"failure_reason":   truncateCompensationError(result.FailureReason),
			"completed_at":     completedAt,
		}
		if refundStatus == models.ShopRefundStatusSucceeded &&
			refund.ShopifyMirrorStatus != models.ShopRefundMirrorStatusSucceeded {
			updates["shopify_mirror_status"] = models.ShopRefundMirrorStatusPending
			updates["shopify_mirror_error"] = ""
		}
		if err := tx.Model(&refund).Updates(updates).Error; err != nil {
			return err
		}
		if refundStatus == models.ShopRefundStatusFailed {
			failureText := truncateCompensationError(
				"Automatic Stripe refund failed and requires reconciliation: " +
					jobError,
			)
			return tx.Model(&models.ShopOrder{}).
				Where("id = ?", refund.OrderID).
				Updates(map[string]any{
					"status": refundReconciliationStatusUnlessTerminal(),
					"failure_reason": refundReconciliationFailureUnlessTerminal(
						failureText,
					),
				}).Error
		}
		if refundStatus != models.ShopRefundStatusSucceeded {
			return nil
		}
		return recalculateCompensatedOrder(tx, refund.OrderID)
	})
}

func recalculateCompensatedOrder(tx *gorm.DB, orderID string) error {
	var order models.ShopOrder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&order, "id = ?", orderID).Error; err != nil {
		return err
	}
	var aggregate struct {
		Amount int64
	}
	if err := tx.Model(&models.ShopRefund{}).
		Select("COALESCE(SUM(amount_minor), 0) AS amount").
		Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusSucceeded).
		Scan(&aggregate).Error; err != nil {
		return err
	}
	refundedAmount := aggregate.Amount
	if order.RefundedAmountMinor > refundedAmount {
		refundedAmount = order.RefundedAmountMinor
	}
	if refundedAmount > order.TotalAmountMinor {
		refundedAmount = order.TotalAmountMinor
	}
	updates := map[string]any{"refunded_amount_minor": refundedAmount}
	if refundedAmount >= order.TotalAmountMinor {
		updates["financial_status"] = "refunded"
		updates["status"] = "refunded"
	} else if refundedAmount > 0 {
		updates["financial_status"] = "partially_refunded"
	}
	return tx.Model(&order).Updates(updates).Error
}

func (q *DurableCompensationRefundQueue) persistFailure(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
	processErr error,
	now time.Time,
) {
	status := models.ShopCompensationRefundJobRetrying
	if job.Attempts >= q.maxAttempts {
		status = models.ShopCompensationRefundJobFailed
	}
	nextAttemptAt := now.Add(compensationRefundRetryDelay(job.Attempts))
	errorText := truncateCompensationError(processErr.Error())
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var refund models.ShopRefund
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&refund, "id = ?", job.RefundID).Error; err != nil {
			return err
		}
		if refund.Status == models.ShopRefundStatusSucceeded {
			// Stripe's webhook can commit success before a lost API response
			// reaches this worker. The terminal webhook state always wins.
			return tx.Model(&models.ShopCompensationRefundJob{}).
				Where("id = ? AND status = ? AND lease_owner = ?",
					job.ID,
					models.ShopCompensationRefundJobProcessing,
					job.LeaseOwner,
				).
				Updates(map[string]any{
					"status":       models.ShopCompensationRefundJobCompleted,
					"locked_until": nil,
					"lease_owner":  "",
					"last_error":   "",
					"completed_at": &now,
				}).Error
		}
		update := tx.Model(&models.ShopCompensationRefundJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopCompensationRefundJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status": status, "locked_until": nil, "lease_owner": "",
				"last_error": errorText, "next_attempt_at": nextAttemptAt,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return nil
		}
		if err := tx.Model(&models.ShopRefund{}).
			Where("id = ? AND status = ?", job.RefundID, models.ShopRefundStatusPending).
			Update("failure_reason", errorText).Error; err != nil {
			return err
		}
		if status != models.ShopCompensationRefundJobFailed {
			return nil
		}
		failureText := truncateCompensationError(
			"Automatic Stripe refund requires reconciliation: " + errorText,
		)
		return tx.Model(&models.ShopOrder{}).
			Where("id = ?", refund.OrderID).
			Updates(map[string]any{
				"status": refundReconciliationStatusUnlessTerminal(),
				"failure_reason": refundReconciliationFailureUnlessTerminal(
					failureText,
				),
			}).Error
	})
	if err != nil {
		log.Printf("[compensation-refund-queue] persist failure refund=%s: %v", job.RefundID, err)
	}
}

func (q *DurableCompensationRefundQueue) persistPaused(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
	processErr error,
	now time.Time,
) {
	errorText := truncateCompensationError(processErr.Error())
	nextAttemptAt := now.Add(compensationRefundRetryDelay(1))
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&models.ShopCompensationRefundJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopCompensationRefundJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status":          models.ShopCompensationRefundJobRetrying,
				"locked_until":    nil,
				"lease_owner":     "",
				"last_error":      errorText,
				"next_attempt_at": nextAttemptAt,
				// A dispute/financial gate is business-state waiting, not a
				// failed Stripe attempt. Neutralize claimNext's increment so
				// an active dispute can never exhaust the retry budget.
				"attempts": gorm.Expr(
					"CASE WHEN attempts > 0 THEN attempts - 1 ELSE 0 END",
				),
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return nil
		}
		return tx.Model(&models.ShopRefund{}).
			Where("id = ? AND status = ?", job.RefundID, models.ShopRefundStatusPending).
			Update("failure_reason", errorText).Error
	})
	if err != nil {
		log.Printf("[compensation-refund-queue] persist pause refund=%s: %v", job.RefundID, err)
	}
}

func (q *DurableCompensationRefundQueue) persistReconciliationRequired(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
	processErr error,
	now time.Time,
) {
	errorText := truncateCompensationError(processErr.Error())
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var refund models.ShopRefund
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&refund, "id = ?", job.RefundID).Error; err != nil {
			return err
		}
		if refund.Status == models.ShopRefundStatusSucceeded {
			return tx.Model(&models.ShopCompensationRefundJob{}).
				Where("id = ? AND status = ? AND lease_owner = ?",
					job.ID,
					models.ShopCompensationRefundJobProcessing,
					job.LeaseOwner,
				).
				Updates(map[string]any{
					"status":       models.ShopCompensationRefundJobCompleted,
					"locked_until": nil,
					"lease_owner":  "",
					"last_error":   "",
					"completed_at": &now,
				}).Error
		}
		jobUpdate := tx.Model(&models.ShopCompensationRefundJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopCompensationRefundJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status":       models.ShopCompensationRefundJobFailed,
				"locked_until": nil,
				"lease_owner":  "",
				"last_error":   errorText,
				"completed_at": &now,
			})
		if jobUpdate.Error != nil {
			return jobUpdate.Error
		}
		if jobUpdate.RowsAffected != 1 {
			return nil
		}
		if err := tx.Model(&models.ShopRefund{}).
			Where("id = ? AND status = ?", job.RefundID, models.ShopRefundStatusPending).
			Update("failure_reason", errorText).Error; err != nil {
			return err
		}
		failureText := truncateCompensationError(
			"Automatic Stripe refund requires reconciliation: " + errorText,
		)
		return tx.Model(&models.ShopOrder{}).
			Where("id = ?", refund.OrderID).
			Updates(map[string]any{
				"status": refundReconciliationStatusUnlessTerminal(),
				"failure_reason": refundReconciliationFailureUnlessTerminal(
					failureText,
				),
			}).Error
	})
	if err != nil {
		log.Printf(
			"[compensation-refund-queue] persist reconciliation refund=%s: %v",
			job.RefundID,
			err,
		)
	}
}

func (q *DurableCompensationRefundQueue) persistNotRequired(
	ctx context.Context,
	job *models.ShopCompensationRefundJob,
	processErr error,
	now time.Time,
) error {
	reason := "Stripe refund not submitted because the order has no remaining refundable balance"
	restoreMappedOrder := errors.Is(processErr, errCompensationRefundShopifyOrderExists)
	if restoreMappedOrder {
		reason = truncateCompensationError(processErr.Error())
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		jobUpdate := tx.Model(&models.ShopCompensationRefundJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopCompensationRefundJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status":       models.ShopCompensationRefundJobCompleted,
				"locked_until": nil,
				"lease_owner":  "",
				"last_error":   "",
				"completed_at": &now,
			})
		if jobUpdate.Error != nil {
			return jobUpdate.Error
		}
		if jobUpdate.RowsAffected != 1 {
			return errors.New("compensation refund job lease was lost before no-op completion")
		}
		refundUpdate := tx.Model(&models.ShopRefund{}).
			Where("id = ? AND status = ? AND stripe_refund_id IS NULL",
				job.RefundID,
				models.ShopRefundStatusPending,
			).
			Updates(map[string]any{
				"status":         models.ShopRefundStatusFailed,
				"stripe_status":  "not_applicable",
				"failure_reason": reason,
				"completed_at":   &now,
			})
		if refundUpdate.Error != nil {
			return refundUpdate.Error
		}
		if !restoreMappedOrder {
			return nil
		}
		var refund models.ShopRefund
		if err := tx.Select("order_id").First(&refund, "id = ?", job.RefundID).Error; err != nil {
			return err
		}
		return tx.Model(&models.ShopOrder{}).
			Where("id = ? AND shopify_order_id IS NOT NULL", refund.OrderID).
			Where("LOWER(financial_status) = ? AND refunded_amount_minor = 0", "paid").
			Where("COALESCE(LOWER(dispute_status), '') IN ?",
				[]string{"", "won", "prevented", "warning_closed"}).
			Where("COALESCE(LOWER(status), '') IN ?",
				[]string{"canceled", "cancelled", "refund_reconciliation_required"}).
			Where("COALESCE(UPPER(fulfillment_status), '') IN ?",
				[]string{"", "CANCELED", "CANCELLED"}).
			Where("LOWER(COALESCE(failure_reason, '')) LIKE ?", "%automatic%refund%").
			Updates(map[string]any{
				"status":             "processing",
				"fulfillment_status": "UNFULFILLED",
				"failure_reason":     "",
			}).Error
	})
}

func (q *DurableCompensationRefundQueue) renewLease(
	jobID string,
	leaseOwner string,
	stop <-chan struct{},
	done chan<- struct{},
) {
	defer close(done)
	interval := q.lease / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			lockedUntil := q.now().UTC().Add(q.lease)
			result := q.db.Model(&models.ShopCompensationRefundJob{}).
				Where("id = ? AND status = ? AND lease_owner = ?",
					jobID,
					models.ShopCompensationRefundJobProcessing,
					leaseOwner,
				).
				Update("locked_until", lockedUntil)
			if result.Error != nil {
				log.Printf("[compensation-refund-queue] renew lease job=%s: %v", jobID, result.Error)
				continue
			}
			if result.RowsAffected != 1 {
				return
			}
		}
	}
}

// ReconcilePendingCompensations restores a missing execution job. A pending
// refund with a Stripe refund ID has already been accepted externally and is
// intentionally left to Stripe webhooks instead of being submitted again.
func (q *DurableCompensationRefundQueue) ReconcilePendingCompensations(
	ctx context.Context,
	limit int,
) (int, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("compensation refund queue database is not configured")
	}
	if limit <= 0 {
		limit = defaultCompensationRefundBatch
	}
	var refunds []models.ShopRefund
	if err := q.db.WithContext(ctx).
		Where("status = ? AND stripe_refund_id IS NULL AND reason IN ?",
			models.ShopRefundStatusPending,
			[]string{
				models.ShopRefundReasonQuoteExpired,
				models.ShopRefundReasonFulfillmentFailed,
			},
		).
		Where("NOT EXISTS (?)",
			q.db.Model(&models.ShopCompensationRefundJob{}).
				Select("1").
				Where("shop_compensation_refund_jobs.refund_id = shop_refunds.id"),
		).
		Order("updated_at ASC").Limit(limit).Find(&refunds).Error; err != nil {
		return 0, err
	}
	now := q.now().UTC()
	enqueued := 0
	for _, refund := range refunds {
		if err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return ensureCompensationRefundJob(tx, refund.ID, now)
		}); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	if enqueued > 0 {
		q.notify()
	}
	return enqueued, nil
}

func (q *DurableCompensationRefundQueue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *DurableCompensationRefundQueue) processAndLog(ctx context.Context) {
	if _, err := q.ProcessPending(ctx, defaultCompensationRefundBatch); err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Printf("[compensation-refund-queue] worker failed: %v", err)
	}
}

func isSystemCompensationReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case models.ShopRefundReasonQuoteExpired,
		models.ShopRefundReasonFulfillmentFailed:
		return true
	default:
		return false
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func truncateCompensationError(value string) string {
	const maxCharacters = 1000
	characters := []rune(strings.TrimSpace(value))
	if len(characters) > maxCharacters {
		characters = characters[:maxCharacters]
	}
	return string(characters)
}

func compensationRefundRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	minutes := math.Pow(2, float64(attempt-1))
	if minutes > 60 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}
