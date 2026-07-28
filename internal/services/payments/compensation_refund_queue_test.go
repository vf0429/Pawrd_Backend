package payments

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type recordingCompensationRefunder struct {
	mu        sync.Mutex
	calls     []CreateRefundRequest
	failures  []error
	responses []*CreateRefundResponse
}

type webhookWinsCompensationRefunder struct {
	db        *gorm.DB
	refundID  string
	orderID   string
	returnErr error
}

func (r *webhookWinsCompensationRefunder) CreateRefund(
	context.Context,
	CreateRefundRequest,
) (*CreateRefundResponse, error) {
	stripeRefundID := "re_webhook_won"
	now := time.Now().UTC()
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ShopRefund{}).
			Where("id = ?", r.refundID).
			Updates(map[string]any{
				"stripe_refund_id": &stripeRefundID,
				"stripe_status":    "succeeded",
				"status":           models.ShopRefundStatusSucceeded,
				"completed_at":     &now,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&models.ShopOrder{}).
			Where("id = ?", r.orderID).
			Updates(map[string]any{
				"financial_status":      "refunded",
				"status":                "refunded",
				"refunded_amount_minor": 8500,
			}).Error
	}); err != nil {
		return nil, err
	}
	if r.returnErr != nil {
		return nil, r.returnErr
	}
	// This is the older API response that arrived after the webhook.
	return &CreateRefundResponse{
		RefundID: stripeRefundID,
		Status:   "pending",
	}, nil
}

func (r *recordingCompensationRefunder) CreateRefund(
	_ context.Context,
	request CreateRefundRequest,
) (*CreateRefundResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, request)
	index := len(r.calls) - 1
	if index < len(r.failures) && r.failures[index] != nil {
		return nil, r.failures[index]
	}
	if index < len(r.responses) && r.responses[index] != nil {
		response := *r.responses[index]
		return &response, nil
	}
	return &CreateRefundResponse{
		RefundID: "re_compensation", Status: "succeeded",
	}, nil
}

func (r *recordingCompensationRefunder) snapshotCalls() []CreateRefundRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CreateRefundRequest(nil), r.calls...)
}

func newCompensationRefundTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.ShopOrder{},
		&models.ShopRefund{},
		&models.ShopCompensationRefundJob{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func createCompensationOrder(
	t *testing.T,
	db *gorm.DB,
	paymentIntentID string,
) models.ShopOrder {
	t.Helper()
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: uuid.NewString(),
		PaymentIntentID: paymentIntentID,
		Status:          "canceled", FinancialStatus: "paid",
		FulfillmentStatus: "CANCELLED",
		Currency:          "HKD", TotalAmountMinor: 8500,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	return order
}

func reserveCompensation(
	t *testing.T,
	db *gorm.DB,
	orderID string,
	reason string,
	now time.Time,
) models.ShopRefund {
	t.Helper()
	var refund *models.ShopRefund
	if err := db.Transaction(func(tx *gorm.DB) error {
		var order models.ShopOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&order, "id = ?", orderID).Error; err != nil {
			return err
		}
		var err error
		refund, err = EnsureSystemCompensationRefund(tx, &order, reason, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if refund == nil {
		t.Fatal("compensation refund was not reserved")
	}
	return *refund
}

func TestEnsureSystemCompensationRefundIsAtomicAndIdempotent(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_expired_idempotent")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	second := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now.Add(time.Minute),
	)
	if first.ID != second.ID || first.IdempotencyKey != second.IdempotencyKey {
		t.Fatalf("duplicate reservation changed identity: first=%+v second=%+v", first, second)
	}
	if first.AmountMinor != order.TotalAmountMinor ||
		first.Reason != models.ShopRefundReasonQuoteExpired ||
		first.RequestedBy != "system:"+models.ShopRefundReasonQuoteExpired {
		t.Fatalf("unexpected compensation reservation: %+v", first)
	}
	var refunds, jobs int64
	if err := db.Model(&models.ShopRefund{}).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ShopCompensationRefundJob{}).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || jobs != 1 {
		t.Fatalf("duplicate reservation created refunds/jobs=%d/%d", refunds, jobs)
	}
}

func TestCompensationRefundQueueRetriesTransportAmbiguityWithSameKey(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_expired_ambiguous")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	refunder := &recordingCompensationRefunder{
		failures: []error{errors.New("Stripe response lost"), nil},
		responses: []*CreateRefundResponse{
			nil,
			{RefundID: "re_same_operation", Status: "succeeded"},
		},
	}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }

	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("first process=%d err=%v", processed, err)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobRetrying {
		t.Fatalf("ambiguous job status=%q, want retrying", job.Status)
	}

	now = job.NextAttemptAt
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("retry process=%d err=%v", processed, err)
	}
	calls := refunder.snapshotCalls()
	if len(calls) != 2 ||
		calls[0].IdempotencyKey == "" ||
		calls[0].IdempotencyKey != calls[1].IdempotencyKey ||
		calls[0].IdempotencyKey != refund.IdempotencyKey {
		t.Fatalf("transport retry changed Stripe idempotency: %#v", calls)
	}

	var updatedRefund models.ShopRefund
	if err := db.First(&updatedRefund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedRefund.Status != models.ShopRefundStatusSucceeded ||
		updatedRefund.StripeRefundID == nil ||
		*updatedRefund.StripeRefundID != "re_same_operation" {
		t.Fatalf("compensation success not persisted: %+v", updatedRefund)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FinancialStatus != "refunded" ||
		order.Status != "refunded" ||
		order.RefundedAmountMinor != order.TotalAmountMinor {
		t.Fatalf("compensated order money state is wrong: %+v", order)
	}
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobCompleted ||
		job.CompletedAt == nil {
		t.Fatalf("compensation job did not complete: %+v", job)
	}
}

func TestCompensationRefundQueueRefusesAgedAmbiguousStripeRetry(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_compensation_aged_retry")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	refunder := &recordingCompensationRefunder{
		failures: []error{errors.New("Stripe response lost")},
	}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("first process=%d err=%v", processed, err)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	now = job.NextAttemptAt
	expiredSubmission := now.Add(-StripeRefundIdempotencyRetryWindow)
	if err := db.Model(&models.ShopRefund{}).
		Where("id = ?", refund.ID).
		Update("stripe_first_submitted_at", &expiredSubmission).Error; err != nil {
		t.Fatal(err)
	}
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("aged retry process=%d err=%v", processed, err)
	}
	if calls := refunder.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("aged Stripe idempotency operation was replayed: %#v", calls)
	}
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobFailed {
		t.Fatalf("aged compensation retry remained runnable: %+v", job)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusPending {
		t.Fatalf("aged ambiguous reservation was released: %+v", refund)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refund_reconciliation_required" {
		t.Fatalf("aged ambiguity did not surface reconciliation: %+v", order)
	}
}

