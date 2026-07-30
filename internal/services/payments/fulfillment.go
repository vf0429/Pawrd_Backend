package payments

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

// ItemSource identifies which fulfillment pipeline handles a line item.
type ItemSource string

const (
	SourceShopify                             ItemSource = "shopify"
	SourceHiCustom                            ItemSource = "hicustom"
	shopifyReconciliationFailurePrefix                   = "shopify_order_reconciliation_required:"
	shopifyAddressReconciliationFailurePrefix            = "shopify_order_reconciliation_required:shipping_address:"
)

// FulfillmentItem is a parsed line item extracted from Stripe PaymentIntent metadata.
type FulfillmentItem struct {
	Source          ItemSource
	Handle          string // Shopify handle (shopify source)
	VariantID       string // Shopify variant id (shopify source)
	CustomProductID string // HiCustom customProductId (hicustom source)
	SKU             string // HiCustom blank SKU (hicustom source)
	Quantity        int
}

// FulfillmentRequest bundles everything the dispatcher needs to fulfill an order
// after a successful Stripe payment.
type FulfillmentRequest struct {
	PaymentIntentID string
	CustomerName    string
	CustomerEmail   string
	CustomerPhone   string
	Items           []FulfillmentItem
	// BeforeExternalDispatch is installed by the durable queue and deliberately
	// excluded from its JSON payload. Shopify dispatchers must call it exactly
	// once, after all local validation and immediately before orderCreate.
	BeforeExternalDispatch func() error `json:"-"`
}

// Fulfiller dispatches a paid order to the correct fulfillment pipeline.
type Fulfiller interface {
	Fulfill(req FulfillmentRequest) error
}

// ShopifyOrderReconciler performs a sourceIdentifier lookup before a paid
// order can be compensated. A lookup miss does not make an ambiguous
// orderCreate transport failure safe to retry or refund automatically.
type ShopifyOrderReconciler interface {
	ReconcileShopifyOrder(context.Context, string) (bool, error)
}

var ErrFulfillmentOrderDefinitelyRejected = errors.New(
	"Shopify definitively rejected the order and sourceIdentifier has no mapping",
)

var ErrFulfillmentOrderCreateAmbiguous = errors.New(
	"Shopify orderCreate result is ambiguous and must not be retried automatically",
)

// Dispatcher routes paid line items by source. Shopify orders are created through
// Admin GraphQL; the HiCustom branch remains reserved for the factory integration.
type Dispatcher struct {
	db           *gorm.DB
	shopifyAdmin shopify.AdminOrderClient
}

// NewDispatcher preserves the metadata-routing test/legacy constructor.
func NewDispatcher() *Dispatcher { return &Dispatcher{} }

func NewOrderDispatcher(db *gorm.DB, admin shopify.AdminOrderClient) *Dispatcher {
	return &Dispatcher{db: db, shopifyAdmin: admin}
}

