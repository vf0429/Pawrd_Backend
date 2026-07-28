package payments

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type recordingFulfiller struct {
	mu       sync.Mutex
	requests []FulfillmentRequest
	err      error
}

type blockingFulfiller struct {
	started chan struct{}
	release chan struct{}
}

type mappingBlockingFulfiller struct {
	db      *gorm.DB
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

type terminalMutatingFulfiller struct {
	db              *gorm.DB
	paymentIntentID string
	updates         map[string]any
	err             error
}

type fulfillmentFenceDriver struct {
	state *fulfillmentFenceDriverState
}

type fulfillmentFenceDriverState struct {
	mu                  sync.Mutex
	txActive            bool
	rollbacks           int
	idleTimeoutDisabled bool
}

type fulfillmentFenceConn struct {
	state *fulfillmentFenceDriverState
}

type fulfillmentFenceTx struct {
	state *fulfillmentFenceDriverState
}

type fulfillmentFenceRows struct {
	delivered bool
}

type reconcilingFulfiller struct {
	db             *gorm.DB
	err            error
	reconcileErr   error
	reconcileFound bool
	requests       int
	reconciles     int
}

func (d *fulfillmentFenceDriver) Open(string) (driver.Conn, error) {
	return &fulfillmentFenceConn{state: d.state}, nil
}

func (c *fulfillmentFenceConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *fulfillmentFenceConn) Close() error { return nil }

func (c *fulfillmentFenceConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *fulfillmentFenceConn) BeginTx(
	context.Context,
	driver.TxOptions,
) (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.txActive = true
	return &fulfillmentFenceTx{state: c.state}, nil
}

func (c *fulfillmentFenceConn) QueryContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Rows, error) {
	if query != "SELECT pg_try_advisory_xact_lock($1)" {
		return nil, errors.New("unexpected query: " + query)
	}
	c.state.mu.Lock()
	active := c.state.txActive
	c.state.mu.Unlock()
	if !active {
		return nil, errors.New("advisory query ran outside a transaction")
	}
	return &fulfillmentFenceRows{}, nil
}

