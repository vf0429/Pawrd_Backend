package payments

import (
	"fmt"
	"log"
	"strconv"
	"strings"
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

// Dispatcher routes line items by Source. Phase B implements the shopify branch
// (placeholder/self-fulfillment) and reserves the hicustom branch for Phase C,
// where hicustom.Client.CreateOrder will be wired in.
type Dispatcher struct {
	// hiCustomOrders reserved for Phase C (hicustom.Client). nil = not yet wired.
}

// NewDispatcher builds a Dispatcher. The HiCustom client dependency is intentionally
// absent in Phase B; it will be injected here once the hicustom service exists.
func NewDispatcher() *Dispatcher { return &Dispatcher{} }

// Fulfill routes each item to its pipeline. Single-source-per-order is the
// supported shape (see docs/hicustom_integration_design.md §13.3); a mixed
// cart is split into separate PaymentIntents by the checkout layer.
func (d *Dispatcher) Fulfill(req FulfillmentRequest) error {
	if len(req.Items) == 0 {
		return fmt.Errorf("fulfillment: no items for payment %s", req.PaymentIntentID)
	}
	for _, it := range req.Items {
		switch it.Source {
		case SourceShopify:
			if err := d.fulfillShopify(req, it); err != nil {
				return err
			}
		case SourceHiCustom:
			if err := d.fulfillHiCustom(req, it); err != nil {
				return err
			}
		default:
			log.Printf("[fulfillment] unknown source %q for payment %s, skipping", it.Source, req.PaymentIntentID)
		}
	}
	return nil
}

func (d *Dispatcher) fulfillShopify(req FulfillmentRequest, it FulfillmentItem) error {
	// Phase B: reliable server-side acknowledgement only. Shopify Admin API order
	// creation (or self-fulfillment) lands in a later step — see design §13.5.
	log.Printf("[fulfillment][shopify] payment=%s handle=%s variant=%s qty=%d customer=%s — TODO create Shopify order",
		req.PaymentIntentID, it.Handle, it.VariantID, it.Quantity, req.CustomerEmail)
	return nil
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
