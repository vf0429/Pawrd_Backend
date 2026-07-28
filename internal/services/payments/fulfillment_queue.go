package payments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultFulfillmentLease       = 2 * time.Minute
	defaultFulfillmentPoll        = 2 * time.Second
	defaultFulfillmentReconcile   = 5 * time.Minute
	defaultFulfillmentBatch       = 10
	defaultFulfillmentMaxAttempts = 8
)

var (
	errFulfillmentDispatchLeaseLost = errors.New(
		"fulfillment dispatch lease was lost before the external call",
	)
	errFulfillmentDispatchBecameIneligible = errors.New(
		"order became ineligible before the external fulfillment call",
	)
	errFulfillmentDispatchAlreadyCompleted = errors.New(
		"Shopify order was mapped before the external fulfillment call",
	)
	errFulfillmentDispatchFenceBusy = errors.New(
		"another process owns the Shopify order dispatch fence",
	)
	errFulfillmentDispatchFenceUnavailable = errors.New(
		"Shopify order dispatch fence is unavailable",
	)
	errFulfillmentDispatchStateUnavailable = errors.New(
		"fulfillment state could not be revalidated before the external call",
	)
	errFulfillmentDispatchPreviouslyStarted = errors.New(
		"Shopify order dispatch was previously started and cannot be repeated automatically",
	)
)

type fulfillmentLocalFenceEntry struct {
	mu   sync.Mutex
	refs int
}

var fulfillmentLocalFences = struct {
	sync.Mutex
	entries map[string]*fulfillmentLocalFenceEntry
}{entries: make(map[string]*fulfillmentLocalFenceEntry)}

// DurableFulfillmentQueue implements Fulfiller by persisting work first. The
// downstream fulfiller is called only by the background worker, never in the
// Stripe webhook request.
type DurableFulfillmentQueue struct {
	db                *gorm.DB
	downstream        Fulfiller
	now               func() time.Time
	wake              chan struct{}
	lease             time.Duration
	pollInterval      time.Duration
	reconcileInterval time.Duration
	maxAttempts       int
}

func NewDurableFulfillmentQueue(db *gorm.DB, downstream Fulfiller) *DurableFulfillmentQueue {
	return &DurableFulfillmentQueue{
		db:                db,
		downstream:        downstream,
		now:               time.Now,
		wake:              make(chan struct{}, 1),
		lease:             defaultFulfillmentLease,
		pollInterval:      defaultFulfillmentPoll,
		reconcileInterval: defaultFulfillmentReconcile,
		maxAttempts:       defaultFulfillmentMaxAttempts,
	}
}

