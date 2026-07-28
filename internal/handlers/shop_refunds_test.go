package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeStripeRefunder struct {
	calls     []payments.CreateRefundRequest
	results   []*payments.CreateRefundResponse
	errs      []error
	afterCall func(payments.CreateRefundRequest)
}

type recordingRefundMirrorEnqueuer struct {
	refundIDs []string
	err       error
}

func (r *recordingRefundMirrorEnqueuer) EnqueueRefundMirror(
	_ context.Context,
	refundID string,
) error {
	r.refundIDs = append(r.refundIDs, refundID)
	return r.err
}

func (f *fakeStripeRefunder) CreateRefund(_ context.Context, req payments.CreateRefundRequest) (*payments.CreateRefundResponse, error) {
	f.calls = append(f.calls, req)
	if f.afterCall != nil {
		f.afterCall(req)
	}
	index := len(f.calls) - 1
	if index < len(f.errs) && f.errs[index] != nil {
		return nil, f.errs[index]
	}
	if index < len(f.results) {
		return f.results[index], nil
	}
	return &payments.CreateRefundResponse{RefundID: "re_default", Status: "succeeded"}, nil
}

func newShopRefundTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ShopOrder{}, &models.ShopRefund{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func createRefundableShopOrder(t *testing.T, db *gorm.DB, total int64) models.ShopOrder {
	t.Helper()
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_" + uuid.NewString(),
		Status: "return_open", FinancialStatus: "paid", FulfillmentStatus: "DELIVERED",
		Currency: "HKD", TotalAmountMinor: total, ReturnID: "gid://shopify/Return/1",
		ReturnStatus: "OPEN",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	return order
}

func shopRefundHTTPRequest(t *testing.T, orderID, idempotencyKey string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/shop/orders/"+orderID+"/refund", strings.NewReader(string(raw)))
	req.SetPathValue("orderID", orderID)
	req.Header.Set("X-Shop-Admin-Key", "admin-secret")
	req.Header.Set("X-Shop-Admin-Actor", "operator@example.com")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	return req
}

func TestShopRefundCreatesPartialThenFullRefundWithoutOverwritingPartialLifecycle(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{results: []*payments.CreateRefundResponse{
		{RefundID: "re_partial", Status: "succeeded"},
		{RefundID: "re_full", Status: "succeeded"},
	}}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")

	first := shopRefundHTTPRequest(t, order.ID, "refund-partial", map[string]any{
		"amountMinor": 400, "reason": "requested_by_customer", "confirmed": true,
	})
	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, first)
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", firstRec.Code, firstRec.Body.String())
	}

	var updated models.ShopOrder
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.RefundedAmountMinor != 400 || updated.FinancialStatus != "partially_refunded" {
		t.Fatalf("partial refund state was not applied: %+v", updated)
	}
	if updated.Status != "return_open" {
		t.Fatalf("partial refund must preserve lifecycle status, got %q", updated.Status)
	}

	second := shopRefundHTTPRequest(t, order.ID, "refund-rest", map[string]any{
		"amountMinor": 600, "reason": "requested_by_customer", "confirmed": true,
	})
	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, second)
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", secondRec.Code, secondRec.Body.String())
	}
	if err := db.First(&updated, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.RefundedAmountMinor != 1000 || updated.FinancialStatus != "refunded" || updated.Status != "refunded" {
		t.Fatalf("full refund state was not applied: %+v", updated)
	}
	if len(refunder.calls) != 2 || refunder.calls[0].IdempotencyKey != "refund-partial" {
		t.Fatalf("Stripe calls did not receive stable idempotency keys: %+v", refunder.calls)
	}
}

func TestShopRefundQueuesShopifyMirrorOnlyAfterStripeSucceeded(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{results: []*payments.CreateRefundResponse{{
		RefundID: "re_mirror", Status: "succeeded",
	}}}
	mirror := &recordingRefundMirrorEnqueuer{}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret", mirror)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shopRefundHTTPRequest(t, order.ID, "refund-mirror", map[string]any{
		"amountMinor": 400, "reason": "requested_by_customer", "confirmed": true,
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(mirror.refundIDs) != 1 {
		t.Fatalf("mirror enqueue calls = %d, want 1", len(mirror.refundIDs))
	}
	var refund models.ShopRefund
	if err := db.First(&refund, "id = ?", mirror.refundIDs[0]).Error; err != nil {
		t.Fatalf("load refund: %v", err)
	}
	if refund.Status != models.ShopRefundStatusSucceeded ||
		refund.ShopifyMirrorStatus != models.ShopRefundMirrorStatusPending {
		t.Fatalf("refund was queued before local Stripe success: %+v", refund)
	}
}

