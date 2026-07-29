package payments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeRefundMirrorClient struct {
	mu       sync.Mutex
	calls    []shopify.AdminExternalRefundInput
	failures []error
	result   *shopify.AdminExternalRefundResult
}

type blockingRefundMirrorClient struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingRefundMirrorClient) RecordExternalRefund(
	_ context.Context,
	input shopify.AdminExternalRefundInput,
) (*shopify.AdminExternalRefundResult, error) {
	close(b.started)
	<-b.release
	return &shopify.AdminExternalRefundResult{
		RefundID:      "gid://shopify/Refund/blocked",
		TransactionID: "gid://shopify/OrderTransaction/blocked",
		AmountMinor:   input.AmountMinor,
		Currency:      input.Currency,
	}, nil
}

func (f *fakeRefundMirrorClient) RecordExternalRefund(
	_ context.Context,
	input shopify.AdminExternalRefundInput,
) (*shopify.AdminExternalRefundResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, input)
	index := len(f.calls) - 1
	if index < len(f.failures) && f.failures[index] != nil {
		return nil, f.failures[index]
	}
	if f.result != nil {
		copy := *f.result
		return &copy, nil
	}
	return &shopify.AdminExternalRefundResult{
		RefundID:      "gid://shopify/Refund/20",
		TransactionID: "gid://shopify/OrderTransaction/21",
		AmountMinor:   input.AmountMinor,
		Currency:      input.Currency,
	}, nil
}

func (f *fakeRefundMirrorClient) snapshotCalls() []shopify.AdminExternalRefundInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]shopify.AdminExternalRefundInput(nil), f.calls...)
}

func newRefundMirrorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&models.ShopOrder{},
		&models.ShopRefund{},
		&models.ShopRefundMirrorJob{},
	); err != nil {
		t.Fatalf("migrate refund mirror schema: %v", err)
	}
	return db
}

func seedSucceededRefund(
	t *testing.T,
	db *gorm.DB,
	withShopifyOrder bool,
) (models.ShopOrder, models.ShopRefund) {
	t.Helper()
	orderID := uuid.NewString()
	paymentIntentID := "pi_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	var shopifyOrderID *string
	if withShopifyOrder {
		value := "gid://shopify/Order/1002"
		shopifyOrderID = &value
	}
	order := models.ShopOrder{
		ID: orderID, UserID: uuid.NewString(), PaymentIntentID: testStringPointer(paymentIntentID),
		ShopifyOrderID: shopifyOrderID, Status: "refunded",
		FinancialStatus: "partially_refunded", Currency: "HKD",
		TotalAmountMinor: 8500, RefundedAmountMinor: 1234,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	stripeRefundID := "re_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	refund := models.ShopRefund{
		ID: uuid.NewString(), OrderID: order.ID, PaymentIntentID: paymentIntentID,
		StripeRefundID: &stripeRefundID, IdempotencyKey: uuid.NewString(),
		AmountMinor: 1234, Currency: "HKD", Reason: "requested_by_customer",
		Status: models.ShopRefundStatusSucceeded, StripeStatus: "succeeded",
		RequestedBy: "test",
	}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatalf("create refund: %v", err)
	}
	return order, refund
}

func TestRefundMirrorQueueDuplicateEnqueueCreatesOneJob(t *testing.T) {
	db := newRefundMirrorTestDB(t)
	_, refund := seedSucceededRefund(t, db, true)
	queue := NewDurableRefundMirrorQueue(db, &fakeRefundMirrorClient{})

	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}

	var count int64
	if err := db.Model(&models.ShopRefundMirrorJob{}).
		Where("refund_id = ?", refund.ID).Count(&count).Error; err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("jobs = %d, want 1", count)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatalf("reload refund: %v", err)
	}
	if refund.ShopifyMirrorStatus != models.ShopRefundMirrorStatusPending {
		t.Fatalf("mirror status = %q, want pending", refund.ShopifyMirrorStatus)
	}
}

func TestRefundMirrorQueueRetriesTransportAmbiguityWithSameKey(t *testing.T) {
	db := newRefundMirrorTestDB(t)
	_, refund := seedSucceededRefund(t, db, true)
	client := &fakeRefundMirrorClient{failures: []error{errors.New("response lost")}}
	queue := NewDurableRefundMirrorQueue(db, client)
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("first process = %d, %v", processed, err)
	}
	var job models.ShopRefundMirrorJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatalf("load retry job: %v", err)
	}
	if job.Status != models.ShopRefundMirrorJobRetrying {
		t.Fatalf("job status = %q, want retrying", job.Status)
	}

	now = now.Add(2 * time.Minute)
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("second process = %d, %v", processed, err)
	}
	calls := client.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("Shopify calls = %d, want 2", len(calls))
	}
	if calls[0].IdempotencyKey != refund.ID || calls[1].IdempotencyKey != refund.ID {
		t.Fatalf("retry changed idempotency key: %#v", calls)
	}
	if calls[0].AmountMinor != refund.AmountMinor ||
		calls[1].AmountMinor != refund.AmountMinor ||
		calls[0].Currency != "HKD" ||
		calls[1].Currency != "HKD" {
		t.Fatalf("retry changed Stripe money: %#v", calls)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatalf("reload mirrored refund: %v", err)
	}
	if refund.ShopifyMirrorStatus != models.ShopRefundMirrorStatusSucceeded ||
		refund.ShopifyRefundID == nil ||
		*refund.ShopifyRefundID != "gid://shopify/Refund/20" {
		t.Fatalf("unexpected mirrored refund: %#v", refund)
	}
}

