package payments

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultRefundMirrorLease       = 2 * time.Minute
	defaultRefundMirrorPoll        = 2 * time.Second
	defaultRefundMirrorReconcile   = 5 * time.Minute
	defaultRefundMirrorBatch       = 10
	defaultRefundMirrorMaxAttempts = 8
)

var errRefundMirrorNotApplicable = errors.New("Shopify refund mirror is not applicable")

// RefundMirrorEnqueuer is used by the operator endpoint and Stripe webhook
// only after a local Stripe refund has reached succeeded.
type RefundMirrorEnqueuer interface {
	EnqueueRefundMirror(context.Context, string) error
}

// DurableRefundMirrorQueue asynchronously records already-succeeded Stripe
// refunds in Shopify. It never calls Stripe and never supplies line items or
// restock instructions to Shopify.
type DurableRefundMirrorQueue struct {
	db                *gorm.DB
	downstream        shopify.AdminRefundMirrorClient
	now               func() time.Time
	wake              chan struct{}
	lease             time.Duration
	pollInterval      time.Duration
	reconcileInterval time.Duration
	maxAttempts       int
}

func NewDurableRefundMirrorQueue(
	db *gorm.DB,
	downstream shopify.AdminRefundMirrorClient,
) *DurableRefundMirrorQueue {
	return &DurableRefundMirrorQueue{
		db:                db,
		downstream:        downstream,
		now:               time.Now,
		wake:              make(chan struct{}, 1),
		lease:             defaultRefundMirrorLease,
		pollInterval:      defaultRefundMirrorPoll,
		reconcileInterval: defaultRefundMirrorReconcile,
		maxAttempts:       defaultRefundMirrorMaxAttempts,
	}
}

// EnqueueRefundMirror persists one job per Pawrd refund. A completed job is
// reset only if the refund's local Shopify mirror fields were not committed;
// replay remains safe because the Pawrd refund UUID is the stable Shopify
// idempotency key.
func (q *DurableRefundMirrorQueue) EnqueueRefundMirror(ctx context.Context, refundID string) error {
	if q == nil || q.db == nil {
		return errors.New("refund mirror queue database is not configured")
	}
	refundID = strings.TrimSpace(refundID)
	if refundID == "" {
		return errors.New("refund mirror queue requires a refund ID")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := q.now().UTC()
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var refund models.ShopRefund
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&refund, "id = ?", refundID).Error; err != nil {
			return err
		}
		if refund.Status != models.ShopRefundStatusSucceeded {
			return fmt.Errorf("refund %s has not succeeded in Stripe", refund.ID)
		}
		if refund.AmountMinor <= 0 || len(strings.TrimSpace(refund.Currency)) != 3 {
			return fmt.Errorf("refund %s has invalid money data", refund.ID)
		}
		if refund.ShopifyMirrorStatus == models.ShopRefundMirrorStatusSucceeded &&
			refund.ShopifyRefundID != nil &&
			strings.TrimSpace(*refund.ShopifyRefundID) != "" {
			return nil
		}
		if refund.ShopifyMirrorStatus == models.ShopRefundMirrorStatusNotApplicable {
			return nil
		}

		var job models.ShopRefundMirrorJob
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("refund_id = ?", refund.ID).First(&job).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			job = models.ShopRefundMirrorJob{
				ID: uuid.NewString(), RefundID: refund.ID,
				Status:        models.ShopRefundMirrorJobPending,
				NextAttemptAt: now,
			}
			if err := tx.Create(&job).Error; err != nil {
				return err
			}
		case findErr != nil:
			return findErr
		case job.Status == models.ShopRefundMirrorJobProcessing &&
			job.LockedUntil != nil && job.LockedUntil.After(now):
			// A live worker owns this lease.
		default:
			if err := tx.Model(&job).Updates(map[string]any{
				"status":          models.ShopRefundMirrorJobPending,
				"next_attempt_at": now, "locked_until": nil,
				"lease_owner": "", "last_error": "", "completed_at": nil,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&refund).
			Where("shopify_mirror_status <> ? OR shopify_mirror_status IS NULL",
				models.ShopRefundMirrorStatusSucceeded).
			Updates(map[string]any{
				"shopify_mirror_status": models.ShopRefundMirrorStatusPending,
				"shopify_mirror_error":  "",
			}).Error
	})
	if err != nil {
		return fmt.Errorf("enqueue Shopify refund mirror: %w", err)
	}
	q.notifyRefundMirror()
	return nil
}