func TestCompensationRefundQueueReconcilesMissingExecutionJob(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_expired_reconcile")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	if err := db.Where("refund_id = ?", refund.ID).
		Delete(&models.ShopCompensationRefundJob{}).Error; err != nil {
		t.Fatal(err)
	}

	queue := NewDurableCompensationRefundQueue(
		db,
		&recordingCompensationRefunder{},
		nil,
	)
	queue.now = func() time.Time { return now.Add(time.Minute) }
	count, err := queue.ReconcilePendingCompensations(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobPending {
		t.Fatalf("reconciled job status=%q, want pending", job.Status)
	}
}

func TestCompensationRefundQueueDoesNotRegressWebhookSuccessWithOlderAPIResponse(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_webhook_wins")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	queue := NewDurableCompensationRefundQueue(
		db,
		&webhookWinsCompensationRefunder{
			db: db, refundID: refund.ID, orderID: order.ID,
		},
		nil,
	)
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("process=%d err=%v", processed, err)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusSucceeded ||
		refund.StripeStatus != "succeeded" {
		t.Fatalf("older API response regressed webhook success: %+v", refund)
	}
}

func TestCompensationRefundQueueMaxAttemptErrorDoesNotRegressWebhookSuccess(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_webhook_wins_max_error")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	queue := NewDurableCompensationRefundQueue(
		db,
		&webhookWinsCompensationRefunder{
			db: db, refundID: refund.ID, orderID: order.ID,
			returnErr: errors.New("Stripe response lost after webhook success"),
		},
		nil,
	)
	queue.maxAttempts = 1
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("process=%d err=%v", processed, err)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusSucceeded ||
		refund.StripeStatus != "succeeded" {
		t.Fatalf("max-attempt error regressed webhook success: %+v", refund)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobCompleted {
		t.Fatalf("webhook-success job was not completed: %+v", job)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refunded" ||
		order.FinancialStatus != "refunded" ||
		order.RefundedAmountMinor != order.TotalAmountMinor {
		t.Fatalf("max-attempt error downgraded refunded order: %+v", order)
	}
}

func TestCompensationRefundQueueReconciliationDecisionDoesNotRegressWebhookSuccess(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_webhook_wins_reconciliation")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	queue := NewDurableCompensationRefundQueue(db, &recordingCompensationRefunder{}, nil)
	queue.now = func() time.Time { return now }
	job, err := queue.claimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stripeRefundID := "re_webhook_before_reconciliation_persist"
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ShopRefund{}).
			Where("id = ?", refund.ID).
			Updates(map[string]any{
				"stripe_refund_id": &stripeRefundID,
				"stripe_status":    "succeeded",
				"status":           models.ShopRefundStatusSucceeded,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&models.ShopOrder{}).
			Where("id = ?", order.ID).
			Updates(map[string]any{
				"status":                "refunded",
				"financial_status":      "refunded",
				"refunded_amount_minor": order.TotalAmountMinor,
			}).Error
	}); err != nil {
		t.Fatal(err)
	}
	queue.persistReconciliationRequired(
		context.Background(),
		job,
		errors.New("stale reconciliation decision"),
		now,
	)
	if err := db.First(job, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobCompleted {
		t.Fatalf("stale reconciliation did not yield to webhook success: %+v", job)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refunded" ||
		order.FinancialStatus != "refunded" {
		t.Fatalf("stale reconciliation downgraded refunded order: %+v", order)
	}
}

func TestCompensationRefundPersistenceDoesNotRegressConcurrentDispute(t *testing.T) {
	for _, test := range []struct {
		name    string
		persist func(
			*DurableCompensationRefundQueue,
			*models.ShopCompensationRefundJob,
			time.Time,
		) error
	}{
		{
			name: "Stripe failed response",
			persist: func(
				queue *DurableCompensationRefundQueue,
				job *models.ShopCompensationRefundJob,
				now time.Time,
			) error {
				return queue.persistResult(
					context.Background(),
					job,
					&CreateRefundResponse{
						RefundID: "re_failed_during_dispute",
						Status:   "failed", FailureReason: "refund rejected",
					},
					now,
				)
			},
		},
		{
			name: "max attempt failure",
			persist: func(
				queue *DurableCompensationRefundQueue,
				job *models.ShopCompensationRefundJob,
				now time.Time,
			) error {
				queue.maxAttempts = 1
				queue.persistFailure(
					context.Background(),
					job,
					errors.New("Stripe response lost"),
					now,
				)
				return nil
			},
		},
		{
			name: "stale reconciliation decision",
			persist: func(
				queue *DurableCompensationRefundQueue,
				job *models.ShopCompensationRefundJob,
				now time.Time,
			) error {
				queue.persistReconciliationRequired(
					context.Background(),
					job,
					errors.New("stale reconciliation decision"),
					now,
				)
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newCompensationRefundTestDB(t)
			order := createCompensationOrder(
				t,
				db,
				"pi_compensation_dispute_"+strings.ReplaceAll(test.name, " ", "_"),
			)
			now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
			refund := reserveCompensation(
				t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
			)
			queue := NewDurableCompensationRefundQueue(
				db,
				&recordingCompensationRefunder{},
				nil,
			)
			job, err := queue.claimNext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&models.ShopOrder{}).
				Where("id = ?", order.ID).
				Updates(map[string]any{
					"status": "payment_disputed", "financial_status": "disputed",
					"dispute_status": "needs_response", "dispute_id": "dp_persist_race",
					"failure_reason": "Stripe dispute requires review",
				}).Error; err != nil {
				t.Fatal(err)
			}
			if err := test.persist(queue, job, now); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if order.Status != "payment_disputed" ||
				order.FinancialStatus != "disputed" ||
				order.FailureReason != "Stripe dispute requires review" {
				t.Fatalf("refund persistence regressed concurrent dispute: %+v", order)
			}
			if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompensationRefundQueuePausesForActiveDisputeAndResumesAfterWin(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_compensation_dispute_gate")
	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	if err := db.Model(&models.ShopOrder{}).
		Where("id = ?", order.ID).
		Updates(map[string]any{
			"financial_status": "disputed",
			"dispute_id":       "dp_compensation_active",
			"dispute_status":   "needs_response",
		}).Error; err != nil {
		t.Fatal(err)
	}

	refunder := &recordingCompensationRefunder{}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }

	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("active-dispute process=%d err=%v", processed, err)
	}
	if calls := refunder.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("active dispute reached Stripe: %#v", calls)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobRetrying ||
		job.Attempts != 0 ||
		!strings.Contains(job.LastError, "active or lost payment dispute") {
		t.Fatalf("active dispute was not recoverably paused: %+v", job)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusPending ||
		refund.StripeRefundID != nil {
		t.Fatalf("paused compensation reservation changed terminal state: %+v", refund)
	}

	if err := db.Model(&models.ShopOrder{}).
		Where("id = ?", order.ID).
		Updates(map[string]any{
			"financial_status": "paid",
			"dispute_status":   "won",
		}).Error; err != nil {
		t.Fatal(err)
	}
	now = job.NextAttemptAt
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("won-dispute process=%d err=%v", processed, err)
	}
	calls := refunder.snapshotCalls()
	if len(calls) != 1 || calls[0].PawrdRefundID != refund.ID {
		t.Fatalf("won dispute did not resume exactly one Stripe refund: %#v", calls)
	}
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobCompleted {
		t.Fatalf("resumed compensation job status=%q, want completed", job.Status)
	}
}

func TestCompensationRefundQueueCompletesWithoutStripeWhenAlreadyRefunded(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_compensation_already_refunded")
	now := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	if err := db.Model(&models.ShopOrder{}).
		Where("id = ?", order.ID).
		Updates(map[string]any{
			"status":                    "refunded",
			"financial_status":          "refunded",
			"refunded_amount_minor":     order.TotalAmountMinor,
			"dispute_status":            "",
			"disputed_amount_minor":     0,
			"dispute_event_created":     0,
			"fulfillment_status":        "CANCELLED",
			"fulfillment_request_error": "",
		}).Error; err != nil {
		t.Fatal(err)
	}

	refunder := &recordingCompensationRefunder{}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("already-refunded process=%d err=%v", processed, err)
	}
	if calls := refunder.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("already-refunded order reached Stripe: %#v", calls)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobCompleted {
		t.Fatalf("already-refunded job status=%q, want completed", job.Status)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusFailed ||
		refund.StripeStatus != "not_applicable" {
		t.Fatalf("unused reservation was not released: %+v", refund)
	}
}

func TestCompensationRefundQueueDoesNotRefundMappedShopifyOrder(t *testing.T) {
	for _, test := range []struct {
		name              string
		status            string
		fulfillmentStatus string
		wantStatus        string
		wantFulfillment   string
	}{
		{
			name:   "restore compensation cancellation",
			status: "canceled", fulfillmentStatus: "CANCELLED",
			wantStatus: "processing", wantFulfillment: "UNFULFILLED",
		},
		{
			name:   "preserve advanced fulfillment",
			status: "delivered", fulfillmentStatus: "DELIVERED",
			wantStatus: "delivered", wantFulfillment: "DELIVERED",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newCompensationRefundTestDB(t)
			order := createCompensationOrder(t, db, "pi_mapped_"+strings.ReplaceAll(test.name, " ", "_"))
			now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
			refund := reserveCompensation(
				t, db, order.ID, models.ShopRefundReasonFulfillmentFailed, now,
			)
			shopifyID := "gid://shopify/Order/mapped-before-refund"
			if err := db.Model(&models.ShopOrder{}).
				Where("id = ?", order.ID).
				Updates(map[string]any{
					"shopify_order_id":   &shopifyID,
					"status":             test.status,
					"fulfillment_status": test.fulfillmentStatus,
					"failure_reason":     "Shopify order could not be created; automatic refund queued",
				}).Error; err != nil {
				t.Fatal(err)
			}

			refunder := &recordingCompensationRefunder{}
			queue := NewDurableCompensationRefundQueue(db, refunder, nil)
			queue.now = func() time.Time { return now }
			if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
				t.Fatalf("process=%d err=%v", processed, err)
			}
			if calls := refunder.snapshotCalls(); len(calls) != 0 {
				t.Fatalf("mapped Shopify order reached Stripe: %#v", calls)
			}
			if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
				t.Fatal(err)
			}
			if refund.Status != models.ShopRefundStatusFailed ||
				refund.StripeStatus != "not_applicable" {
				t.Fatalf("mapped-order reservation was not released: %+v", refund)
			}
			var stored models.ShopOrder
			if err := db.First(&stored, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != test.wantStatus ||
				stored.FulfillmentStatus != test.wantFulfillment {
				t.Fatalf("mapped order lifecycle regressed: %+v", stored)
			}
		})
	}
}