func TestShopRefundAllowsOperatorConfirmedCanceledOrder(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: "pi_canceled_refund",
		Status: "canceled", FinancialStatus: "paid", FulfillmentStatus: "CANCELLED",
		Currency: "HKD", TotalAmountMinor: 800,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	refunder := &fakeStripeRefunder{results: []*payments.CreateRefundResponse{{
		RefundID: "re_canceled", Status: "succeeded",
	}}}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shopRefundHTTPRequest(t, order.ID, "refund-canceled", map[string]any{
		"amountMinor": 800, "reason": "requested_by_customer", "confirmed": true,
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected canceled order refund 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(refunder.calls) != 1 {
		t.Fatalf("Stripe refund calls=%d, want 1", len(refunder.calls))
	}
}

func TestShopRefundIsIdempotentAndRejectsOverRefund(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{results: []*payments.CreateRefundResponse{
		{RefundID: "re_partial", Status: "succeeded"},
	}}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")
	body := map[string]any{"amountMinor": 700, "reason": "requested_by_customer", "confirmed": true}

	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, shopRefundHTTPRequest(t, order.ID, "same-key", body))
	if firstRec.Code != http.StatusCreated {
		t.Fatalf("expected first request 201, got %d: %s", firstRec.Code, firstRec.Body.String())
	}
	replayRec := httptest.NewRecorder()
	handler.ServeHTTP(replayRec, shopRefundHTTPRequest(t, order.ID, "same-key", body))
	if replayRec.Code != http.StatusOK {
		t.Fatalf("expected replay 200, got %d: %s", replayRec.Code, replayRec.Body.String())
	}
	if len(refunder.calls) != 1 {
		t.Fatalf("idempotent replay called Stripe %d times", len(refunder.calls))
	}

	overRec := httptest.NewRecorder()
	handler.ServeHTTP(overRec, shopRefundHTTPRequest(t, order.ID, "over-key", map[string]any{
		"amountMinor": 301, "reason": "requested_by_customer", "confirmed": true,
	}))
	if overRec.Code != http.StatusConflict {
		t.Fatalf("expected over-refund 409, got %d: %s", overRec.Code, overRec.Body.String())
	}
	if len(refunder.calls) != 1 {
		t.Fatal("over-refund must be rejected before Stripe is called")
	}

	conflictRec := httptest.NewRecorder()
	handler.ServeHTTP(conflictRec, shopRefundHTTPRequest(t, order.ID, "same-key", map[string]any{
		"amountMinor": 200, "reason": "requested_by_customer", "confirmed": true,
	}))
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("expected changed idempotent request 409, got %d", conflictRec.Code)
	}
}

func TestShopRefundPendingAmountIsReserved(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{results: []*payments.CreateRefundResponse{
		{RefundID: "re_pending", Status: "pending"},
	}}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")

	pendingRec := httptest.NewRecorder()
	handler.ServeHTTP(pendingRec, shopRefundHTTPRequest(t, order.ID, "pending-key", map[string]any{
		"amountMinor": 800, "reason": "requested_by_customer", "confirmed": true,
	}))
	if pendingRec.Code != http.StatusAccepted {
		t.Fatalf("expected pending refund 202, got %d: %s", pendingRec.Code, pendingRec.Body.String())
	}
	overRec := httptest.NewRecorder()
	handler.ServeHTTP(overRec, shopRefundHTTPRequest(t, order.ID, "second-key", map[string]any{
		"amountMinor": 201, "reason": "requested_by_customer", "confirmed": true,
	}))
	if overRec.Code != http.StatusConflict {
		t.Fatalf("expected reserved amount to prevent over-refund, got %d", overRec.Code)
	}
	if len(refunder.calls) != 1 {
		t.Fatal("Stripe was called despite a reserved pending refund")
	}
}