// Fulfill durably enqueues a paid order. Repeated Stripe events reuse the
// unique PaymentIntent job and therefore cannot create parallel Shopify orders.
func (q *DurableFulfillmentQueue) Fulfill(req FulfillmentRequest) error {
	if q == nil || q.db == nil {
		return errors.New("fulfillment queue database is not configured")
	}
	req.PaymentIntentID = strings.TrimSpace(req.PaymentIntentID)
	if req.PaymentIntentID == "" {
		return errors.New("fulfillment queue requires a PaymentIntent ID")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode fulfillment request: %w", err)
	}
	now := q.now().UTC()
	err = q.db.Transaction(func(tx *gorm.DB) error {
		order, blockedReason, err := fulfillmentOrderState(tx, req.PaymentIntentID, true)
		if err != nil {
			return err
		}
		if order.ShopifyOrderGID() != "" || blockedReason != "" {
			// A repeated payment event after cancellation/refund must be
			// acknowledged without recreating fulfillment work. The durable
			// order state remains the operator-visible source of the reason.
			return nil
		}

		var job models.ShopFulfillmentJob
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("payment_intent_id = ?", req.PaymentIntentID).First(&job).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			job = models.ShopFulfillmentJob{
				ID: uuid.NewString(), PaymentIntentID: req.PaymentIntentID,
				Payload: string(payload), Status: models.ShopFulfillmentJobPending,
				NextAttemptAt: now,
			}
			// PaymentIntent IDs are globally unique and safe as stable job IDs.
			if err := tx.Create(&job).Error; err != nil {
				return err
			}
		case findErr != nil:
			return findErr
		case job.Status == models.ShopFulfillmentJobCompleted:
			// Reconciliation may discover a completed job whose local order
			// mapping was not committed. Reset it only when Shopify is absent.
			var order models.ShopOrder
			orderErr := tx.Select("shopify_order_id").
				Where("payment_intent_id = ?", req.PaymentIntentID).First(&order).Error
			if orderErr == nil && order.ShopifyOrderGID() == "" {
				if err := tx.Model(&job).Updates(map[string]any{
					"payload": string(payload), "status": models.ShopFulfillmentJobPending,
					"attempts": 0, "next_attempt_at": now, "locked_until": nil,
					"lease_owner": "", "last_error": "", "completed_at": nil,
				}).Error; err != nil {
					return err
				}
			}
		case job.Status == models.ShopFulfillmentJobProcessing &&
			job.LockedUntil != nil && job.LockedUntil.After(now):
			// A live worker owns the lease; updating the payload is enough.
			if err := tx.Model(&job).Update("payload", string(payload)).Error; err != nil {
				return err
			}
		default:
			if err := tx.Model(&job).Updates(map[string]any{
				"payload": string(payload), "status": models.ShopFulfillmentJobPending,
				"next_attempt_at": now, "locked_until": nil, "lease_owner": "", "last_error": "",
			}).Error; err != nil {
				return err
			}
		}

		result := tx.Model(&models.ShopOrder{}).
			Where("payment_intent_id = ? AND LOWER(financial_status) NOT IN ?",
				req.PaymentIntentID,
				[]string{"partially_refunded", "refunded", "disputed", "dispute_lost"}).
			Updates(map[string]any{
				"status": "fulfillment_pending", "financial_status": "paid",
				"failure_reason": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("paid order became ineligible while fulfillment was enqueued")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("enqueue fulfillment: %w", err)
	}
	q.notify()
	return nil
}

// Run processes queued fulfillment jobs until the context is cancelled.
func (q *DurableFulfillmentQueue) Run(ctx context.Context) {
	if q == nil || q.db == nil || q.downstream == nil {
		log.Printf("[fulfillment-queue] disabled: database or downstream fulfiller is missing")
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
			if _, err := q.ReconcilePaidOrders(ctx, defaultFulfillmentBatch); err != nil {
				log.Printf("[fulfillment-queue] reconciliation failed: %v", err)
			}
			q.processAndLog(ctx)
		}
	}
}

func (q *DurableFulfillmentQueue) processAndLog(ctx context.Context) {
	if _, err := q.ProcessPending(ctx, defaultFulfillmentBatch); err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Printf("[fulfillment-queue] worker failed: %v", err)
	}
}

// ProcessPending claims and processes up to limit due jobs. Claiming uses a
// conditional update so multiple app instances cannot execute the same lease.
func (q *DurableFulfillmentQueue) ProcessPending(ctx context.Context, limit int) (int, error) {
	if q == nil || q.db == nil || q.downstream == nil {
		return 0, errors.New("fulfillment queue is not fully configured")
	}
	if limit <= 0 {
		limit = defaultFulfillmentBatch
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
		q.processClaimed(job)
	}
	return processed, nil
}