func TestCompensationRefundQueueShrinksUntouchedReservationAfterExternalPartialRefund(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_compensation_shrink")
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	externalRefundID := "re_external_partial"
	if err := db.Create(&models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID,
		PaymentIntentID: order.PaymentIntentID,
		StripeRefundID:  &externalRefundID,
		IdempotencyKey:  "external-partial-" + order.ID,
		AmountMinor:     2000,
		Currency:        "HKD",
		Reason:          "external",
		Status:          models.ShopRefundStatusSucceeded,
		StripeStatus:    "succeeded",
		RequestedBy:     "stripe-webhook",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ShopOrder{}).
		Where("id = ?", order.ID).
		Updates(map[string]any{
			"status": "partially_refunded", "financial_status": "partially_refunded",
			"refunded_amount_minor": 2000,
		}).Error; err != nil {
		t.Fatal(err)
	}

	refunder := &recordingCompensationRefunder{}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("process=%d err=%v", processed, err)
	}
	calls := refunder.snapshotCalls()
	if len(calls) != 1 || calls[0].AmountMinor != 6500 {
		t.Fatalf("Stripe did not receive exact remaining balance: %#v", calls)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.AmountMinor != 6500 ||
		refund.Status != models.ShopRefundStatusSucceeded {
		t.Fatalf("reservation was not atomically reduced and completed: %+v", refund)
	}
	var pending int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ? AND status = ?", order.ID, models.ShopRefundStatusPending).
		Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("safe shrink left %d pending reservations blocking operator actions", pending)
	}
}

