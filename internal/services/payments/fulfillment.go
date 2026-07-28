package payments

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
	"gorm.io/gorm"
)

// ItemSource identifies which fulfillment pipeline handles a line item.
type ItemSource string

const (
	SourceShopify  ItemSource = "shopify"
	SourceHiCustom ItemSource = "hicustom"
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
}

// Fulfiller dispatches a paid order to the correct fulfillment pipeline.
type Fulfiller interface {
	Fulfill(req FulfillmentRequest) error
}

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
		return nil
	}
	if err := d.db.Model(&order).Updates(map[string]any{
		"status": "paid", "financial_status": "paid", "failure_reason": "",
	}).Error; err != nil {
		return err
	}
	if d.shopifyAdmin == nil {
		return fmt.Errorf("shopify admin client is not configured")
	}
	lines := make([]shopify.AdminOrderLineInput, 0, len(order.Items))
	for _, item := range order.Items {
		lines = append(lines, shopify.AdminOrderLineInput{VariantID: item.VariantID, Quantity: item.Quantity})
	}
	result, err := d.shopifyAdmin.CreateOrder(context.Background(), shopify.AdminOrderInput{
		Currency:        order.Currency,
		CustomerEmail:   order.CustomerEmail,
		CustomerPhone:   order.CustomerPhone,
		ShippingName:    order.CustomerName,
		ShippingPhone:   order.CustomerPhone,
		ShippingAddress: order.ShippingAddress1,
		ShippingCity:    order.ShippingDistrict,
		ShippingRegion:  order.ShippingRegion,
		Amount:          fmt.Sprintf("%.2f", float64(order.TotalAmountMinor)/100),
		PaymentID:       order.PaymentIntentID,
		Lines:           lines,
	})
	if err != nil {
		_ = d.db.Model(&order).Updates(map[string]any{"status": "failed", "failure_reason": err.Error()}).Error
		return err
	}
	return d.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&order).Updates(map[string]any{
			"shopify_order_id": result.ID, "shopify_order_legacy_id": result.LegacyID,
			"shopify_order_name": result.Name, "status": "processing",
			"financial_status": "paid", "failure_reason": "",
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