func (q *DurableFulfillmentQueue) claimNext(ctx context.Context) (*models.ShopFulfillmentJob, error) {
	now := q.now().UTC()
	var candidates []models.ShopFulfillmentJob
	err := q.db.WithContext(ctx).
		Where("next_attempt_at <= ? AND ((status IN ?) OR (status = ? AND (locked_until IS NULL OR locked_until <= ?)))",
			now,
			[]string{models.ShopFulfillmentJobPending, models.ShopFulfillmentJobRetrying},
			models.ShopFulfillmentJobProcessing, now).
		Order("next_attempt_at ASC, created_at ASC").Limit(5).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		lockedUntil := now.Add(q.lease)
		leaseOwner := uuid.NewString()
		result := q.db.WithContext(ctx).Model(&models.ShopFulfillmentJob{}).
			Where("id = ? AND next_attempt_at <= ? AND ((status IN ?) OR (status = ? AND (locked_until IS NULL OR locked_until <= ?)))",
				candidate.ID, now,
				[]string{models.ShopFulfillmentJobPending, models.ShopFulfillmentJobRetrying},
				models.ShopFulfillmentJobProcessing, now).
			Updates(map[string]any{
				"status":       models.ShopFulfillmentJobProcessing,
				"locked_until": lockedUntil,
				"lease_owner":  leaseOwner,
				"attempts":     gorm.Expr("attempts + 1"),
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 1 {
			var claimed models.ShopFulfillmentJob
			if err := q.db.WithContext(ctx).First(&claimed, "id = ?", candidate.ID).Error; err != nil {
				return nil, err
			}
			if claimed.LeaseOwner != leaseOwner {
				continue
			}
			return &claimed, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (q *DurableFulfillmentQueue) processClaimed(job *models.ShopFulfillmentJob) {
	order, blockedReason, stateErr := fulfillmentOrderState(q.db, job.PaymentIntentID, false)
	if stateErr != nil {
		q.failClaimedWithoutCallingDownstream(job, stateErr)
		return
	}
	if order.ShopifyOrderGID() != "" {
		q.completeClaimedWithoutCallingDownstream(job)
		return
	}
	if blockedReason != "" {
		q.cancelClaimedWithoutCallingDownstream(job, blockedReason)
		return
	}

	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go q.renewClaimLease(job.ID, job.LeaseOwner, stopHeartbeat, heartbeatDone)

	var req FulfillmentRequest
	err := json.Unmarshal([]byte(job.Payload), &req)
	if err == nil {
		err = q.withFulfillmentDispatchFence(job.PaymentIntentID, func() error {
			return q.dispatchClaimedAfterFence(job, req)
		})
	}
	close(stopHeartbeat)
	<-heartbeatDone

	now := q.now().UTC()
	if err == nil {
		result := q.db.Model(&models.ShopFulfillmentJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID, models.ShopFulfillmentJobProcessing, job.LeaseOwner).
			Updates(map[string]any{
				"status": models.ShopFulfillmentJobCompleted, "locked_until": nil,
				"lease_owner": "", "last_error": "", "completed_at": &now,
			})
		if result.Error != nil {
			log.Printf("[fulfillment-queue] mark completed payment=%s: %v", job.PaymentIntentID, result.Error)
		} else if result.RowsAffected != 1 {
			log.Printf("[fulfillment-queue] completion ignored after lease loss payment=%s", job.PaymentIntentID)
		}
		return
	}

	switch {
	case errors.Is(err, errFulfillmentDispatchLeaseLost):
		log.Printf(
			"[fulfillment-queue] external call skipped after lease loss payment=%s",
			job.PaymentIntentID,
		)
		return
	case errors.Is(err, errFulfillmentDispatchAlreadyCompleted):
		q.completeClaimedWithoutCallingDownstream(job)
		return
	case errors.Is(err, errFulfillmentDispatchBecameIneligible):
		q.cancelClaimedWithoutCallingDownstream(job, err.Error())
		return
	case errors.Is(err, errFulfillmentDispatchFenceBusy),
		errors.Is(err, errFulfillmentDispatchStateUnavailable):
		// A different process may still be inside orderCreate, or the latest
		// durable state could not be read. Neither case is evidence that the
		// Shopify order is absent, so keep retrying even after maxAttempts.
		q.failClaimedWithoutCallingDownstream(job, err)
		return
	case errors.Is(err, errFulfillmentDispatchFenceUnavailable):
		// Without the cross-process fence, executing orderCreate could create
		// duplicate orders. Stop automatically and require reconciliation.
		q.markClaimedReconciliationRequired(job, err)
		return
	case errors.Is(err, errFulfillmentDispatchPreviouslyStarted):
		q.markClaimedReconciliationRequired(job, err)
		return
	}

	if errors.Is(err, ErrFulfillmentOrderCreateAmbiguous) {
		q.markClaimedReconciliationRequired(job, err)
		return
	}
	if errors.Is(err, ErrFulfillmentOrderDefinitelyRejected) {
		q.compensateClaimedWithoutShopifyOrder(job, err)
		return
	}
	if job.Attempts >= q.maxAttempts {
		q.resolveExhaustedClaim(job, err)
		return
	}
	delay := fulfillmentRetryDelay(job.Attempts)
	nextAttempt := now.Add(delay)
	result := q.db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ? AND status = ? AND lease_owner = ?",
			job.ID, models.ShopFulfillmentJobProcessing, job.LeaseOwner).
		Updates(map[string]any{
			"status":       models.ShopFulfillmentJobRetrying,
			"locked_until": nil, "lease_owner": "",
			"last_error":      truncateCompensationError(err.Error()),
			"next_attempt_at": nextAttempt,
		})
	if result.Error != nil {
		log.Printf("[fulfillment-queue] persist failure payment=%s: %v", job.PaymentIntentID, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		log.Printf("[fulfillment-queue] failure ignored after lease loss payment=%s", job.PaymentIntentID)
		return
	}
	_ = q.db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ? AND shopify_order_id IS NULL", job.PaymentIntentID).
		Updates(map[string]any{
			"status": orderStatusUnlessTerminal("fulfillment_retrying"),
			"failure_reason": orderFailureUnlessTerminal(
				truncateCompensationError(err.Error()),
			),
		}).Error
}

func (q *DurableFulfillmentQueue) dispatchClaimedAfterFence(
	job *models.ShopFulfillmentJob,
	req FulfillmentRequest,
) error {
	var current models.ShopFulfillmentJob
	if err := q.db.Select("id", "status", "lease_owner", "dispatch_started_at").
		First(&current, "id = ?", job.ID).Error; err != nil {
		return fmt.Errorf("%w: load fulfillment lease: %v", errFulfillmentDispatchStateUnavailable, err)
	}
	if current.Status != models.ShopFulfillmentJobProcessing ||
		current.LeaseOwner != job.LeaseOwner {
		return errFulfillmentDispatchLeaseLost
	}

	order, blockedReason, err := fulfillmentOrderState(q.db, job.PaymentIntentID, false)
	if err != nil {
		return fmt.Errorf("%w: load latest order state: %v", errFulfillmentDispatchStateUnavailable, err)
	}
	if order.ShopifyOrderGID() != "" {
		return nil
	}
	if blockedReason != "" {
		return fmt.Errorf("%w: %s", errFulfillmentDispatchBecameIneligible, blockedReason)
	}
	if current.DispatchStartedAt != nil {
		reconciler, ok := q.downstream.(ShopifyOrderReconciler)
		if !ok {
			return fmt.Errorf(
				"%w: sourceIdentifier lookup is unavailable",
				errFulfillmentDispatchPreviouslyStarted,
			)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		mapped, err := reconciler.ReconcileShopifyOrder(ctx, job.PaymentIntentID)
		switch {
		case err != nil:
			return fmt.Errorf(
				"%w: sourceIdentifier lookup failed: %v",
				errFulfillmentDispatchPreviouslyStarted,
				err,
			)
		case mapped:
			return nil
		default:
			return fmt.Errorf(
				"%w: sourceIdentifier lookup returned no order",
				errFulfillmentDispatchPreviouslyStarted,
			)
		}
	}

	req.BeforeExternalDispatch = func() error {
		return q.markClaimedDispatchStarted(job)
	}
	return q.downstream.Fulfill(req)
}

// markClaimedDispatchStarted is called by Dispatcher only after its Shopify
// lookup and local order/quote validation have succeeded. Persisting this
// marker immediately before orderCreate closes the response-loss retry window
// without turning pre-dispatch validation failures into ambiguous dispatches.
func (q *DurableFulfillmentQueue) markClaimedDispatchStarted(
	job *models.ShopFulfillmentJob,
) error {
	var current models.ShopFulfillmentJob
	if err := q.db.Select("id", "status", "lease_owner", "dispatch_started_at").
		First(&current, "id = ?", job.ID).Error; err != nil {
		return fmt.Errorf(
			"%w: reload fulfillment lease: %v",
			errFulfillmentDispatchStateUnavailable,
			err,
		)
	}
	if current.Status != models.ShopFulfillmentJobProcessing ||
		current.LeaseOwner != job.LeaseOwner {
		return errFulfillmentDispatchLeaseLost
	}
	if current.DispatchStartedAt != nil {
		return fmt.Errorf(
			"%w: external dispatch marker was already present",
			errFulfillmentDispatchPreviouslyStarted,
		)
	}

	order, blockedReason, err := fulfillmentOrderState(q.db, job.PaymentIntentID, false)
	if err != nil {
		return fmt.Errorf(
			"%w: reload latest order state: %v",
			errFulfillmentDispatchStateUnavailable,
			err,
		)
	}
	if order.ShopifyOrderGID() != "" {
		return errFulfillmentDispatchAlreadyCompleted
	}
	if blockedReason != "" {
		return fmt.Errorf(
			"%w: %s",
			errFulfillmentDispatchBecameIneligible,
			blockedReason,
		)
	}

	dispatchStartedAt := q.now().UTC()
	marker := q.db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ? AND status = ? AND lease_owner = ? AND dispatch_started_at IS NULL",
			job.ID,
			models.ShopFulfillmentJobProcessing,
			job.LeaseOwner,
		).
		Update("dispatch_started_at", &dispatchStartedAt)
	if marker.Error != nil {
		return fmt.Errorf(
			"%w: persist external dispatch marker: %v",
			errFulfillmentDispatchStateUnavailable,
			marker.Error,
		)
	}
	if marker.RowsAffected != 1 {
		return fmt.Errorf(
			"%w: external dispatch marker was not acquired",
			errFulfillmentDispatchStateUnavailable,
		)
	}
	return nil
}

func (q *DurableFulfillmentQueue) withFulfillmentDispatchFence(
	paymentIntentID string,
	fn func() error,
) error {
	releaseLocal := acquireFulfillmentLocalFence(paymentIntentID)
	defer releaseLocal()

	if q.db.Dialector.Name() != "postgres" {
		return fn()
	}

	sqlDB, err := q.db.DB()
	if err != nil {
		return fmt.Errorf("%w: get SQL database: %v", errFulfillmentDispatchFenceUnavailable, err)
	}
	connCtx, cancelConn := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := sqlDB.Conn(connCtx)
	cancelConn()
	if err != nil {
		return fmt.Errorf("%w: reserve SQL connection: %v", errFulfillmentDispatchFenceUnavailable, err)
	}
	defer conn.Close()

	// BeginTx's context controls the transaction lifetime. It must remain live
	// for the complete external orderCreate call; the deferred rollback below
	// is the sole release path for this transaction-scoped advisory lock.
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("%w: begin advisory transaction: %v", errFulfillmentDispatchFenceUnavailable, err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil &&
			!errors.Is(rollbackErr, sql.ErrTxDone) {
			log.Printf(
				"[fulfillment-queue] release Shopify dispatch transaction payment=%s err=%v",
				paymentIntentID,
				rollbackErr,
			)
		}
	}()

	timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = tx.ExecContext(
		timeoutCtx,
		"SET LOCAL idle_in_transaction_session_timeout = 0",
	)
	cancelTimeout()
	if err != nil {
		return fmt.Errorf(
			"%w: disable idle transaction timeout for advisory fence: %v",
			errFulfillmentDispatchFenceUnavailable,
			err,
		)
	}

	key := fulfillmentDispatchAdvisoryKey(paymentIntentID)
	lockCtx, cancelLock := context.WithTimeout(context.Background(), 5*time.Second)
	var acquired bool
	err = tx.QueryRowContext(
		lockCtx,
		"SELECT pg_try_advisory_xact_lock($1)",
		key,
	).Scan(&acquired)
	cancelLock()
	if err != nil {
		return fmt.Errorf("%w: acquire PostgreSQL advisory lock: %v", errFulfillmentDispatchFenceUnavailable, err)
	}
	if !acquired {
		return errFulfillmentDispatchFenceBusy
	}

	return fn()
}

func acquireFulfillmentLocalFence(paymentIntentID string) func() {
	key := strings.TrimSpace(paymentIntentID)
	fulfillmentLocalFences.Lock()
	entry := fulfillmentLocalFences.entries[key]
	if entry == nil {
		entry = &fulfillmentLocalFenceEntry{}
		fulfillmentLocalFences.entries[key] = entry
	}
	entry.refs++
	fulfillmentLocalFences.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		fulfillmentLocalFences.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(fulfillmentLocalFences.entries, key)
		}
		fulfillmentLocalFences.Unlock()
	}
}

func fulfillmentDispatchAdvisoryKey(paymentIntentID string) int64 {
	sum := sha256.Sum256([]byte("pawrd:shopify-order:" + strings.TrimSpace(paymentIntentID)))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

func (q *DurableFulfillmentQueue) resolveExhaustedClaim(
	job *models.ShopFulfillmentJob,
	processErr error,
) {
	reconciler, ok := q.downstream.(ShopifyOrderReconciler)
	if !ok {
		q.markClaimedReconciliationRequired(
			job,
			fmt.Errorf(
				"%v; final Shopify sourceIdentifier reconciliation is unavailable",
				processErr,
			),
		)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	mapped, err := reconciler.ReconcileShopifyOrder(ctx, job.PaymentIntentID)
	switch {
	case err != nil:
		q.markClaimedReconciliationRequired(
			job,
			fmt.Errorf("%v; final Shopify sourceIdentifier lookup: %w", processErr, err),
		)
	case mapped:
		q.completeClaimedWithoutCallingDownstream(job)
	default:
		// A sourceIdentifier miss is not documented by Shopify as a
		// strongly-consistent proof that a timed-out orderCreate was rejected.
		// Only the typed mutation userError path may compensate automatically.
		q.markClaimedReconciliationRequired(
			job,
			fmt.Errorf(
				"%v; Shopify sourceIdentifier lookup returned no order after an ambiguous create result",
				processErr,
			),
		)
	}
}

func (q *DurableFulfillmentQueue) compensateClaimedWithoutShopifyOrder(
	job *models.ShopFulfillmentJob,
	processErr error,
) {
	now := q.now().UTC()
	err := q.db.Transaction(func(tx *gorm.DB) error {
		var order models.ShopOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&order, "payment_intent_id = ?", job.PaymentIntentID).Error; err != nil {
			return err
		}
		if order.ShopifyOrderGID() != "" {
			return completeFulfillmentJobInTransaction(tx, job, now)
		}
		if !strings.EqualFold(strings.TrimSpace(order.FinancialStatus), "paid") {
			return fmt.Errorf(
				"refusing fulfillment compensation from financial state %q",
				order.FinancialStatus,
			)
		}
		switch strings.ToLower(strings.TrimSpace(order.DisputeStatus)) {
		case "", "won", "prevented", "warning_closed":
		default:
			return errors.New("refusing fulfillment compensation during an active or lost dispute")
		}
		if _, err := EnsureSystemCompensationRefund(
			tx,
			&order,
			models.ShopRefundReasonFulfillmentFailed,
			now,
		); err != nil {
			return err
		}

		jobUpdate := tx.Model(&models.ShopFulfillmentJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopFulfillmentJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status":       models.ShopFulfillmentJobCanceled,
				"locked_until": nil, "lease_owner": "",
				"last_error":   truncateCompensationError(processErr.Error()),
				"completed_at": &now,
			})
		if jobUpdate.Error != nil {
			return jobUpdate.Error
		}
		if jobUpdate.RowsAffected != 1 {
			return errors.New("fulfillment job lease was lost before compensation")
		}
		return tx.Model(&order).Updates(map[string]any{
			"status":             "canceled",
			"fulfillment_status": "CANCELLED",
			"failure_reason": "Shopify order could not be created; " +
				"automatic refund queued",
		}).Error
	})
	if err != nil {
		q.markClaimedReconciliationRequired(
			job,
			fmt.Errorf("%v; reserve automatic compensation: %w", processErr, err),
		)
	}
}

func completeFulfillmentJobInTransaction(
	tx *gorm.DB,
	job *models.ShopFulfillmentJob,
	now time.Time,
) error {
	update := tx.Model(&models.ShopFulfillmentJob{}).
		Where("id = ? AND status = ? AND lease_owner = ?",
			job.ID,
			models.ShopFulfillmentJobProcessing,
			job.LeaseOwner,
		).
		Updates(map[string]any{
			"status":       models.ShopFulfillmentJobCompleted,
			"locked_until": nil, "lease_owner": "", "last_error": "",
			"completed_at": &now,
		})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("fulfillment job lease was lost before recovered completion")
	}
	return nil
}

func (q *DurableFulfillmentQueue) markClaimedReconciliationRequired(
	job *models.ShopFulfillmentJob,
	processErr error,
) {
	errorText := truncateCompensationError(processErr.Error())
	err := q.db.Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&models.ShopFulfillmentJob{}).
			Where("id = ? AND status = ? AND lease_owner = ?",
				job.ID,
				models.ShopFulfillmentJobProcessing,
				job.LeaseOwner,
			).
			Updates(map[string]any{
				"status":       models.ShopFulfillmentJobFailed,
				"locked_until": nil, "lease_owner": "",
				"last_error": errorText,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return nil
		}
		return tx.Model(&models.ShopOrder{}).
			Where("payment_intent_id = ?", job.PaymentIntentID).
			Updates(map[string]any{
				"status":         orderStatusUnlessTerminal("reconciliation_required"),
				"failure_reason": orderFailureUnlessTerminal(errorText),
			}).Error
	})
	if err != nil {
		log.Printf(
			"[fulfillment-queue] persist reconciliation state payment=%s: %v",
			job.PaymentIntentID,
			err,
		)
	}
}

