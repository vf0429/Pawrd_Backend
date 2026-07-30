package payments

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeFulfillmentRequester struct {
	calls  int
	result *shopify.AdminFulfillmentRequestResult
	err    error
}

func (f *fakeFulfillmentRequester) RequestOrderFulfillment(context.Context, string) (*shopify.AdminFulfillmentRequestResult, error) {
	f.calls++
	if f.result != nil || f.err != nil {
		return f.result, f.err
	}
	return &shopify.AdminFulfillmentRequestResult{
		Requested: []shopify.AdminFulfillmentRequestItem{{RequestStatus: "SUBMITTED"}},
	}, nil
}

func TestAutoRequestingFulfillerDoesNotRequestWhenDefaultOff(t *testing.T) {
	downstream := &recordingFulfiller{}
	requester := &fakeFulfillmentRequester{}
	fulfiller := NewAutoRequestingFulfiller(downstream, nil, requester, false)
	if err := fulfiller.Fulfill(FulfillmentRequest{PaymentIntentID: "pi_auto_off"}); err != nil {
		t.Fatal(err)
	}
	if downstream.count() != 1 || requester.calls != 0 {
		t.Fatalf("downstream=%d requester=%d, want 1/0", downstream.count(), requester.calls)
	}
}

func newAutoFulfillmentTestDB(t *testing.T) *gorm.DB {
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

func TestAutoRequestingFulfillerRequestsOnlyEligiblePaidOrder(t *testing.T) {
	db := newAutoFulfillmentTestDB(t)
	shopifyOrderID := "gid://shopify/Order/eligible"
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: testStringPointer("pi_auto_on"),
		ShopifyOrderID: &shopifyOrderID, Status: "processing", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	downstream := &recordingFulfiller{}
	requester := &fakeFulfillmentRequester{}
	fulfiller := NewAutoRequestingFulfiller(downstream, db, requester, true)
	if err := fulfiller.Fulfill(FulfillmentRequest{PaymentIntentID: order.PaymentIntentIDValue()}); err != nil {
		t.Fatal(err)
	}
	if requester.calls != 1 {
		t.Fatalf("requester calls=%d, want 1", requester.calls)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FulfillmentRequestStatus != "submitted" || order.FulfillmentRequestedAt == nil {
		t.Fatalf("fulfillment request audit state not stored: %+v", order)
	}
}

func TestFulfillmentRequestGateRejectsCanceledRefundedDisputedReturningAndPendingRefundOrders(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*models.ShopOrder)
		refund bool
	}{
		{name: "canceled", mutate: func(order *models.ShopOrder) { order.Status = "canceled" }},
		{name: "cancellation requested", mutate: func(order *models.ShopOrder) {
			order.Status = "cancellation_requested"
			order.ReturnStatus = "CANCELLATION_REQUESTED"
		}},
		{name: "refunded", mutate: func(order *models.ShopOrder) {
			order.FinancialStatus = "refunded"
			order.RefundedAmountMinor = order.TotalAmountMinor
		}},
		{name: "disputed", mutate: func(order *models.ShopOrder) {
			order.FinancialStatus = "disputed"
			order.DisputeStatus = "needs_response"
		}},
		{name: "return", mutate: func(order *models.ShopOrder) { order.ReturnStatus = "REQUESTED" }},
		{name: "pending refund", mutate: func(_ *models.ShopOrder) {}, refund: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newAutoFulfillmentTestDB(t)
			shopifyOrderID := "gid://shopify/Order/blocked"
			order := models.ShopOrder{
				ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: testStringPointer("pi_" + uuid.NewString()),
				ShopifyOrderID: &shopifyOrderID, Status: "processing", FinancialStatus: "paid",
				Currency: "HKD", TotalAmountMinor: 1000,
			}
			test.mutate(&order)
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			if test.refund {
				refund := models.ShopRefund{
					ID: uuid.NewString(), OrderID: order.ID, PaymentIntentID: order.PaymentIntentIDValue(),
					IdempotencyKey: uuid.NewString(), AmountMinor: 1000, Currency: "HKD",
					Reason: "requested_by_customer", Status: models.ShopRefundStatusPending,
					RequestedBy: "operator",
				}
				if err := db.Create(&refund).Error; err != nil {
					t.Fatal(err)
				}
			}
			requester := &fakeFulfillmentRequester{}
			if _, err := RequestOrderFulfillmentIfEligible(
				context.Background(),
				db,
				requester,
				order.ID,
			); err == nil {
				t.Fatal("ineligible order was submitted to fulfillment")
			}
			if requester.calls != 0 {
				t.Fatalf("ineligible order reached requester %d times", requester.calls)
			}
		})
	}
}