func (q *DurableRefundMirrorQueue) Run(ctx context.Context) {
	if q == nil || q.db == nil || q.downstream == nil {
		log.Printf("[refund-mirror-queue] disabled: database or Shopify Admin client is missing")
		return
	}
	pollTicker := time.NewTicker(q.pollInterval)
	reconcileTicker := time.NewTicker(q.reconcileInterval)
	defer pollTicker.Stop()
	defer reconcileTicker.Stop()

	q.processRefundMirrorsAndLog(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
			q.processRefundMirrorsAndLog(ctx)
		case <-pollTicker.C:
			q.processRefundMirrorsAndLog(ctx)
		case <-reconcileTicker.C:
			if _, err := q.ReconcileSucceededRefunds(ctx, defaultRefundMirrorBatch); err != nil {
				log.Printf("[refund-mirror-queue] reconciliation failed: %v", err)
			}
			q.processRefundMirrorsAndLog(ctx)
		}
	}
}

func (q *DurableRefundMirrorQueue) ProcessPending(ctx context.Context, limit int) (int, error) {
	if q == nil || q.db == nil || q.downstream == nil {
		return 0, errors.New("refund mirror queue is not fully configured")
	}
	if limit <= 0 {
		limit = defaultRefundMirrorBatch
	}
	processed := 0
	for processed < limit {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		job, err := q.claimNextRefundMirror(ctx)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return processed, nil
		}
		if err != nil {
			return processed, err
		}
		processed++
		q.processClaimedRefundMirror(ctx, job)
	}
	return processed, nil
}

func (q *DurableRefundMirrorQueue) claimNextRefundMirror(
	ctx context.Context,
) (*models.ShopRefundMirrorJob, error) {
	now := q.now().UTC()
	var candidates []models.ShopRefundMirrorJob
	err := q.db.WithContext(ctx).
		Where("next_attempt_at <= ? AND ((status IN ?) OR (status = ? AND (locked_until IS NULL OR locked_until <= ?)))",
			now,
			[]string{models.ShopRefundMirrorJobPending, models.ShopRefundMirrorJobRetrying},
			models.ShopRefundMirrorJobProcessing, now).
		Order("next_attempt_at ASC, created_at ASC").Limit(5).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		lockedUntil := now.Add(q.lease)
		leaseOwner := uuid.NewString()
		result := q.db.WithContext(ctx).Model(&models.ShopRefundMirrorJob{}).
			Where("id = ? AND next_attempt_at <= ? AND ((status IN ?) OR (status = ? AND (locked_until IS NULL OR locked_until <= ?)))",
				candidate.ID, now,
				[]string{models.ShopRefundMirrorJobPending, models.ShopRefundMirrorJobRetrying},
				models.ShopRefundMirrorJobProcessing, now).
			Updates(map[string]any{
				"status":       models.ShopRefundMirrorJobProcessing,
				"locked_until": lockedUntil, "lease_owner": leaseOwner,
				"attempts": gorm.Expr("attempts + 1"),
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			continue
		}
		var claimed models.ShopRefundMirrorJob
		if err := q.db.WithContext(ctx).First(&claimed, "id = ?", candidate.ID).Error; err != nil {
			return nil, err
		}
		if claimed.LeaseOwner == leaseOwner {
			return &claimed, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (q *DurableRefundMirrorQueue) processClaimedRefundMirror(
	ctx context.Context,
	job *models.ShopRefundMirrorJob,
) {
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go q.renewRefundMirrorLease(job.ID, job.LeaseOwner, stopHeartbeat, heartbeatDone)
	result, err := q.mirrorRefund(ctx, job)
	close(stopHeartbeat)
	<-heartbeatDone
	now := q.now().UTC()
	if errors.Is(err, errRefundMirrorNotApplicable) {
		if notApplicableErr := q.completeRefundMirrorNotApplicable(ctx, job, now); notApplicableErr == nil {
			return
		} else {
			err = notApplicableErr
		}
	}
	if err == nil {
		shopifyRefundID := strings.TrimSpace(result.RefundID)
		err = q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			update := tx.Model(&models.ShopRefundMirrorJob{}).
				Where("id = ? AND status = ? AND lease_owner = ?",
					job.ID, models.ShopRefundMirrorJobProcessing, job.LeaseOwner).
				Updates(map[string]any{
					"status":       models.ShopRefundMirrorJobCompleted,
					"locked_until": nil, "lease_owner": "", "last_error": "",
					"completed_at": &now,
				})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return errors.New("refund mirror job lease was lost before completion")
			}
			return tx.Model(&models.ShopRefund{}).Where("id = ?", job.RefundID).
				Updates(map[string]any{
					"shopify_mirror_status":         models.ShopRefundMirrorStatusSucceeded,
					"shopify_refund_id":             &shopifyRefundID,
					"shopify_refund_transaction_id": strings.TrimSpace(result.TransactionID),
					"shopify_mirror_error":          "", "shopify_mirrored_at": &now,
				}).Error
		})
		if err == nil {
			return
		}
	}
	if err == nil {
		err = errors.New("Shopify refund mirror returned an empty result")
	}
	q.persistRefundMirrorFailure(ctx, job, err, now)
}