func (q *DurableFulfillmentQueue) completeClaimedWithoutCallingDownstream(job *models.ShopFulfillmentJob) {
	now := q.now().UTC()
	result := q.db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ? AND status = ? AND lease_owner = ?",
			job.ID, models.ShopFulfillmentJobProcessing, job.LeaseOwner).
		Updates(map[string]any{
			"status": models.ShopFulfillmentJobCompleted, "locked_until": nil,
			"lease_owner": "", "last_error": "", "completed_at": &now,
		})
	if result.Error != nil {
		log.Printf("[fulfillment-queue] complete mapped order payment=%s: %v", job.PaymentIntentID, result.Error)
	}
}

func (q *DurableFulfillmentQueue) cancelClaimedWithoutCallingDownstream(
	job *models.ShopFulfillmentJob,
	reason string,
) {
	result := q.db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ? AND status = ? AND lease_owner = ?",
			job.ID, models.ShopFulfillmentJobProcessing, job.LeaseOwner).
		Updates(map[string]any{
			"status": models.ShopFulfillmentJobCanceled, "locked_until": nil,
			"lease_owner": "", "last_error": reason,
		})
	if result.Error != nil {
		log.Printf("[fulfillment-queue] cancel ineligible payment=%s: %v", job.PaymentIntentID, result.Error)
	}
}