func TestFulfillmentRequestGateMarksMixedRequestedAndSkippedAsReconciliationRequired(t *testing.T) {
	db := newAutoFulfillmentTestDB(t)
	shopifyOrderID := "gid://shopify/Order/mixed"
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: testStringPointer("pi_mixed_result"),
		ShopifyOrderID: &shopifyOrderID, Status: "processing", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	requester := &fakeFulfillmentRequester{
		result: &shopify.AdminFulfillmentRequestResult{
			Requested: []shopify.AdminFulfillmentRequestItem{{
				FulfillmentOrderID: "gid://shopify/FulfillmentOrder/requested",
				RequestStatus:      "SUBMITTED",
			}},
			Skipped: []shopify.AdminFulfillmentRequestItem{{
				FulfillmentOrderID: "gid://shopify/FulfillmentOrder/skipped",
				Status:             "OPEN",
				RequestStatus:      "UNSUBMITTED",
				SkipReason:         "assigned fulfillment service is not explicitly identified as DSers",
			}},
		},
	}

	result, err := RequestOrderFulfillmentIfEligible(
		context.Background(),
		db,
		requester,
		order.ID,
	)
	if !errors.Is(err, ErrFulfillmentRequestIneligible) {
		t.Fatalf("error=%v, want ErrFulfillmentRequestIneligible", err)
	}
	if result != requester.result {
		t.Fatalf("mixed result was not returned for audit: %#v", result)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FulfillmentRequestStatus != "reconciliation_required" ||
		!strings.Contains(order.FulfillmentRequestError, "not explicitly identified as DSers") ||
		order.FulfillmentRequestedAt != nil {
		t.Fatalf("mixed fulfillment result was reported as successful: %+v", order)
	}
}

func TestFulfillmentRequestGateAllowsOnlyExplicitTerminalSkippedOrders(t *testing.T) {
	db := newAutoFulfillmentTestDB(t)
	shopifyOrderID := "gid://shopify/Order/closed"
	order := models.ShopOrder{
		ID: uuid.NewString(), UserID: "user-1", PaymentIntentID: testStringPointer("pi_closed_result"),
		ShopifyOrderID: &shopifyOrderID, Status: "processing", FinancialStatus: "paid",
		Currency: "HKD", TotalAmountMinor: 1000,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	requester := &fakeFulfillmentRequester{
		result: &shopify.AdminFulfillmentRequestResult{
			Skipped: []shopify.AdminFulfillmentRequestItem{{
				FulfillmentOrderID: "gid://shopify/FulfillmentOrder/closed",
				Status:             "CLOSED",
				RequestStatus:      "ACCEPTED",
				SkipReason:         "fulfillment order is already completed and closed",
				TerminalNoRequest:  true,
			}},
		},
	}

	if _, err := RequestOrderFulfillmentIfEligible(
		context.Background(),
		db,
		requester,
		order.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&order, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if order.FulfillmentRequestStatus != "not_required" ||
		order.FulfillmentRequestedAt != nil {
		t.Fatalf("terminal no-request result was not recorded: %+v", order)
	}
}