func TestShopRefundRejectsOrderWithActiveDispute(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	if err := db.Model(&order).Updates(map[string]any{
		"financial_status": "disputed",
		"dispute_status":   "needs_response",
	}).Error; err != nil {
		t.Fatal(err)
	}
	refunder := &fakeStripeRefunder{}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shopRefundHTTPRequest(t, order.ID, "disputed-key", map[string]any{
		"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true,
	}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("active dispute refund status=%d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(refunder.calls) != 0 {
		t.Fatal("active dispute reached Stripe refund API")
	}
}

func TestShopRefundRequiresAdminConfirmationApprovedReturnAndIdempotency(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	if err := db.Model(&order).Update("return_status", "REQUESTED").Error; err != nil {
		t.Fatal(err)
	}
	refunder := &fakeStripeRefunder{}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")

	unapprovedRec := httptest.NewRecorder()
	handler.ServeHTTP(unapprovedRec, shopRefundHTTPRequest(t, order.ID, "unapproved", map[string]any{
		"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true,
	}))
	if unapprovedRec.Code != http.StatusConflict {
		t.Fatalf("expected unapproved return 409, got %d", unapprovedRec.Code)
	}

	noConfirmation := shopRefundHTTPRequest(t, order.ID, "not-confirmed", map[string]any{
		"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": false,
	})
	noConfirmationRec := httptest.NewRecorder()
	handler.ServeHTTP(noConfirmationRec, noConfirmation)
	if noConfirmationRec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing confirmation 400, got %d", noConfirmationRec.Code)
	}

	noKey := shopRefundHTTPRequest(t, order.ID, "unused", map[string]any{
		"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true,
	})
	noKey.Header.Del("Idempotency-Key")
	noKeyRec := httptest.NewRecorder()
	handler.ServeHTTP(noKeyRec, noKey)
	if noKeyRec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing idempotency key 400, got %d", noKeyRec.Code)
	}

	unauthorized := shopRefundHTTPRequest(t, order.ID, "unauthorized", map[string]any{
		"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true,
	})
	unauthorized.Header.Set("X-Shop-Admin-Key", "wrong")
	unauthorizedRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized 401, got %d", unauthorizedRec.Code)
	}
	if len(refunder.calls) != 0 {
		t.Fatal("invalid refund requests must not call Stripe")
	}
}

func TestShopRefundNetworkFailureCanRetrySameStripeIdempotencyKey(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{
		errs: []error{errors.New("timeout"), nil},
		results: []*payments.CreateRefundResponse{
			nil,
			{RefundID: "re_after_timeout", Status: "succeeded"},
		},
	}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")
	body := map[string]any{"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true}

	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, shopRefundHTTPRequest(t, order.ID, "retry-key", body))
	if firstRec.Code != http.StatusBadGateway {
		t.Fatalf("expected first request 502, got %d", firstRec.Code)
	}
	var pending models.ShopRefund
	if err := db.First(&pending, "idempotency_key = ?", "retry-key").Error; err != nil {
		t.Fatal(err)
	}
	if pending.Status != models.ShopRefundStatusPending || pending.StripeStatus != "request_unknown" {
		t.Fatalf("ambiguous refund status=(%s,%s), want pending/request_unknown", pending.Status, pending.StripeStatus)
	}
	safeRetryStart := time.Now().UTC().Add(-22 * time.Hour)
	if err := db.Model(&pending).
		Update("stripe_first_submitted_at", &safeRetryStart).Error; err != nil {
		t.Fatal(err)
	}

	// The timed-out call might already have created a Stripe refund. A new key
	// must not bypass the reservation while that outcome is unknown.
	otherKeyRec := httptest.NewRecorder()
	handler.ServeHTTP(otherKeyRec, shopRefundHTTPRequest(t, order.ID, "different-key", body))
	if otherKeyRec.Code != http.StatusConflict {
		t.Fatalf("expected ambiguous reservation to reject a new key, got %d: %s", otherKeyRec.Code, otherKeyRec.Body.String())
	}
	if len(refunder.calls) != 1 {
		t.Fatalf("new key called Stripe while the original result was unknown: %d calls", len(refunder.calls))
	}

	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, shopRefundHTTPRequest(t, order.ID, "retry-key", body))
	if secondRec.Code != http.StatusCreated {
		t.Fatalf("expected retry 201, got %d: %s", secondRec.Code, secondRec.Body.String())
	}
	if len(refunder.calls) != 2 ||
		refunder.calls[0].IdempotencyKey != refunder.calls[1].IdempotencyKey {
		t.Fatalf("network retry did not reuse the Stripe idempotency key: %+v", refunder.calls)
	}
}