func (q *DurableFulfillmentQueue) failClaimedWithoutCallingDownstream(
	job *models.ShopFulfillmentJob,
	err error,
) {
	nextAttempt := q.now().UTC().Add(fulfillmentRetryDelay(job.Attempts))
	result := q.db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ? AND status = ? AND lease_owner = ?",
			job.ID, models.ShopFulfillmentJobProcessing, job.LeaseOwner).
		Updates(map[string]any{
			"status": models.ShopFulfillmentJobRetrying, "locked_until": nil,
			"lease_owner": "", "last_error": err.Error(), "next_attempt_at": nextAttempt,
		})
	if result.Error != nil {
		log.Printf("[fulfillment-queue] requeue state-check failure payment=%s: %v", job.PaymentIntentID, result.Error)
	}
}

func fulfillmentOrderState(
	db *gorm.DB,
	paymentIntentID string,
	lock bool,
) (models.ShopOrder, string, error) {
	var order models.ShopOrder
	query := db
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&order, "payment_intent_id = ?", paymentIntentID).Error; err != nil {
		return order, "", err
	}

	switch strings.ToLower(strings.TrimSpace(order.FinancialStatus)) {
	case "", "pending", "paid":
	default:
		return order, "order financial state no longer permits fulfillment", nil
	}
	switch strings.ToLower(strings.TrimSpace(order.Status)) {
	case "canceled", "cancelled", "payment_canceled",
		"refund_pending", "refunded", "partially_refunded", "disputed",
		"dispute_lost", "reconciliation_required", "refund_reconciliation_required":
		return order, "order lifecycle no longer permits fulfillment", nil
	}
	switch strings.ToUpper(strings.TrimSpace(order.FulfillmentStatus)) {
	case "CANCELED", "CANCELLED":
		return order, "order fulfillment was canceled", nil
	}
	switch strings.ToLower(strings.TrimSpace(order.DisputeStatus)) {
	case "", "won", "prevented", "warning_closed":
	default:
		return order, "order has an active or lost payment dispute", nil
	}

	var activeRefunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ? AND status IN ?", order.ID,
			[]string{models.ShopRefundStatusPending, models.ShopRefundStatusSucceeded}).
		Count(&activeRefunds).Error; err != nil {
		return order, "", err
	}
	if activeRefunds > 0 {
		return order, "order has a pending or completed refund", nil
	}
	return order, "", nil
}