func TestCompensationRefundQueueDoesNotChangeAmountAfterAmbiguousStripeAttempt(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_compensation_amount_ambiguous")
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonQuoteExpired, now,
	)
	refunder := &recordingCompensationRefunder{
		failures: []error{errors.New("Stripe response lost")},
	}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("first process=%d err=%v", processed, err)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	externalRefundID := "re_external_after_ambiguity"
	if err := db.Create(&models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID,
		PaymentIntentID: order.PaymentIntentID,
		StripeRefundID:  &externalRefundID,
		IdempotencyKey:  "external-after-ambiguity-" + order.ID,
		AmountMinor:     2000,
		Currency:        "HKD",
		Reason:          "external",
		Status:          models.ShopRefundStatusSucceeded,
		StripeStatus:    "succeeded",
		RequestedBy:     "stripe-webhook",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ShopOrder{}).
		Where("id = ?", order.ID).
		Updates(map[string]any{
			"status": "partially_refunded", "financial_status": "partially_refunded",
			"refunded_amount_minor": 2000,
		}).Error; err != nil {
		t.Fatal(err)
	}
	now = job.NextAttemptAt
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("second process=%d err=%v", processed, err)
	}
	if calls := refunder.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("ambiguous Stripe operation was resubmitted with changed parameters: %#v", calls)
	}
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobFailed {
		t.Fatalf("ambiguous changed amount did not stop automatically: %+v", job)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.AmountMinor != order.TotalAmountMinor ||
		refund.Status != models.ShopRefundStatusPending {
		t.Fatalf("ambiguous Stripe reservation was mutated: %+v", refund)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refund_reconciliation_required" {
		t.Fatalf("ambiguous Stripe amount did not surface reconciliation: %+v", order)
	}
}