func (c *fulfillmentFenceConn) ExecContext(
	_ context.Context,
	query string,
	_ []driver.NamedValue,
) (driver.Result, error) {
	if query != "SET LOCAL idle_in_transaction_session_timeout = 0" {
		return nil, errors.New("unexpected exec: " + query)
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if !c.state.txActive {
		return nil, errors.New("SET LOCAL ran outside a transaction")
	}
	c.state.idleTimeoutDisabled = true
	return driver.RowsAffected(0), nil
}

func (tx *fulfillmentFenceTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.txActive = false
	return nil
}

func (tx *fulfillmentFenceTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.txActive = false
	tx.state.rollbacks++
	return nil
}

func (*fulfillmentFenceRows) Columns() []string { return []string{"acquired"} }

func (*fulfillmentFenceRows) Close() error { return nil }

func (rows *fulfillmentFenceRows) Next(dest []driver.Value) error {
	if rows.delivered {
		return io.EOF
	}
	rows.delivered = true
	dest[0] = true
	return nil
}

func (f *reconcilingFulfiller) Fulfill(FulfillmentRequest) error {
	f.requests++
	return f.err
}

func (f *reconcilingFulfiller) ReconcileShopifyOrder(
	_ context.Context,
	paymentIntentID string,
) (bool, error) {
	f.reconciles++
	if f.reconcileErr != nil {
		return false, f.reconcileErr
	}
	if !f.reconcileFound {
		return false, nil
	}
	shopifyID := "gid://shopify/Order/recovered"
	if f.db != nil {
		if err := f.db.Model(&models.ShopOrder{}).
			Where("payment_intent_id = ?", paymentIntentID).
			Update("shopify_order_id", &shopifyID).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}

func (f *blockingFulfiller) Fulfill(FulfillmentRequest) error {
	close(f.started)
	<-f.release
	return nil
}

func (f *mappingBlockingFulfiller) Fulfill(req FulfillmentRequest) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	f.once.Do(func() { close(f.started) })
	<-f.release
	shopifyID := "gid://shopify/Order/fenced"
	return f.db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", req.PaymentIntentID).
		Update("shopify_order_id", &shopifyID).Error
}

func (f *mappingBlockingFulfiller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *terminalMutatingFulfiller) Fulfill(FulfillmentRequest) error {
	if err := f.db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", f.paymentIntentID).
		Updates(f.updates).Error; err != nil {
		return err
	}
	return f.err
}

func (f *recordingFulfiller) Fulfill(req FulfillmentRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return f.err
}

func (f *recordingFulfiller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newFulfillmentQueueTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(
		&models.ShopOrder{}, &models.ShopOrderItem{}, &models.ShopFulfillmentJob{},
		&models.ShopRefund{}, &models.ShopCompensationRefundJob{},
		&models.ShopCheckoutQuote{},
	); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return db
}

func createQueueTestOrder(t *testing.T, db *gorm.DB, paymentIntentID string) {
	t.Helper()
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: uuid.NewString(),
		PaymentIntentID: paymentIntentID, Status: "payment_pending",
		FinancialStatus: "pending", Currency: "HKD", TotalAmountMinor: 9900,
		CustomerName: "Alice", CustomerEmail: "alice@example.com",
		Items: []models.ShopOrderItem{{
			ID: uuid.NewString(), Source: string(SourceShopify), Handle: "cat-bed",
			VariantID: "gid://shopify/ProductVariant/1", Title: "Cat Bed",
			Quantity: 1, UnitAmountMinor: 9900, Currency: "HKD",
		}},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
}

func TestDurableFulfillmentQueueEnqueuesThenProcesses(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_success")
	downstream := &recordingFulfiller{}
	queue := NewDurableFulfillmentQueue(db, downstream)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	req := FulfillmentRequest{
		PaymentIntentID: "pi_queue_success", CustomerEmail: "alice@example.com",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "gid://shopify/ProductVariant/1",
			Quantity: 1,
		}},
	}
	if err := queue.Fulfill(req); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if downstream.count() != 0 {
		t.Fatal("downstream ran inside webhook enqueue")
	}

	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", req.PaymentIntentID).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if order.Status != "fulfillment_pending" || order.FinancialStatus != "paid" {
		t.Fatalf("unexpected paid order state: status=%q financial=%q", order.Status, order.FinancialStatus)
	}

	processed, err := queue.ProcessPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("process pending: %v", err)
	}
	if processed != 1 || downstream.count() != 1 {
		t.Fatalf("processed=%d downstream=%d, want 1/1", processed, downstream.count())
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", req.PaymentIntentID).Error; err != nil {
		t.Fatalf("load job: %v", err)
	}
	if job.Status != models.ShopFulfillmentJobCompleted || job.CompletedAt == nil {
		t.Fatalf("unexpected completed job: status=%q completedAt=%v", job.Status, job.CompletedAt)
	}

	shopifyOrderID := "gid://shopify/Order/1"
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", req.PaymentIntentID).
		Update("shopify_order_id", &shopifyOrderID).Error; err != nil {
		t.Fatalf("record Shopify order mapping: %v", err)
	}
	if err := queue.Fulfill(req); err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	processed, err = queue.ProcessPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("process duplicate: %v", err)
	}
	if processed != 0 || downstream.count() != 1 {
		t.Fatalf("duplicate processed=%d downstream=%d, want 0/1", processed, downstream.count())
	}
}