func (q *DurableRefundMirrorQueue) mirrorRefund(
	ctx context.Context,
	job *models.ShopRefundMirrorJob,
) (*shopify.AdminExternalRefundResult, error) {
	var refund models.ShopRefund
	if err := q.db.WithContext(ctx).First(&refund, "id = ?", job.RefundID).Error; err != nil {
		return nil, err
	}
	if refund.Status != models.ShopRefundStatusSucceeded {
		return nil, fmt.Errorf("refund %s is no longer succeeded", refund.ID)
	}
	if refund.StripeRefundID == nil || strings.TrimSpace(*refund.StripeRefundID) == "" {
		return nil, fmt.Errorf("refund %s has no Stripe refund ID", refund.ID)
	}
	var order models.ShopOrder
	if err := q.db.WithContext(ctx).First(&order, "id = ?", refund.OrderID).Error; err != nil {
		return nil, err
	}
	shopifyOrderID := order.ShopifyOrderGID()
	if shopifyOrderID == "" {
		if strings.EqualFold(strings.TrimSpace(order.FulfillmentStatus), "CANCELLED") &&
			(strings.EqualFold(strings.TrimSpace(order.Status), "canceled") ||
				strings.EqualFold(strings.TrimSpace(order.Status), "refunded") ||
				strings.EqualFold(strings.TrimSpace(order.FinancialStatus), "refunded") ||
				strings.EqualFold(strings.TrimSpace(order.FinancialStatus), "partially_refunded")) {
			// A payment that completed after its sealed quote expired is kept
			// locally as canceled/CANCELLED and intentionally never creates a
			// Shopify order. Its Stripe refund therefore has nothing to mirror.
			return nil, errRefundMirrorNotApplicable
		}
		// Fulfillment may still be recovering its own durable Shopify order
		// mapping. This is retryable and uses the same refund idempotency key.
		return nil, fmt.Errorf("refund %s Shopify order is not mapped yet", refund.ID)
	}
	result, err := q.downstream.RecordExternalRefund(ctx, shopify.AdminExternalRefundInput{
		OrderID:        shopifyOrderID,
		StripeRefundID: strings.TrimSpace(*refund.StripeRefundID),
		AmountMinor:    refund.AmountMinor,
		Currency:       strings.ToUpper(strings.TrimSpace(refund.Currency)),
		IdempotencyKey: refund.ID,
	})
	if err != nil {
		return nil, err
	}
	if result == nil ||
		result.AmountMinor != refund.AmountMinor ||
		!strings.EqualFold(strings.TrimSpace(result.Currency), strings.TrimSpace(refund.Currency)) {
		return nil, fmt.Errorf("Shopify refund mirror amount or currency did not match Stripe")
	}
	if strings.TrimSpace(result.RefundID) == "" ||
		strings.TrimSpace(result.TransactionID) == "" {
		return nil, fmt.Errorf("Shopify refund mirror returned incomplete identifiers")
	}
	return result, nil
}