func TestRefundMirrorQueueRenewsLeaseDuringShopifyCall(t *testing.T) {
	db := newRefundMirrorTestDB(t)
	_, refund := seedSucceededRefund(t, db, true)
	client := &blockingRefundMirrorClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	queue := NewDurableRefundMirrorQueue(db, client)
	queue.lease = 300 * time.Millisecond
	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := queue.ProcessPending(context.Background(), 1)
		done <- err
	}()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Shopify call did not start")
	}
	var initial models.ShopRefundMirrorJob
	if err := db.First(&initial, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatalf("load initial lease: %v", err)
	}
	if initial.LockedUntil == nil {
		t.Fatal("claimed job has no lease")
	}
	time.Sleep(450 * time.Millisecond)
	var renewed models.ShopRefundMirrorJob
	if err := db.First(&renewed, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatalf("load renewed lease: %v", err)
	}
	if renewed.LockedUntil == nil || !renewed.LockedUntil.After(*initial.LockedUntil) {
		t.Fatalf("lease was not renewed: initial=%v renewed=%v", initial.LockedUntil, renewed.LockedUntil)
	}
	close(client.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not finish after Shopify call")
	}
}

func TestRefundMirrorQueueRetriesUntilShopifyOrderIsMapped(t *testing.T) {
	db := newRefundMirrorTestDB(t)
	order, refund := seedSucceededRefund(t, db, false)
	client := &fakeRefundMirrorClient{}
	queue := NewDurableRefundMirrorQueue(db, client)
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }

	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatalf("process without mapping: %v", err)
	}
	if got := len(client.snapshotCalls()); got != 0 {
		t.Fatalf("Shopify was called before order mapping: %d", got)
	}

	shopifyOrderID := "gid://shopify/Order/1002"
	if err := db.Model(&order).Update("shopify_order_id", &shopifyOrderID).Error; err != nil {
		t.Fatalf("map Shopify order: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("process after mapping = %d, %v", processed, err)
	}
	if got := len(client.snapshotCalls()); got != 1 {
		t.Fatalf("Shopify calls after mapping = %d, want 1", got)
	}
}

func TestRefundMirrorQueueCompletesLatePaymentRefundAsNotApplicable(t *testing.T) {
	db := newRefundMirrorTestDB(t)
	order, refund := seedSucceededRefund(t, db, false)
	if err := db.Model(&order).Updates(map[string]any{
		"status":             "canceled",
		"financial_status":   "paid",
		"fulfillment_status": "CANCELLED",
		"failure_reason":     "Payment completed after the Shopify quote expired; operator refund required",
	}).Error; err != nil {
		t.Fatalf("mark late payment canceled: %v", err)
	}
	client := &fakeRefundMirrorClient{}
	queue := NewDurableRefundMirrorQueue(db, client)

	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if processed, err := queue.ProcessPending(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("process = %d, %v", processed, err)
	}
	if got := len(client.snapshotCalls()); got != 0 {
		t.Fatalf("Shopify called for an order that intentionally does not exist: %d", got)
	}
	var job models.ShopRefundMirrorJob
	if err := db.First(&job, "refund_id = ?", refund.ID).Error; err != nil {
		t.Fatalf("load job: %v", err)
	}
	if job.Status != models.ShopRefundMirrorJobCompleted || job.LastError != "" {
		t.Fatalf("late-payment mirror job should complete cleanly: %+v", job)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatalf("reload refund: %v", err)
	}
	if refund.ShopifyMirrorStatus != models.ShopRefundMirrorStatusNotApplicable ||
		refund.ShopifyMirrorError != "" {
		t.Fatalf("late-payment refund should be not_applicable: %+v", refund)
	}
}

func TestRefundMirrorQueueRejectsAmountMismatch(t *testing.T) {
	db := newRefundMirrorTestDB(t)
	_, refund := seedSucceededRefund(t, db, true)
	client := &fakeRefundMirrorClient{result: &shopify.AdminExternalRefundResult{
		RefundID:      "gid://shopify/Refund/20",
		TransactionID: "gid://shopify/OrderTransaction/21",
		AmountMinor:   1233,
		Currency:      "HKD",
	}}
	queue := NewDurableRefundMirrorQueue(db, client)

	if err := queue.EnqueueRefundMirror(context.Background(), refund.ID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := queue.ProcessPending(context.Background(), 1); err != nil {
		t.Fatalf("process mismatch: %v", err)
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatalf("reload refund: %v", err)
	}
	if refund.ShopifyMirrorStatus != models.ShopRefundMirrorStatusRetrying ||
		!strings.Contains(refund.ShopifyMirrorError, "amount or currency") {
		t.Fatalf("mismatch was not retained for retry: %#v", refund)
	}
}