func TestDurableFulfillmentQueueRetriesFailure(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_retry")
	downstream := &recordingFulfiller{err: errors.New("temporary Shopify failure")}
	queue := NewDurableFulfillmentQueue(db, downstream)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_queue_retry",
		Items:           []FulfillmentItem{{Source: SourceShopify, VariantID: "variant", Quantity: 1}},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatalf("process: %v", err)
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", "pi_queue_retry").Error; err != nil {
		t.Fatalf("load job: %v", err)
	}
	if job.Status != models.ShopFulfillmentJobRetrying || job.Attempts != 1 {
		t.Fatalf("unexpected retry job: status=%q attempts=%d", job.Status, job.Attempts)
	}
	if !job.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("next attempt=%s, want %s", job.NextAttemptAt, now.Add(time.Minute))
	}
}

func TestDurableFulfillmentQueueFailureDoesNotRegressConcurrentRefund(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_retry_terminal")
	downstream := &terminalMutatingFulfiller{
		db: db, paymentIntentID: "pi_queue_retry_terminal",
		updates: map[string]any{
			"status": "refunded", "financial_status": "refunded",
			"failure_reason": "",
		},
		err: errors.New("Shopify response arrived after Stripe refund"),
	}
	queue := NewDurableFulfillmentQueue(db, downstream)
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_queue_retry_terminal",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_queue_retry_terminal").Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refunded" ||
		order.FinancialStatus != "refunded" ||
		order.FailureReason != "" {
		t.Fatalf("fulfillment retry regressed concurrent refund: %+v", order)
	}
}

func TestDurableFulfillmentQueueReconciliationDoesNotRegressConcurrentDispute(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_reconciliation_terminal")
	queue := NewDurableFulfillmentQueue(db, &recordingFulfiller{})
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_queue_reconciliation_terminal",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	job, err := queue.claimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", job.PaymentIntentID).
		Updates(map[string]any{
			"status": "payment_disputed", "financial_status": "disputed",
			"dispute_status": "needs_response", "dispute_id": "dp_queue_race",
			"failure_reason": "Stripe dispute requires review",
		}).Error; err != nil {
		t.Fatal(err)
	}
	queue.markClaimedReconciliationRequired(job, errors.New("stale Shopify ambiguity"))
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", job.PaymentIntentID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "payment_disputed" ||
		order.FinancialStatus != "disputed" ||
		order.FailureReason != "Stripe dispute requires review" {
		t.Fatalf("fulfillment reconciliation regressed concurrent dispute: %+v", order)
	}
}