func (q *DurableRefundMirrorQueue) completeRefundMirrorNotApplicable(
	ctx context.Context,
	job *models.ShopRefundMirrorJob,
	now time.Time,
) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&models.ShopRefundMirrorJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID, models.ShopRefundMirrorJobProcessing, job.LeaseOwner).
			Updates(map[string]any{
				"status":       models.ShopRefundMirrorJobCompleted,
				"locked_until": nil, "lease_owner": "", "last_error": "",
				"completed_at": &now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("refund mirror job lease was lost before not-applicable completion")
		}
		return tx.Model(&models.ShopRefund{}).Where("id = ?", job.RefundID).
			Updates(map[string]any{
				"shopify_mirror_status": models.ShopRefundMirrorStatusNotApplicable,
				"shopify_mirror_error":  "",
			}).Error
	})
}

func (q *DurableRefundMirrorQueue) renewRefundMirrorLease(
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
			result := q.db.Model(&models.ShopRefundMirrorJob{}).
				Where("id = ? AND status = ? AND lease_owner = ?",
					jobID, models.ShopRefundMirrorJobProcessing, leaseOwner).
				Update("locked_until", lockedUntil)
			if result.Error != nil {
				log.Printf("[refund-mirror-queue] renew lease job=%s: %v", jobID, result.Error)
				continue
			}
			if result.RowsAffected != 1 {
				return
			}
		}
	}
}

func (q *DurableRefundMirrorQueue) persistRefundMirrorFailure(
	ctx context.Context,
	job *models.ShopRefundMirrorJob,
	processErr error,
	now time.Time,
) {
	status := models.ShopRefundMirrorJobRetrying
	mirrorStatus := models.ShopRefundMirrorStatusRetrying
	if job.Attempts >= q.maxAttempts {
		status = models.ShopRefundMirrorJobFailed
		mirrorStatus = models.ShopRefundMirrorStatusFailed
	}
	nextAttemptAt := now.Add(refundMirrorRetryDelay(job.Attempts))
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&models.ShopRefundMirrorJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID, models.ShopRefundMirrorJobProcessing, job.LeaseOwner).
			Updates(map[string]any{
				"status": status, "locked_until": nil, "lease_owner": "",
				"last_error": processErr.Error(), "next_attempt_at": nextAttemptAt,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return nil
		}
		return tx.Model(&models.ShopRefund{}).
			Where("id = ? AND (shopify_mirror_status <> ? OR shopify_mirror_status IS NULL)",
				job.RefundID, models.ShopRefundMirrorStatusSucceeded).
			Updates(map[string]any{
				"shopify_mirror_status": mirrorStatus,
				"shopify_mirror_error":  processErr.Error(),
			}).Error
	})
	if err != nil {
		log.Printf("[refund-mirror-queue] persist failure refund=%s: %v", job.RefundID, err)
	}
}

// ReconcileSucceededRefunds closes the narrow crash window between committing
// Stripe success and enqueueing the Shopify mirror job.
func (q *DurableRefundMirrorQueue) ReconcileSucceededRefunds(
	ctx context.Context,
	limit int,
) (int, error) {
	if q == nil || q.db == nil {
		return 0, errors.New("refund mirror queue database is not configured")
	}
	if limit <= 0 {
		limit = defaultRefundMirrorBatch
	}
	var refunds []models.ShopRefund
	if err := q.db.WithContext(ctx).
		Where("status = ? AND (shopify_mirror_status IS NULL OR shopify_mirror_status NOT IN ?)",
			models.ShopRefundStatusSucceeded,
			[]string{
				models.ShopRefundMirrorStatusSucceeded,
				models.ShopRefundMirrorStatusNotApplicable,
			}).
		Order("updated_at ASC").Limit(limit).Find(&refunds).Error; err != nil {
		return 0, err
	}
	enqueued := 0
	for _, refund := range refunds {
		if err := q.EnqueueRefundMirror(ctx, refund.ID); err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

func (q *DurableRefundMirrorQueue) notifyRefundMirror() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *DurableRefundMirrorQueue) processRefundMirrorsAndLog(ctx context.Context) {
	if _, err := q.ProcessPending(ctx, defaultRefundMirrorBatch); err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Printf("[refund-mirror-queue] worker failed: %v", err)
	}
}

func refundMirrorRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	minutes := math.Pow(2, float64(attempt-1))
	if minutes > 60 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}