func TestShopRefundRefusesRetryAtStripeIdempotencyRetentionBoundary(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{
		errs: []error{errors.New("Stripe response lost")},
	}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")
	body := map[string]any{
		"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true,
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, shopRefundHTTPRequest(t, order.ID, "aged-retry-key", body))
	if first.Code != http.StatusBadGateway {
		t.Fatalf("first response=%d, want 502", first.Code)
	}
	var refund models.ShopRefund
	if err := db.First(&refund, "idempotency_key = ?", "aged-retry-key").Error; err != nil {
		t.Fatal(err)
	}
	retentionBoundary := time.Now().UTC().Add(-payments.StripeRefundIdempotencyRetryWindow)
	if err := db.Model(&refund).
		Update("stripe_first_submitted_at", &retentionBoundary).Error; err != nil {
		t.Fatal(err)
	}

	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, shopRefundHTTPRequest(t, order.ID, "aged-retry-key", body))
	if retry.Code != http.StatusConflict {
		t.Fatalf("aged retry response=%d, want 409: %s", retry.Code, retry.Body.String())
	}
	if len(refunder.calls) != 1 {
		t.Fatalf("aged Stripe idempotency key was replayed: calls=%d", len(refunder.calls))
	}
	if err := db.First(&refund, "id = ?", refund.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusPending ||
		refund.StripeStatus != "request_unknown" {
		t.Fatalf("aged ambiguous reservation was released or changed: %+v", refund)
	}
}

func TestShopRefundTransportErrorDoesNotRegressWebhookSuccess(t *testing.T) {
	db := newShopRefundTestDB(t)
	order := createRefundableShopOrder(t, db, 1000)
	refunder := &fakeStripeRefunder{
		errs: []error{errors.New("response lost after Stripe accepted refund")},
	}
	refunder.afterCall = func(request payments.CreateRefundRequest) {
		now := time.Now().UTC()
		stripeRefundID := "re_webhook_before_transport_error"
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.ShopRefund{}).
				Where("id = ?", request.PawrdRefundID).
				Updates(map[string]any{
					"stripe_refund_id": &stripeRefundID,
					"stripe_status":    "succeeded",
					"status":           models.ShopRefundStatusSucceeded,
					"failure_reason":   "",
					"completed_at":     &now,
				}).Error; err != nil {
				return err
			}
			return tx.Model(&models.ShopOrder{}).
				Where("id = ?", request.PawrdOrderID).
				Updates(map[string]any{
					"status":                "refunded",
					"financial_status":      "refunded",
					"refunded_amount_minor": request.AmountMinor,
				}).Error
		}); err != nil {
			t.Fatalf("commit webhook success: %v", err)
		}
	}
	handler := NewShopOrderRefundHandler(db, refunder, "admin-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, shopRefundHTTPRequest(
		t,
		order.ID,
		"webhook-wins-transport-error",
		map[string]any{
			"amountMinor": 1000, "reason": "requested_by_customer", "confirmed": true,
		},
	))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("transport response=%d, want 502", rec.Code)
	}
	var refund models.ShopRefund
	if err := db.First(&refund, "idempotency_key = ?", "webhook-wins-transport-error").Error; err != nil {
		t.Fatal(err)
	}
	if refund.Status != models.ShopRefundStatusSucceeded ||
		refund.StripeStatus != "succeeded" ||
		refund.StripeRefundID == nil ||
		*refund.StripeRefundID != "re_webhook_before_transport_error" {
		t.Fatalf("transport error regressed webhook success: %+v", refund)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != "refunded" ||
		order.FinancialStatus != "refunded" ||
		order.RefundedAmountMinor != order.TotalAmountMinor {
		t.Fatalf("transport error regressed refunded order: %+v", order)
	}
}