func TestDurableFulfillmentQueueReconcilesPaidOrderWithoutJob(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_reconcile")
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_queue_reconcile").
		Updates(map[string]any{"status": "paid", "financial_status": "paid"}).Error; err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	queue := NewDurableFulfillmentQueue(db, &recordingFulfiller{})
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	count, err := queue.ReconcilePaidOrders(context.Background(), 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if count != 1 {
		t.Fatalf("reconciled=%d, want 1", count)
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", "pi_queue_reconcile").Error; err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	if job.Status != models.ShopFulfillmentJobPending {
		t.Fatalf("job status=%q, want pending", job.Status)
	}
}

func TestDurableFulfillmentQueueDoesNotReconcilePaidCanceledLatePayment(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_late_payment")
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_queue_late_payment").
		Updates(map[string]any{
			"status":             "canceled",
			"financial_status":   "paid",
			"fulfillment_status": "CANCELLED",
			"failure_reason":     "operator refund required",
		}).Error; err != nil {
		t.Fatalf("mark late payment canceled: %v", err)
	}
	downstream := &recordingFulfiller{}
	queue := NewDurableFulfillmentQueue(db, downstream)

	count, err := queue.ReconcilePaidOrders(context.Background(), 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if count != 0 {
		t.Fatalf("reconciled=%d, want 0", count)
	}
	var jobs int64
	if err := db.Model(&models.ShopFulfillmentJob{}).
		Where("payment_intent_id = ?", "pi_queue_late_payment").
		Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || downstream.count() != 0 {
		t.Fatalf("late paid cancellation created work: jobs=%d downstream=%d", jobs, downstream.count())
	}
}

func TestDurableFulfillmentQueueCancelsClaimWhenOrderBecomesIneligible(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_canceled_after_enqueue")
	downstream := &recordingFulfiller{}
	queue := NewDurableFulfillmentQueue(db, downstream)
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_queue_canceled_after_enqueue",
		Items:           []FulfillmentItem{{Source: SourceShopify, VariantID: "variant", Quantity: 1}},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_queue_canceled_after_enqueue").
		Updates(map[string]any{
			"status": "canceled", "financial_status": "paid",
			"fulfillment_status": "CANCELLED",
		}).Error; err != nil {
		t.Fatal(err)
	}

	processed, err := queue.ProcessPending(context.Background(), 1)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if processed != 1 || downstream.count() != 0 {
		t.Fatalf("processed=%d downstream=%d, want 1/0", processed, downstream.count())
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", "pi_queue_canceled_after_enqueue").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobCanceled {
		t.Fatalf("job status=%q, want canceled", job.Status)
	}
}

func TestDurableFulfillmentQueueSkipsActiveDisputeEvenIfFinancialStateIsStalePaid(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_active_dispute")
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_queue_active_dispute").
		Updates(map[string]any{
			"status": "paid", "financial_status": "paid",
			"dispute_status": "needs_response", "dispute_id": "dp_active",
		}).Error; err != nil {
		t.Fatal(err)
	}
	downstream := &recordingFulfiller{}
	queue := NewDurableFulfillmentQueue(db, downstream)

	count, err := queue.ReconcilePaidOrders(context.Background(), 10)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if count != 0 || downstream.count() != 0 {
		t.Fatalf("active dispute reconciled fulfillment: count=%d downstream=%d", count, downstream.count())
	}

	// A stale job created before the dispute must be stopped by the worker too.
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_queue_active_dispute").
		Updates(map[string]any{
			"dispute_status": "", "dispute_id": "",
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_queue_active_dispute",
		Items:           []FulfillmentItem{{Source: SourceShopify, VariantID: "variant", Quantity: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.ShopOrder{}).
		Where("payment_intent_id = ?", "pi_queue_active_dispute").
		Updates(map[string]any{
			"financial_status": "paid", "dispute_status": "needs_response",
			"dispute_id": "dp_active",
		}).Error; err != nil {
		t.Fatal(err)
	}
	processed, err := queue.ProcessPending(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 || downstream.count() != 0 {
		t.Fatalf("stale dispute job reached downstream: processed=%d downstream=%d", processed, downstream.count())
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", "pi_queue_active_dispute").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobCanceled {
		t.Fatalf("stale dispute job status=%q, want canceled", job.Status)
	}
}

func TestDurableFulfillmentQueueRenewsLeaseAndRejectsStaleWorkerCompletion(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_queue_lease")
	blocking := &blockingFulfiller{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	queue := NewDurableFulfillmentQueue(db, blocking)
	queue.lease = 300 * time.Millisecond
	if err := queue.Fulfill(FulfillmentRequest{PaymentIntentID: "pi_queue_lease"}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := queue.ProcessPending(context.Background(), 1)
		done <- err
	}()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}

	// Wait beyond the original lease. The heartbeat must keep the job owned so
	// another app instance cannot claim it while the external call is running.
	time.Sleep(450 * time.Millisecond)
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", "pi_queue_lease").Error; err != nil {
		t.Fatal(err)
	}
	if job.LeaseOwner == "" || job.LockedUntil == nil || !job.LockedUntil.After(time.Now().UTC()) {
		t.Fatalf("active worker lease was not renewed: %+v", job)
	}
	other := NewDurableFulfillmentQueue(db, &recordingFulfiller{})
	if _, err := other.claimNext(context.Background()); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("second worker claimed a live lease: %v", err)
	}

	close(blocking.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish")
	}
	if err := db.First(&job, "payment_intent_id = ?", "pi_queue_lease").Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobCompleted || job.LeaseOwner != "" {
		t.Fatalf("completed job retained lease ownership: %+v", job)
	}

	// A worker that loses ownership must not overwrite the newer owner's
	// status, even if its downstream call eventually returns.
	createQueueTestOrder(t, db, "pi_queue_stale_owner")
	staleQueue := NewDurableFulfillmentQueue(db, &recordingFulfiller{})
	if err := staleQueue.Fulfill(FulfillmentRequest{PaymentIntentID: "pi_queue_stale_owner"}); err != nil {
		t.Fatal(err)
	}
	staleJob, err := staleQueue.claimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Minute)
	if err := db.Model(&models.ShopFulfillmentJob{}).Where("id = ?", staleJob.ID).Updates(map[string]any{
		"lease_owner": "new-owner", "locked_until": &future,
	}).Error; err != nil {
		t.Fatal(err)
	}
	staleQueue.processClaimed(staleJob)
	job = models.ShopFulfillmentJob{}
	if err := db.First(&job, "id = ?", staleJob.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobProcessing || job.LeaseOwner != "new-owner" {
		t.Fatalf("stale worker overwrote current owner: %+v", job)
	}
}

func TestFulfillmentDispatchPostgresFenceLivesThroughExternalCall(t *testing.T) {
	state := &fulfillmentFenceDriverState{}
	driverName := "fulfillment-fence-" + uuid.NewString()
	sql.Register(driverName, &fulfillmentFenceDriver{state: state})
	sqlDB, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB}),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		t.Fatalf("open fake PostgreSQL database: %v", err)
	}
	queue := NewDurableFulfillmentQueue(db, &recordingFulfiller{})
	called := false
	err = queue.withFulfillmentDispatchFence("pi_postgres_fence_lifetime", func() error {
		called = true
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.txActive ||
			state.rollbacks != 0 ||
			!state.idleTimeoutDisabled {
			t.Fatalf(
				"advisory transaction was not safely held: active=%t rollbacks=%d idle-disabled=%t",
				state.txActive,
				state.rollbacks,
				state.idleTimeoutDisabled,
			)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run fenced call: %v", err)
	}
	if !called {
		t.Fatal("fenced external call was skipped")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.txActive || state.rollbacks != 1 {
		t.Fatalf(
			"advisory transaction was not released after call: active=%t rollbacks=%d",
			state.txActive,
			state.rollbacks,
		)
	}
}

func TestDurableFulfillmentQueueFencePreventsDuplicateExternalCallAfterLeaseSteal(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// SQLite shared-cache writes are intentionally serialized in this race
	// test; the concurrency under test is the worker dispatch fence, not the
	// SQLite driver's SQLITE_LOCKED behavior.
	sqlDB.SetMaxOpenConns(1)
	createQueueTestOrder(t, db, "pi_queue_fenced_lease_steal")
	downstream := &mappingBlockingFulfiller{
		db: db, started: make(chan struct{}), release: make(chan struct{}),
	}
	firstQueue := NewDurableFulfillmentQueue(db, downstream)
	secondQueue := NewDurableFulfillmentQueue(db, downstream)
	if err := firstQueue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_queue_fenced_lease_steal",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	firstJob, err := firstQueue.claimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		firstQueue.processClaimed(firstJob)
	}()
	select {
	case <-downstream.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not enter external order creation")
	}

	// Simulate a process pause longer than the DB lease: another worker claims
	// the same durable job while the first external call is still in flight.
	secondOwner := uuid.NewString()
	future := time.Now().UTC().Add(time.Minute)
	result := db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ?", firstJob.ID).
		Updates(map[string]any{
			"status":       models.ShopFulfillmentJobProcessing,
			"lease_owner":  secondOwner,
			"locked_until": &future,
			"attempts":     gorm.Expr("attempts + 1"),
		})
	if result.Error != nil || result.RowsAffected != 1 {
		t.Fatalf("steal lease: rows=%d err=%v", result.RowsAffected, result.Error)
	}
	var secondJob models.ShopFulfillmentJob
	if err := db.First(&secondJob, "id = ?", firstJob.ID).Error; err != nil {
		t.Fatal(err)
	}

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		secondQueue.processClaimed(&secondJob)
	}()
	time.Sleep(100 * time.Millisecond)
	if calls := downstream.count(); calls != 1 {
		t.Fatalf("second worker bypassed the in-flight dispatch fence: calls=%d", calls)
	}

	close(downstream.release)
	for name, done := range map[string]<-chan struct{}{
		"first":  firstDone,
		"second": secondDone,
	} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s worker did not finish", name)
		}
	}
	if calls := downstream.count(); calls != 1 {
		t.Fatalf("lease steal created duplicate Shopify calls: calls=%d", calls)
	}
	var finalJob models.ShopFulfillmentJob
	if err := db.First(&finalJob, "id = ?", firstJob.ID).Error; err != nil {
		t.Fatal(err)
	}
	if finalJob.Status == models.ShopFulfillmentJobRetrying {
		if err := db.Model(&finalJob).
			Update("next_attempt_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := secondQueue.ProcessPending(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		if err := db.First(&finalJob, "id = ?", firstJob.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if finalJob.Status != models.ShopFulfillmentJobCompleted ||
		finalJob.LeaseOwner != "" {
		t.Fatalf("new owner did not complete recovered mapping: %+v", finalJob)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", secondJob.PaymentIntentID).Error; err != nil {
		t.Fatal(err)
	}
	if order.ShopifyOrderGID() == "" {
		t.Fatal("external Shopify order mapping was not preserved")
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("fenced successful order created %d compensation refunds", refunds)
	}
}

func TestDurableFulfillmentQueueRecoversPaymentFailedAndFulfillmentFailedOrders(t *testing.T) {
	for _, staleStatus := range []string{"payment_failed", "fulfillment_failed"} {
		t.Run(staleStatus, func(t *testing.T) {
			db := newFulfillmentQueueTestDB(t)
			paymentIntentID := "pi_recover_" + staleStatus
			createQueueTestOrder(t, db, paymentIntentID)
			if err := db.Model(&models.ShopOrder{}).
				Where("payment_intent_id = ?", paymentIntentID).
				Updates(map[string]any{
					"status": staleStatus, "financial_status": "pending",
				}).Error; err != nil {
				t.Fatal(err)
			}
			downstream := &recordingFulfiller{}
			queue := NewDurableFulfillmentQueue(db, downstream)
			if err := queue.Fulfill(FulfillmentRequest{
				PaymentIntentID: paymentIntentID,
				Items: []FulfillmentItem{{
					Source: SourceShopify, VariantID: "variant", Quantity: 1,
				}},
			}); err != nil {
				t.Fatalf("recover enqueue: %v", err)
			}
			if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
				t.Fatal(err)
			}
			if downstream.count() != 1 {
				t.Fatalf("%s order did not reach fulfillment", staleStatus)
			}
			var order models.ShopOrder
			if err := db.First(&order, "payment_intent_id = ?", paymentIntentID).Error; err != nil {
				t.Fatal(err)
			}
			if order.FinancialStatus != "paid" ||
				order.Status != "fulfillment_pending" {
				t.Fatalf("%s order did not recover: %+v", staleStatus, order)
			}
		})
	}
}

func TestDurableFulfillmentQueueDeterministicRejectionQueuesOneCompensation(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_inventory_rejected")
	downstream := &reconcilingFulfiller{
		db: db,
		err: errors.Join(
			ErrFulfillmentOrderDefinitelyRejected,
			errors.New("inventory is no longer available"),
		),
	}
	queue := NewDurableFulfillmentQueue(db, downstream)
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_inventory_rejected",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_inventory_rejected").Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "canceled" ||
		order.FulfillmentStatus != "CANCELLED" ||
		order.FinancialStatus != "paid" {
		t.Fatalf("deterministic failure did not enter compensation state: %+v", order)
	}
	var refund models.ShopRefund
	if err := db.First(&refund, "order_id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Reason != models.ShopRefundReasonFulfillmentFailed ||
		refund.AmountMinor != order.TotalAmountMinor ||
		refund.Status != models.ShopRefundStatusPending {
		t.Fatalf("unexpected fulfillment compensation: %+v", refund)
	}
	var compensationJobs int64
	if err := db.Model(&models.ShopCompensationRefundJob{}).
		Where("refund_id = ?", refund.ID).Count(&compensationJobs).Error; err != nil {
		t.Fatal(err)
	}
	if compensationJobs != 1 {
		t.Fatalf("compensation jobs=%d, want 1", compensationJobs)
	}

	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: order.PaymentIntentID,
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 1 {
		t.Fatalf("duplicate fulfillment event created %d compensations", refunds)
	}
}

func TestDurableFulfillmentQueueAmbiguousOrderCreateIsNeverRetriedOrRefunded(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_order_create_response_lost")
	admin := &capturingShopifyOrderAdmin{
		createErr: errors.New("Shopify response body was lost after orderCreate"),
	}
	queue := NewDurableFulfillmentQueue(db, NewOrderDispatcher(db, admin))
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_order_create_response_lost",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if admin.createCalls != 1 || admin.lookupCalls != 2 {
		t.Fatalf(
			"ambiguous orderCreate was repeated: create=%d lookup=%d",
			admin.createCalls,
			admin.lookupCalls,
		)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_order_create_response_lost").Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "reconciliation_required" ||
		order.FinancialStatus != "paid" {
		t.Fatalf("ambiguous orderCreate did not fail closed: %+v", order)
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("ambiguous orderCreate created %d blind refunds", refunds)
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", order.PaymentIntentID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobFailed {
		t.Fatalf("ambiguous orderCreate job remained runnable: %+v", job)
	}
}

func TestDurableFulfillmentQueueCommitsDispatchMarkerImmediatelyBeforeOrderCreate(
	t *testing.T,
) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_dispatch_marker_ordering")
	markerObservedBeforeCreate := false
	admin := &capturingShopifyOrderAdmin{
		beforeCreate: func() {
			var job models.ShopFulfillmentJob
			if err := db.First(
				&job,
				"payment_intent_id = ?",
				"pi_dispatch_marker_ordering",
			).Error; err != nil {
				t.Fatalf("load job from Shopify CreateOrder hook: %v", err)
			}
			if job.DispatchStartedAt == nil {
				t.Fatal("Shopify CreateOrder ran before the dispatch marker was committed")
			}
			markerObservedBeforeCreate = true
		},
	}
	queue := NewDurableFulfillmentQueue(db, NewOrderDispatcher(db, admin))
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_dispatch_marker_ordering",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if !markerObservedBeforeCreate || admin.createCalls != 1 {
		t.Fatalf(
			"dispatch ordering was not exercised: marker=%t createCalls=%d",
			markerObservedBeforeCreate,
			admin.createCalls,
		)
	}
}