func (q *DurableFulfillmentQueue) renewClaimLease(
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
			result := q.db.Model(&models.ShopFulfillmentJob{}).
				Where("id = ? AND status = ? AND lease_owner = ?",
					jobID, models.ShopFulfillmentJobProcessing, leaseOwner).
				Update("locked_until", lockedUntil)
			if result.Error != nil {
				log.Printf("[fulfillment-queue] renew lease job=%s: %v", jobID, result.Error)
				continue
			}
			if result.RowsAffected != 1 {
				return
			}
		}
	}
}

func fulfillmentRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	minutes := math.Pow(2, float64(attempt-1))
	if minutes > 60 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}

// ReconcilePaidOrders restores missing jobs, including the narrow crash window
// where Shopify accepted orderCreate but the local order mapping was not saved.
// The dispatcher resolves that window by looking up sourceIdentifier before it
// attempts another orderCreate.
func (q *DurableFulfillmentQueue) ReconcilePaidOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = defaultFulfillmentBatch
	}
	var orders []models.ShopOrder
	if err := q.db.WithContext(ctx).Preload("Items").
		Where("LOWER(financial_status) = ? AND shopify_order_id IS NULL AND payment_intent_id <> ?", "paid", "").
		Where("COALESCE(LOWER(status), '') NOT IN ?", []string{
			"canceled", "cancelled", "payment_canceled",
			"refund_pending", "refunded", "partially_refunded", "disputed",
			"dispute_lost", "reconciliation_required", "refund_reconciliation_required",
		}).
		Where("COALESCE(UPPER(fulfillment_status), '') NOT IN ?", []string{"CANCELED", "CANCELLED"}).
		Where("COALESCE(LOWER(dispute_status), '') IN ?",
			[]string{"", "won", "prevented", "warning_closed"}).
		Where("NOT EXISTS (?)", q.db.Model(&models.ShopRefund{}).
			Select("1").
			Where("shop_refunds.order_id = shop_orders.id AND shop_refunds.status IN ?",
				[]string{models.ShopRefundStatusPending, models.ShopRefundStatusSucceeded})).
		Order("updated_at ASC").Limit(limit).Find(&orders).Error; err != nil {
		return 0, err
	}
	enqueued := 0
	for _, order := range orders {
		items := make([]FulfillmentItem, 0, len(order.Items))
		for _, item := range order.Items {
			items = append(items, FulfillmentItem{
				Source: ItemSource(item.Source), Handle: item.Handle,
				VariantID: item.VariantID, Quantity: item.Quantity,
			})
		}
		err := q.Fulfill(FulfillmentRequest{
			PaymentIntentID: order.PaymentIntentID,
			CustomerName:    order.CustomerName, CustomerEmail: order.CustomerEmail,
			CustomerPhone: order.CustomerPhone, Items: items,
		})
		if err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

func (q *DurableFulfillmentQueue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