// Fulfill routes each item to its pipeline. Single-source-per-order is the
// supported shape (see docs/hicustom_integration_design.md §13.3); a mixed
// cart is split into separate PaymentIntents by the checkout layer.
func (d *Dispatcher) Fulfill(req FulfillmentRequest) error {
	if len(req.Items) == 0 {
		return fmt.Errorf("fulfillment: no items for payment %s", req.PaymentIntentID)
	}
	if d.db == nil {
		for _, item := range req.Items {
			switch item.Source {
			case SourceShopify:
				log.Printf("[fulfillment][shopify] payment=%s handle=%s variant=%s qty=%d", req.PaymentIntentID, item.Handle, item.VariantID, item.Quantity)
			case SourceHiCustom:
				if err := d.fulfillHiCustom(req, item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	source := req.Items[0].Source
	for _, it := range req.Items {
		if it.Source != source {
			return fmt.Errorf("fulfillment: mixed-source order %s is not supported", req.PaymentIntentID)
		}
	}
	switch source {
	case SourceShopify:
		return d.fulfillShopify(req)
	case SourceHiCustom:
		for _, it := range req.Items {
			if err := d.fulfillHiCustom(req, it); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("fulfillment: unknown source %q", source)
	}
	return nil
}

func (d *Dispatcher) fulfillShopify(req FulfillmentRequest) error {
	if d.db == nil {
		log.Printf("[fulfillment][shopify] payment=%s — no order database configured", req.PaymentIntentID)
		return nil
	}
	var order models.ShopOrder
	if err := d.db.Preload("Items").Where("payment_intent_id = ?", req.PaymentIntentID).First(&order).Error; err != nil {
		return fmt.Errorf("load shop order: %w", err)
	}
	if order.ShopifyOrderGID() != "" {
		if err := d.repairShopifyOrderShippingAddress(context.Background(), order); err != nil {
			return fmt.Errorf("repair mapped Shopify order shipping address: %w", err)
		}
		if strings.Contains(order.FailureReason, shopifyAddressReconciliationFailurePrefix) {
			if err := d.db.Model(&models.ShopOrder{}).
				Where(
					"id = ? AND status = ? AND failure_reason = ?",
					order.ID,
					"reconciliation_required",
					order.FailureReason,
				).
				Updates(map[string]any{"status": "processing", "failure_reason": ""}).Error; err != nil {
				return fmt.Errorf("clear repaired Shopify address reconciliation: %w", err)
			}
			return nil
		}
		if strings.Contains(order.FailureReason, shopifyReconciliationFailurePrefix) {
			return fmt.Errorf("%s", order.FailureReason)
		}
		return nil
	}
	if err := d.db.Model(&order).Updates(map[string]any{
		"status":           orderStatusUnlessTerminal("paid"),
		"financial_status": paidUnlessTerminalFinancialStatus(),
		"failure_reason":   orderFailureUnlessTerminal(""),
	}).Error; err != nil {
		return err
	}
	if d.shopifyAdmin == nil {
		return fmt.Errorf("shopify admin client is not configured")
	}

	// Recover an order accepted by Shopify before a previous local mapping
	// transaction failed. sourceIdentifier is the stable Stripe PaymentIntent.
	mapped, err := d.reconcileShopifyOrder(context.Background(), order)
	if err != nil {
		_ = d.db.Model(&order).Updates(map[string]any{
			"status":         orderStatusUnlessTerminal("failed"),
			"failure_reason": orderFailureUnlessTerminal(err.Error()),
		}).Error
		return err
	}
	if mapped {
		return nil
	}

	adminInput, inputErr := d.shopifyOrderInput(order)
	if inputErr != nil {
		_ = d.db.Model(&order).Updates(map[string]any{
			"status":         orderStatusUnlessTerminal("failed"),
			"failure_reason": orderFailureUnlessTerminal(inputErr.Error()),
		}).Error
		return inputErr
	}
	if req.BeforeExternalDispatch != nil {
		if err := req.BeforeExternalDispatch(); err != nil {
			return fmt.Errorf("prepare Shopify orderCreate dispatch: %w", err)
		}
	}
	result, err := d.shopifyAdmin.CreateOrder(context.Background(), adminInput)
	if err != nil {
		// Every response after invoking orderCreate is reconciled once. A
		// transport/decode failure is never automatically retried because a
		// temporarily missing sourceIdentifier result is not a strong proof
		// that Shopify did not accept the first request.
		mapped, reconcileErr := d.reconcileShopifyOrder(context.Background(), order)
		switch {
		case mapped && reconcileErr == nil:
			return nil
		case errors.Is(err, shopify.ErrOrderCreateRejected) && reconcileErr == nil:
			err = fmt.Errorf(
				"%w: %v",
				ErrFulfillmentOrderDefinitelyRejected,
				err,
			)
		case reconcileErr != nil:
			err = fmt.Errorf(
				"%w: orderCreate error: %v; sourceIdentifier lookup: %v",
				ErrFulfillmentOrderCreateAmbiguous,
				err,
				reconcileErr,
			)
		default:
			err = fmt.Errorf("%w: %v", ErrFulfillmentOrderCreateAmbiguous, err)
		}
		_ = d.db.Model(&order).Updates(map[string]any{
			"status":         orderStatusUnlessTerminal("failed"),
			"failure_reason": orderFailureUnlessTerminal(err.Error()),
		}).Error
		return err
	}

	return d.persistShopifyOrderResult(order, result)
}

// ReconcileShopifyOrder resolves the orderCreate response-loss window without
// creating a new order. It is used both before orderCreate and as the final
// safety check before a system refund.
func (d *Dispatcher) ReconcileShopifyOrder(
	ctx context.Context,
	paymentIntentID string,
) (bool, error) {
	if d == nil || d.db == nil || d.shopifyAdmin == nil {
		return false, errors.New("Shopify order reconciliation is not configured")
	}
	var order models.ShopOrder
	if err := d.db.Preload("Items").
		First(&order, "payment_intent_id = ?", strings.TrimSpace(paymentIntentID)).Error; err != nil {
		return false, err
	}
	if order.ShopifyOrderGID() != "" {
		return true, nil
	}
	return d.reconcileShopifyOrder(ctx, order)
}

func (d *Dispatcher) reconcileShopifyOrder(
	ctx context.Context,
	order models.ShopOrder,
) (bool, error) {
	lookup, ok := d.shopifyAdmin.(shopify.AdminOrderLookupClient)
	if !ok {
		return false, errors.New(
			"Shopify admin client does not support source-identifier idempotency lookup",
		)
	}
	result, err := lookup.FindOrderBySourceIdentifier(ctx, order.PaymentIntentIDValue())
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	if !result.HasCompleteShippingAddress {
		if err := d.updateShopifyOrderShippingAddress(ctx, result.ID, order); err != nil {
			reconciliationErr := fmt.Errorf(
				"%s repair reconciled Shopify order shipping address: %w",
				shopifyAddressReconciliationFailurePrefix,
				err,
			)
			if persistErr := d.persistShopifyOrderMapping(
				order,
				result,
				"reconciliation_required",
				reconciliationErr.Error(),
			); persistErr != nil {
				return true, fmt.Errorf(
					"%v; persist Shopify reconciliation mapping: %w",
					reconciliationErr,
					persistErr,
				)
			}
			return true, reconciliationErr
		}
		result.HasCompleteShippingAddress = true
	}
	if err := d.persistShopifyOrderResult(order, result); err != nil {
		return true, err
	}
	return true, nil
}

func (d *Dispatcher) shopifyOrderInput(order models.ShopOrder) (shopify.AdminOrderInput, error) {
	input := shopify.AdminOrderInput{
		Currency:        order.Currency,
		CustomerEmail:   order.CustomerEmail,
		CustomerPhone:   order.CustomerPhone,
		ShippingName:    order.CustomerName,
		ShippingPhone:   order.CustomerPhone,
		ShippingAddress: order.ShippingAddress1,
		ShippingCity:    order.ShippingDistrict,
		ShippingRegion:  order.ShippingRegion,
		Amount:          minorAmountString(order.TotalAmountMinor),
		PaymentID:       order.PaymentIntentIDValue(),
	}

	var quote models.ShopCheckoutQuote
	err := d.db.Where("order_id = ?", order.ID).First(&quote).Error
	switch {
	case err == nil:
		snapshot, snapshotErr := quote.DecodeAndVerifySnapshot()
		if snapshotErr != nil {
			return shopify.AdminOrderInput{}, fmt.Errorf("verify checkout quote for Shopify order: %w", snapshotErr)
		}
		if strings.TrimSpace(quote.PaymentIntentID) != strings.TrimSpace(order.PaymentIntentIDValue()) ||
			snapshot.Amounts.TotalAmountMinor != order.TotalAmountMinor {
			return shopify.AdminOrderInput{}, fmt.Errorf("checkout quote does not match paid order")
		}
		input.QuoteID = quote.ID
		input.DiscountCode = snapshot.Discount.Code
		input.DiscountAmount = minorAmountString(snapshot.Amounts.DiscountAmountMinor)
		input.DiscountTargetType = snapshot.Discount.TargetType
		input.TaxAmount = minorAmountString(snapshot.Amounts.TaxAmountMinor)
		if snapshot.Amounts.TaxAmountMinor > 0 {
			return shopify.AdminOrderInput{}, fmt.Errorf("tax-bearing Shopify carts are not supported by Hong Kong checkout")
		}
		if snapshot.SelectedDeliveryOption != nil {
			input.ShippingTitle = snapshot.SelectedDeliveryOption.Title
			input.ShippingCode = snapshot.SelectedDeliveryOption.Code
			input.ShippingAmount = minorAmountString(snapshot.SelectedDeliveryOption.AmountMinor)
		}
		input.Lines = make([]shopify.AdminOrderLineInput, 0, len(snapshot.LineItems))
		for _, item := range snapshot.LineItems {
			input.Lines = append(input.Lines, shopify.AdminOrderLineInput{
				VariantID: item.VariantID, Quantity: item.Quantity,
				// Pawrd checkout accepts only shippable physical variants. Force
				// the Admin order line to retain that invariant because Shopify
				// orderCreate defaults requiresShipping to false.
				RequiresShipping: true,
				UnitPrice:        minorAmountString(item.UnitAmountMinor),
			})
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Legacy paid orders predate quote snapshots. Preserve their server-side
		// stored unit prices while retaining the physical-product behavior.
		input.Lines = make([]shopify.AdminOrderLineInput, 0, len(order.Items))
		for _, item := range order.Items {
			input.Lines = append(input.Lines, shopify.AdminOrderLineInput{
				VariantID: item.VariantID, Quantity: item.Quantity,
				RequiresShipping: true,
				UnitPrice:        minorAmountString(item.UnitAmountMinor),
			})
		}
	default:
		return shopify.AdminOrderInput{}, fmt.Errorf("load checkout quote for Shopify order: %w", err)
	}
	return input, nil
}

func (d *Dispatcher) repairShopifyOrderShippingAddress(
	ctx context.Context,
	order models.ShopOrder,
) error {
	return d.updateShopifyOrderShippingAddress(ctx, order.ShopifyOrderGID(), order)
}

func (d *Dispatcher) updateShopifyOrderShippingAddress(
	ctx context.Context,
	orderID string,
	order models.ShopOrder,
) error {
	updater, ok := d.shopifyAdmin.(shopify.AdminOrderAddressClient)
	if !ok {
		return nil
	}
	if strings.TrimSpace(orderID) == "" ||
		strings.TrimSpace(order.CustomerName) == "" ||
		strings.TrimSpace(order.CustomerPhone) == "" ||
		strings.TrimSpace(order.ShippingAddress1) == "" ||
		strings.TrimSpace(order.ShippingDistrict) == "" ||
		strings.TrimSpace(order.ShippingRegion) == "" {
		return nil
	}
	return updater.UpdateOrderShippingAddress(
		ctx,
		orderID,
		shopify.AdminShippingAddressInput{
			Name: order.CustomerName, Phone: order.CustomerPhone,
			Address: order.ShippingAddress1, City: order.ShippingDistrict,
			Region: order.ShippingRegion,
		},
	)
}

func (d *Dispatcher) persistShopifyOrderResult(order models.ShopOrder, result *shopify.AdminOrderResult) error {
	if result == nil || strings.TrimSpace(result.ID) == "" {
		return fmt.Errorf("Shopify returned no order result")
	}
	if err := validateShopifyOrderTotal(order, result); err != nil {
		reconciliationErr := fmt.Errorf("%s %w", shopifyReconciliationFailurePrefix, err)
		if persistErr := d.persistShopifyOrderMapping(
			order,
			result,
			"reconciliation_required",
			reconciliationErr.Error(),
		); persistErr != nil {
			return fmt.Errorf("%v; persist Shopify reconciliation mapping: %w", reconciliationErr, persistErr)
		}
		return reconciliationErr
	}
	return d.persistShopifyOrderMapping(order, result, "processing", "")
}

func (d *Dispatcher) persistShopifyOrderMapping(
	order models.ShopOrder,
	result *shopify.AdminOrderResult,
	status string,
	failureReason string,
) error {
	return d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&order).Updates(map[string]any{
			"shopify_order_id": result.ID, "shopify_order_legacy_id": result.LegacyID,
			"shopify_order_name": result.Name, "status": orderStatusUnlessTerminal(status),
			"financial_status": paidUnlessTerminalFinancialStatus(),
			"failure_reason":   orderFailureUnlessTerminal(failureReason),
		}).Error; err != nil {
			return err
		}
		for index, lineID := range result.LineItemIDs {
			if index < len(order.Items) {
				if err := tx.Model(&order.Items[index]).Update("shopify_line_item_id", lineID).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func validateShopifyOrderTotal(order models.ShopOrder, result *shopify.AdminOrderResult) error {
	if !strings.EqualFold(strings.TrimSpace(result.Currency), strings.TrimSpace(order.Currency)) {
		return fmt.Errorf(
			"Shopify order currency mismatch: expected %s, got %s",
			order.Currency,
			result.Currency,
		)
	}
	amount, ok := new(big.Rat).SetString(strings.TrimSpace(result.TotalAmount))
	if !ok || amount.Sign() < 0 {
		return fmt.Errorf("Shopify order returned an invalid total %q", result.TotalAmount)
	}
	amount.Mul(amount, big.NewRat(100, 1))
	if !amount.IsInt() || !amount.Num().IsInt64() {
		return fmt.Errorf("Shopify order total cannot be represented in currency minor units")
	}
	if amount.Num().Int64() != order.TotalAmountMinor {
		return fmt.Errorf(
			"Shopify order total mismatch: expected %s %s, got %s %s",
			minorAmountString(order.TotalAmountMinor),
			strings.ToUpper(order.Currency),
			result.TotalAmount,
			strings.ToUpper(result.Currency),
		)
	}
	return nil
}

var protectedFinancialStatuses = []string{
	"partially_refunded", "refunded", "disputed", "dispute_lost",
}

var protectedOrderStatuses = []string{
	"canceled", "cancelled", "payment_canceled",
	"refund_pending", "refunded", "partially_refunded",
	"payment_disputed", "payment_dispute_lost",
	"reconciliation_required", "refund_reconciliation_required",
}

var inactiveDisputeStatuses = []string{
	"", "won", "prevented", "warning_closed",
}

func paidUnlessTerminalFinancialStatus() any {
	return gorm.Expr(
		"CASE WHEN LOWER(financial_status) IN ? THEN financial_status ELSE ? END",
		protectedFinancialStatuses,
		"paid",
	)
}

func orderStatusUnlessTerminal(status string) any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
				OR UPPER(COALESCE(fulfillment_status, '')) IN ?
			THEN status
			ELSE ?
		END`,
		protectedOrderStatuses,
		protectedFinancialStatuses,
		inactiveDisputeStatuses,
		[]string{"CANCELED", "CANCELLED"},
		status,
	)
}

func orderFailureUnlessTerminal(failureReason string) any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
				OR UPPER(COALESCE(fulfillment_status, '')) IN ?
			THEN failure_reason
			ELSE ?
		END`,
		protectedOrderStatuses,
		protectedFinancialStatuses,
		inactiveDisputeStatuses,
		[]string{"CANCELED", "CANCELLED"},
		failureReason,
	)
}

func refundReconciliationStatusUnlessTerminal() any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
			THEN status
			ELSE ?
		END`,
		[]string{
			"refunded", "payment_disputed", "payment_dispute_lost",
			"reconciliation_required", "refund_reconciliation_required",
		},
		[]string{"refunded", "disputed", "dispute_lost"},
		inactiveDisputeStatuses,
		"refund_reconciliation_required",
	)
}

func refundReconciliationFailureUnlessTerminal(failureReason string) any {
	return gorm.Expr(
		`CASE
			WHEN LOWER(COALESCE(status, '')) IN ?
				OR LOWER(COALESCE(financial_status, '')) IN ?
				OR LOWER(COALESCE(dispute_status, '')) NOT IN ?
			THEN failure_reason
			ELSE ?
		END`,
		[]string{
			"refunded", "payment_disputed", "payment_dispute_lost",
			"reconciliation_required", "refund_reconciliation_required",
		},
		[]string{"refunded", "disputed", "dispute_lost"},
		inactiveDisputeStatuses,
		failureReason,
	)
}

func minorAmountString(amount int64) string {
	if amount < 0 {
		amount = 0
	}
	return fmt.Sprintf("%d.%02d", amount/100, amount%100)
}

func (d *Dispatcher) fulfillHiCustom(req FulfillmentRequest, it FulfillmentItem) error {
	// Phase C: hicustom.Client.CreateOrder(customProductId + address). Reserved —
	// returns nil so the webhook acknowledges the event; real push is wired in C.
	log.Printf("[fulfillment][hicustom] payment=%s customProductId=%s sku=%s qty=%d customer=%s — RESERVED for Phase C",
		req.PaymentIntentID, it.CustomProductID, it.SKU, it.Quantity, req.CustomerEmail)
	return nil
}

// ParseItemsFromMetadata extracts FulfillmentItems from Stripe PaymentIntent metadata
// produced by buildCheckoutPaymentData (handlers/shop_checkout.go).
//
// Supported formats (tolerant):
//
//	item_N = "handle | variantID | qty:N"                                (legacy)
//	item_N = "source=shopify | handle=... | variant=... | qty:N"         (new)
//	item_N = "source=hicustom | customProductId=... | sku=... | qty:N"   (new)
func ParseItemsFromMetadata(meta map[string]string) []FulfillmentItem {
	items := make([]FulfillmentItem, 0)
	for k, v := range meta {
		if !strings.HasPrefix(k, "item_") {
			continue
		}
		it, ok := parseItemLine(v)
		if !ok {
			continue
		}
		items = append(items, it)
	}
	return items
}

func parseItemLine(line string) (FulfillmentItem, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return FulfillmentItem{}, false
	}

	var it FulfillmentItem
	if strings.Contains(line, "=") {
		// kv-style: "source=... | handle=... | variant=... | qty:N"
		// qty uses a colon (qty:N) to match the legacy format, so handle it separately.
		for _, part := range strings.Split(line, "|") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "qty:") {
				if n, err := strconv.Atoi(strings.TrimPrefix(part, "qty:")); err == nil {
					it.Quantity = n
				}
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			key := strings.TrimSpace(kv[0])
			val := strings.TrimSpace(kv[1])
			switch key {
			case "source":
				it.Source = ItemSource(val)
			case "handle":
				it.Handle = val
			case "variant":
				it.VariantID = val
			case "customProductId":
				it.CustomProductID = val
			case "sku":
				it.SKU = val
			}
		}
	} else {
		// legacy: "handle | variantID | qty:N"
		parts := strings.Split(line, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) >= 1 {
			it.Handle = parts[0]
		}
		if len(parts) >= 2 {
			it.VariantID = parts[1]
		}
		for _, p := range parts {
			if strings.HasPrefix(p, "qty:") {
				if n, err := strconv.Atoi(strings.TrimPrefix(p, "qty:")); err == nil {
					it.Quantity = n
				}
			}
		}
	}

	if it.Source == "" {
		it.Source = SourceShopify // legacy checkout is always shopify
	}
	if it.Quantity <= 0 {
		it.Quantity = 1
	}
	return it, true
}