func TestCompensationRefundQueueKeepsReservationWhenShopifyMappingFollowsAmbiguousStripeAttempt(t *testing.T) {
	db := newCompensationRefundTestDB(t)
	order := createCompensationOrder(t, db, "pi_compensation_gid_after_ambiguity")
	now := time.Date(2026, 7, 29, 2, 0, 0, 0, time.UTC)
	refund := reserveCompensation(
		t, db, order.ID, models.ShopRefundReasonFulfillmentFailed, now,
	)
	refunder := &recordingCompensationRefunder{
		failures: []error{errors.New("Stripe response lost")},
	}
	queue := NewDurableCompensationRefundQueue(db, refunder, nil)
	queue.now = func() time.Time { return now }
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("first process=%d err=%v", processed, err)
	}
	var job models.ShopCompensationRefundJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	shopifyID := "gid://shopify/Order/appeared-after-stripe-timeout"
	if err := db.Model(&models.ShopOrder{}).
		Where("id = ?", order.ID).
		Update("shopify_order_id", &shopifyID).Error; err != nil {
		t.Fatal(err)
	}
	now = job.NextAttemptAt
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("second process=%d err=%v", processed, err)
	}
	if calls := refunder.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("mapping after ambiguous Stripe attempt triggered another call: %#v", calls)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusPending ||
		refund.StripeStatus == "not_applicable" {
		t.Fatalf("ambiguous reservation was unsafely released: %+v", refund)
	}
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopCompensationRefundJobFailed {
		t.Fatalf("ambiguous mapped refund job remained runnable: %+v", job)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refund_reconciliation_required" ||
		order.FulfillmentStatus != "CANCELLED" {
		t.Fatalf("ambiguous mapped order did not fail closed: %+v", order)
	}
}