func TestDurableFulfillmentQueueRecoveredLeaseNeverRepeatsStartedOrderCreate(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_order_create_process_crash")
	downstream := &reconcilingFulfiller{
		db: db, reconcileFound: false,
		// Simulate the first process having already sent one orderCreate before
		// it died without persisting the Shopify response.
		requests: 1,
	}
	queue := NewDurableFulfillmentQueue(db, downstream)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_order_create_process_crash",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "gid://shopify/ProductVariant/1", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	firstJob, err := queue.claimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dispatchStartedAt := now
	expiredLease := now.Add(-time.Second)
	if err := db.Model(&models.ShopFulfillmentJob{}).
		Where("id = ?", firstJob.ID).
		Updates(map[string]any{
			"dispatch_started_at": &dispatchStartedAt,
			"locked_until":        &expiredLease,
		}).Error; err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	recoveredJob, err := queue.claimNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	queue.processClaimed(recoveredJob)
	if downstream.requests != 1 || downstream.reconciles != 1 {
		t.Fatalf(
			"recovered lease repeated orderCreate: create=%d lookup=%d",
			downstream.requests,
			downstream.reconciles,
		)
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "id = ?", firstJob.ID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobFailed {
		t.Fatalf("previously dispatched job remained automatic: %+v", job)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", job.PaymentIntentID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "reconciliation_required" ||
		order.FinancialStatus != "paid" {
		t.Fatalf("crash-window order did not fail closed: %+v", order)
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("crash-window order created %d blind refunds", refunds)
	}
}

func TestDurableFulfillmentQueueExhaustionRecoversResponseLossBeforeRefund(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_response_loss_recovered")
	downstream := &reconcilingFulfiller{
		db: db, err: errors.New("orderCreate response lost"),
		reconcileFound: true,
	}
	queue := NewDurableFulfillmentQueue(db, downstream)
	queue.maxAttempts = 1
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_response_loss_recovered",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_response_loss_recovered").Error; err != nil {
		t.Fatal(err)
	}
	if order.ShopifyOrderGID() == "" {
		t.Fatal("response-loss reconciliation did not persist the Shopify order")
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("recovered Shopify order was refunded %d times", refunds)
	}
	var job models.ShopFulfillmentJob
	if err := db.First(&job, "payment_intent_id = ?", order.PaymentIntentID).Error; err != nil {
		t.Fatal(err)
	}
	if job.Status != models.ShopFulfillmentJobCompleted ||
		downstream.reconciles != 1 {
		t.Fatalf("response-loss job not completed safely: %+v reconciles=%d", job, downstream.reconciles)
	}
}

func TestDurableFulfillmentQueueExhaustionQueryAmbiguityDoesNotRefund(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_query_ambiguous")
	downstream := &reconcilingFulfiller{
		db: db, err: errors.New("orderCreate timeout"),
		reconcileErr: errors.New("Shopify lookup timeout"),
	}
	queue := NewDurableFulfillmentQueue(db, downstream)
	queue.maxAttempts = 1
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_query_ambiguous",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_query_ambiguous").Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "reconciliation_required" ||
		order.FinancialStatus != "paid" {
		t.Fatalf("ambiguous lookup did not fail closed: %+v", order)
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if refunds != 0 {
		t.Fatalf("ambiguous Shopify state created %d blind refunds", refunds)
	}
}

func TestDurableFulfillmentQueueExhaustionSourceLookupMissRequiresReconciliation(t *testing.T) {
	db := newFulfillmentQueueTestDB(t)
	createQueueTestOrder(t, db, "pi_exhausted_absent")
	downstream := &reconcilingFulfiller{
		db: db, err: errors.New("Shopify unavailable"),
		reconcileFound: false,
	}
	queue := NewDurableFulfillmentQueue(db, downstream)
	queue.maxAttempts = 1
	if err := queue.Fulfill(FulfillmentRequest{
		PaymentIntentID: "pi_exhausted_absent",
		Items: []FulfillmentItem{{
			Source: SourceShopify, VariantID: "variant", Quantity: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	var order models.ShopOrder
	if err := db.First(&order, "payment_intent_id = ?", "pi_exhausted_absent").Error; err != nil {
		t.Fatal(err)
	}
	var refunds int64
	if err := db.Model(&models.ShopRefund{}).
		Where("order_id = ?", order.ID).Count(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "reconciliation_required" ||
		refunds != 0 ||
		downstream.reconciles != 1 {
		t.Fatalf(
			"ambiguous source lookup miss did not fail closed: order=%+v refunds=%d reconciles=%d",
			order,
			refunds,
			downstream.reconciles,
		)
	}
}
