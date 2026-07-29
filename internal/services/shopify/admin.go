package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
)

type AdminOrderLineInput struct {
	VariantID        string
	Quantity         int
	RequiresShipping bool
	UnitPrice        string
}

type AdminOrderInput struct {
	Currency           string
	CustomerEmail      string
	CustomerPhone      string
	ShippingName       string
	ShippingPhone      string
	ShippingAddress    string
	ShippingCity       string
	ShippingRegion     string
	Amount             string
	PaymentID          string
	QuoteID            string
	ShippingTitle      string
	ShippingCode       string
	ShippingAmount     string
	DiscountCode       string
	DiscountAmount     string
	DiscountTargetType string
	TaxTitle           string
	TaxAmount          string
	TaxRate            string
	Lines              []AdminOrderLineInput
}

type AdminOrderResult struct {
	ID          string
	LegacyID    string
	Name        string
	TotalAmount string
	Currency    string
	LineItemIDs []string
}

type AdminOrderSnapshot struct {
	FulfillmentStatus   string
	TrackingCompany     string
	TrackingNumber      string
	TrackingURL         string
	EstimatedDeliveryAt *time.Time
	DeliveredAt         *time.Time
}

type AdminReturnResult struct {
	ID     string
	Name   string
	Status string
}

type AdminOrderClient interface {
	CreateOrder(context.Context, AdminOrderInput) (*AdminOrderResult, error)
	FetchOrder(context.Context, string) (*AdminOrderSnapshot, error)
	AddOrderTags(context.Context, string, []string) error
	RequestReturn(context.Context, string, string, string) (*AdminReturnResult, error)
}

// AdminOrderLookupClient supports idempotency recovery when Shopify accepted
// orderCreate but the local database update failed. Callers should look up the
// Stripe PaymentIntent source identifier before attempting another create.
type AdminOrderLookupClient interface {
	FindOrderBySourceIdentifier(context.Context, string) (*AdminOrderResult, error)
}

// ErrOrderCreateRejected means Shopify definitively returned a mutation
// userError. Callers must still repeat the sourceIdentifier lookup before
// compensating a captured payment.
var ErrOrderCreateRejected = errors.New("Shopify orderCreate was rejected")

var requiredWebhookTopics = []string{
	"FULFILLMENTS_CREATE",
	"FULFILLMENTS_UPDATE",
	"ORDERS_FULFILLED",
	"ORDERS_CANCELLED",
	"RETURNS_REQUEST",
	"RETURNS_APPROVE",
	"RETURNS_DECLINE",
	"RETURNS_CANCEL",
	"RETURNS_CLOSE",
	"RETURNS_REOPEN",
	"RETURNS_PROCESS",
	"RETURNS_UPDATE",
	"REFUNDS_CREATE",
}

type AdminClient struct {
	endpoint      string
	tokenProvider *adminTokenProvider
	httpClient    *http.Client
}

type adminTokenProvider struct {
	endpoint     string
	clientID     string
	clientSecret string
	staticToken  string
	httpClient   *http.Client
	now          func() time.Time

	mu        sync.Mutex
	token     string
	refreshAt time.Time
}

func NewAdminClient(cfg *config.Config) (*AdminClient, error) {
	if err := cfg.ValidateShopifyAdminConfig(); err != nil {
		return nil, err
	}
	domain := strings.TrimPrefix(strings.TrimSpace(cfg.ShopifyDomain), "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimRight(domain, "/")
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return &AdminClient{
		endpoint: fmt.Sprintf("https://%s/admin/api/%s/graphql.json", domain, cfg.ShopifyAdminAPIVersion),
		tokenProvider: &adminTokenProvider{
			endpoint:     fmt.Sprintf("https://%s/admin/oauth/access_token", domain),
			clientID:     cfg.ShopifyClientID,
			clientSecret: cfg.ShopifyClientSecret,
			staticToken:  cfg.ShopifyAdminAccessToken,
			httpClient:   httpClient,
			now:          time.Now,
		},
		httpClient: httpClient,
	}, nil
}

func (c *AdminClient) execute(ctx context.Context, query string, variables any, target any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	token, err := c.tokenProvider.Token(ctx, false)
	if err != nil {
		return err
	}
	status, raw, err := c.executeRequest(ctx, body, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && c.tokenProvider.Refreshable() {
		token, refreshErr := c.tokenProvider.Token(ctx, true)
		if refreshErr != nil {
			return fmt.Errorf("refresh Shopify Admin token after 401: %w", refreshErr)
		}
		status, raw, err = c.executeRequest(ctx, body, token)
		if err != nil {
			return err
		}
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("shopify admin returned %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []GraphQLError  `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode shopify admin response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("shopify admin graphql: %s", envelope.Errors[0].Message)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode shopify admin data: %w", err)
	}
	return nil
}

func (c *AdminClient) executeRequest(ctx context.Context, body []byte, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shopify-Access-Token", token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("shopify admin request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (p *adminTokenProvider) Refreshable() bool {
	return p.clientID != "" && p.clientSecret != ""
}

func (p *adminTokenProvider) Token(ctx context.Context, forceRefresh bool) (string, error) {
	if !p.Refreshable() {
		if p.staticToken == "" {
			return "", fmt.Errorf("Shopify Admin token is not configured")
		}
		return p.staticToken, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if !forceRefresh && p.token != "" && now.Before(p.refreshAt) {
		return p.token, nil
	}

	token, expiresIn, err := p.fetch(ctx)
	if err != nil {
		if p.staticToken != "" {
			return p.staticToken, nil
		}
		return "", err
	}
	p.token = token
	lifetime := time.Duration(expiresIn) * time.Second
	refreshSkew := 5 * time.Minute
	if lifetime <= 10*time.Minute {
		refreshSkew = lifetime / 10
	}
	p.refreshAt = now.Add(lifetime - refreshSkew)
	return p.token, nil
}

func (p *adminTokenProvider) fetch(ctx context.Context) (string, int64, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", p.clientID)
	form.Set("client_secret", p.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("request Shopify Admin token: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("Shopify token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, fmt.Errorf("decode Shopify Admin token response: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || payload.ExpiresIn <= 0 {
		return "", 0, fmt.Errorf("Shopify Admin token response is missing access_token or expires_in")
	}
	return strings.TrimSpace(payload.AccessToken), payload.ExpiresIn, nil
}

// EnsureWebhookSubscriptions creates only the missing shop-specific HTTPS
// subscriptions for Pawrd's order lifecycle. Existing subscriptions using the
// same callback URL are preserved, and subscriptions using other URLs are not
// changed.
func (c *AdminClient) EnsureWebhookSubscriptions(ctx context.Context, callbackURL string) (int, error) {
	return c.ensureWebhookSubscriptions(ctx, callbackURL, requiredWebhookTopics)
}

func (c *AdminClient) ensureWebhookSubscriptions(ctx context.Context, callbackURL string, topics []string) (int, error) {
	callbackURL = strings.TrimSpace(callbackURL)
	parsed, err := url.ParseRequestURI(callbackURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return 0, fmt.Errorf("Shopify webhook callback URL must be a valid HTTPS URL")
	}

	const subscriptionsQuery = `query PawrdWebhookSubscriptions {
	  webhookSubscriptions(first: 250) {
	    nodes { topic uri }
	  }
	}`
	var existingData struct {
		WebhookSubscriptions struct {
			Nodes []struct {
				Topic string `json:"topic"`
				URI   string `json:"uri"`
			} `json:"nodes"`
		} `json:"webhookSubscriptions"`
	}
	if err := c.execute(ctx, subscriptionsQuery, nil, &existingData); err != nil {
		return 0, fmt.Errorf("list Shopify webhook subscriptions: %w", err)
	}

	existing := make(map[string]bool, len(existingData.WebhookSubscriptions.Nodes))
	for _, subscription := range existingData.WebhookSubscriptions.Nodes {
		if strings.EqualFold(strings.TrimSpace(subscription.URI), callbackURL) {
			existing[strings.ToUpper(strings.TrimSpace(subscription.Topic))] = true
		}
	}

	const createMutation = `mutation CreatePawrdWebhook($topic: WebhookSubscriptionTopic!, $subscription: WebhookSubscriptionInput!) {
	  webhookSubscriptionCreate(topic: $topic, webhookSubscription: $subscription) {
	    webhookSubscription { id topic uri }
	    userErrors { field message }
	  }
	}`
	created := 0
	for _, topic := range topics {
		topic = strings.ToUpper(strings.TrimSpace(topic))
		if topic == "" || existing[topic] {
			continue
		}
		var createData struct {
			WebhookSubscriptionCreate struct {
				WebhookSubscription *struct {
					ID    string `json:"id"`
					Topic string `json:"topic"`
					URI   string `json:"uri"`
				} `json:"webhookSubscription"`
				UserErrors []struct {
					Message string `json:"message"`
				} `json:"userErrors"`
			} `json:"webhookSubscriptionCreate"`
		}
		variables := map[string]any{
			"topic":        topic,
			"subscription": map[string]any{"uri": callbackURL},
		}
		if err := c.execute(ctx, createMutation, variables, &createData); err != nil {
			return created, fmt.Errorf("create Shopify webhook %s: %w", topic, err)
		}
		if len(createData.WebhookSubscriptionCreate.UserErrors) > 0 {
			return created, fmt.Errorf("create Shopify webhook %s: %s", topic, createData.WebhookSubscriptionCreate.UserErrors[0].Message)
		}
		if createData.WebhookSubscriptionCreate.WebhookSubscription == nil {
			return created, fmt.Errorf("create Shopify webhook %s returned no subscription", topic)
		}
		existing[topic] = true
		created++
	}
	return created, nil
}

func (c *AdminClient) CreateOrder(ctx context.Context, input AdminOrderInput) (*AdminOrderResult, error) {
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		return nil, fmt.Errorf("shopify order currency is required")
	}
	if strings.TrimSpace(input.Amount) == "" {
		return nil, fmt.Errorf("shopify order amount is required")
	}
	if err := validateSourceIdentifier(input.PaymentID); err != nil {
		return nil, err
	}
	if len(input.Lines) == 0 {
		return nil, fmt.Errorf("shopify order requires at least one line")
	}

	lines := make([]map[string]any, 0, len(input.Lines))
	for _, line := range input.Lines {
		variantID := strings.TrimSpace(line.VariantID)
		if !strings.HasPrefix(variantID, "gid://shopify/ProductVariant/") || line.Quantity <= 0 {
			return nil, fmt.Errorf("shopify order contains an invalid line")
		}
		payload := map[string]any{
			"variantId":        variantID,
			"quantity":         line.Quantity,
			"requiresShipping": line.RequiresShipping,
		}
		if unitPrice := strings.TrimSpace(line.UnitPrice); unitPrice != "" {
			payload["priceSet"] = adminMoneyBag(unitPrice, currency)
		}
		lines = append(lines, payload)
	}
	order := map[string]any{
		"currency":         currency,
		"email":            input.CustomerEmail,
		"phone":            input.CustomerPhone,
		"financialStatus":  "PAID",
		"lineItems":        lines,
		"sourceIdentifier": strings.TrimSpace(input.PaymentID),
		"shippingAddress": map[string]any{
			"firstName":   input.ShippingName,
			"phone":       input.ShippingPhone,
			"address1":    input.ShippingAddress,
			"city":        input.ShippingCity,
			"province":    input.ShippingRegion,
			"countryCode": "HK",
		},
		"tags": []string{"Pawrd", "Stripe"},
		"transactions": []map[string]any{{
			"amountSet": adminMoneyBag(strings.TrimSpace(input.Amount), currency),
			"gateway":   "Stripe", "kind": "SALE", "status": "SUCCESS",
		}},
	}
	customAttributes := make([]map[string]any, 0, 2)
	if quoteID := strings.TrimSpace(input.QuoteID); quoteID != "" {
		customAttributes = append(customAttributes, map[string]any{
			"key": "Pawrd quote ID", "value": quoteID,
		})
	}
	if shippingTitle := strings.TrimSpace(input.ShippingTitle); shippingTitle != "" {
		shippingLine := map[string]any{
			"title":    shippingTitle,
			"priceSet": adminMoneyBag(defaultMoney(input.ShippingAmount), currency),
			"source":   "Shopify Storefront",
		}
		if shippingCode := strings.TrimSpace(input.ShippingCode); shippingCode != "" {
			shippingLine["code"] = shippingCode
		}
		order["shippingLines"] = []map[string]any{shippingLine}
	}
	if discountCode := strings.TrimSpace(input.DiscountCode); discountCode != "" {
		switch strings.ToUpper(strings.TrimSpace(input.DiscountTargetType)) {
		case "SHIPPING_LINE":
			if sameAdminMoney(input.DiscountAmount, input.ShippingAmount) {
				order["discountCode"] = map[string]any{
					"freeShippingDiscountCode": map[string]any{"code": discountCode},
				}
			} else {
				order["discountCode"] = map[string]any{
					"itemFixedDiscountCode": map[string]any{
						"amountSet": adminMoneyBag(defaultMoney(input.DiscountAmount), currency),
						"code":      discountCode,
					},
				}
				customAttributes = append(customAttributes, map[string]any{
					"key": "Pawrd original discount target", "value": "SHIPPING_LINE",
				})
			}
		case "", "LINE_ITEM":
			order["discountCode"] = map[string]any{
				"itemFixedDiscountCode": map[string]any{
					"amountSet": adminMoneyBag(defaultMoney(input.DiscountAmount), currency),
					"code":      discountCode,
				},
			}
		default:
			return nil, fmt.Errorf("unsupported Shopify discount target %q", input.DiscountTargetType)
		}
	}
	if len(customAttributes) > 0 {
		order["customAttributes"] = customAttributes
	}
	if taxAmount := strings.TrimSpace(input.TaxAmount); taxAmount != "" && taxAmount != "0" && taxAmount != "0.00" {
		taxRate := strings.TrimSpace(input.TaxRate)
		if taxRate == "" {
			return nil, fmt.Errorf("shopify tax rate is required when tax amount is non-zero")
		}
		taxTitle := strings.TrimSpace(input.TaxTitle)
		if taxTitle == "" {
			taxTitle = "Tax"
		}
		order["taxLines"] = []map[string]any{{
			"title":    taxTitle,
			"rate":     taxRate,
			"priceSet": adminMoneyBag(taxAmount, currency),
		}}
	}
	const mutation = `mutation CreatePawrdOrder(
	  $order: OrderCreateOrderInput!,
	  $options: OrderCreateOptionsInput
	) {
	  orderCreate(order: $order, options: $options) {
	    order {
	      id legacyResourceId name
	      totalPriceSet { shopMoney { amount currencyCode } }
	      lineItems(first: 100) { nodes { id } }
	    }
	    userErrors { field message }
	  }
	}`
	var data struct {
		OrderCreate struct {
			Order *struct {
				ID               string `json:"id"`
				LegacyResourceID string `json:"legacyResourceId"`
				Name             string `json:"name"`
				TotalPriceSet    struct {
					ShopMoney struct {
						Amount       string `json:"amount"`
						CurrencyCode string `json:"currencyCode"`
					} `json:"shopMoney"`
				} `json:"totalPriceSet"`
				LineItems struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"lineItems"`
			} `json:"order"`
			UserErrors []struct {
				Field   []string `json:"field"`
				Message string   `json:"message"`
			} `json:"userErrors"`
		} `json:"orderCreate"`
	}
	variables := map[string]any{
		"order": order,
		"options": map[string]any{
			"inventoryBehaviour":     "DECREMENT_OBEYING_POLICY",
			"sendReceipt":            true,
			"sendFulfillmentReceipt": false,
		},
	}
	if err := c.execute(ctx, mutation, variables, &data); err != nil {
		return nil, err
	}
	if len(data.OrderCreate.UserErrors) > 0 {
		return nil, fmt.Errorf(
			"%w: %s",
			ErrOrderCreateRejected,
			data.OrderCreate.UserErrors[0].Message,
		)
	}
	if data.OrderCreate.Order == nil {
		return nil, fmt.Errorf("shopify orderCreate returned no order")
	}
	result := &AdminOrderResult{
		ID:          data.OrderCreate.Order.ID,
		LegacyID:    data.OrderCreate.Order.LegacyResourceID,
		Name:        data.OrderCreate.Order.Name,
		TotalAmount: data.OrderCreate.Order.TotalPriceSet.ShopMoney.Amount,
		Currency:    data.OrderCreate.Order.TotalPriceSet.ShopMoney.CurrencyCode,
	}
	for _, line := range data.OrderCreate.Order.LineItems.Nodes {
		result.LineItemIDs = append(result.LineItemIDs, line.ID)
	}
	return result, nil
}

// FindOrderBySourceIdentifier returns the one Shopify order created for a
// Stripe PaymentIntent. A nil result means Shopify has no matching order.
func (c *AdminClient) FindOrderBySourceIdentifier(ctx context.Context, sourceIdentifier string) (*AdminOrderResult, error) {
	sourceIdentifier = strings.TrimSpace(sourceIdentifier)
	if err := validateSourceIdentifier(sourceIdentifier); err != nil {
		return nil, err
	}
	const query = `query PawrdOrderBySourceIdentifier($query: String!) {
	  orders(first: 2, query: $query) {
	    nodes {
	      id
	      legacyResourceId
	      name
	      totalPriceSet { shopMoney { amount currencyCode } }
	      lineItems(first: 100) { nodes { id } }
	    }
	  }
	}`
	var data struct {
		Orders struct {
			Nodes []struct {
				ID               string `json:"id"`
				LegacyResourceID string `json:"legacyResourceId"`
				Name             string `json:"name"`
				TotalPriceSet    struct {
					ShopMoney struct {
						Amount       string `json:"amount"`
						CurrencyCode string `json:"currencyCode"`
					} `json:"shopMoney"`
				} `json:"totalPriceSet"`
				LineItems struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"lineItems"`
			} `json:"nodes"`
		} `json:"orders"`
	}
	if err := c.execute(ctx, query, map[string]any{
		"query": "source_identifier:" + sourceIdentifier,
	}, &data); err != nil {
		return nil, err
	}
	if len(data.Orders.Nodes) == 0 {
		return nil, nil
	}
	if len(data.Orders.Nodes) > 1 {
		return nil, fmt.Errorf("multiple Shopify orders use source identifier %q", sourceIdentifier)
	}
	order := data.Orders.Nodes[0]
	result := &AdminOrderResult{
		ID: order.ID, LegacyID: order.LegacyResourceID, Name: order.Name,
		TotalAmount: order.TotalPriceSet.ShopMoney.Amount,
		Currency:    order.TotalPriceSet.ShopMoney.CurrencyCode,
	}
	for _, line := range order.LineItems.Nodes {
		result.LineItemIDs = append(result.LineItemIDs, line.ID)
	}
	return result, nil
}

func adminMoneyBag(amount, currency string) map[string]any {
	return map[string]any{
		"shopMoney": map[string]any{
			"amount": strings.TrimSpace(amount), "currencyCode": currency,
		},
	}
}

func defaultMoney(amount string) string {
	if amount = strings.TrimSpace(amount); amount != "" {
		return amount
	}
	return "0.00"
}

func sameAdminMoney(left, right string) bool {
	leftAmount, leftOK := new(big.Rat).SetString(defaultMoney(left))
	rightAmount, rightOK := new(big.Rat).SetString(defaultMoney(right))
	return leftOK && rightOK && leftAmount.Cmp(rightAmount) == 0
}

func validateSourceIdentifier(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 {
		return fmt.Errorf("Shopify source identifier is invalid")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' {
			continue
		}
		return fmt.Errorf("Shopify source identifier is invalid")
	}
	return nil
}

func (c *AdminClient) FetchOrder(ctx context.Context, orderID string) (*AdminOrderSnapshot, error) {
	const query = `query PawrdOrderLogistics($id: ID!) {
	  order(id: $id) {
	    displayFulfillmentStatus
	    fulfillments(first: 20) {
	      nodes {
	        status displayStatus estimatedDeliveryAt deliveredAt
	        trackingInfo(first: 10) { company number url }
	      }
	    }
	  }
	}`
	var data struct {
		Order *struct {
			DisplayFulfillmentStatus string `json:"displayFulfillmentStatus"`
			Fulfillments             struct {
				Nodes []struct {
					Status              string     `json:"status"`
					DisplayStatus       string     `json:"displayStatus"`
					EstimatedDeliveryAt *time.Time `json:"estimatedDeliveryAt"`
					DeliveredAt         *time.Time `json:"deliveredAt"`
					TrackingInfo        []struct {
						Company string `json:"company"`
						Number  string `json:"number"`
						URL     string `json:"url"`
					} `json:"trackingInfo"`
				} `json:"nodes"`
			} `json:"fulfillments"`
		} `json:"order"`
	}
	if err := c.execute(ctx, query, map[string]any{"id": orderID}, &data); err != nil {
		return nil, err
	}
	if data.Order == nil {
		return nil, fmt.Errorf("shopify order not found")
	}
	snapshot := &AdminOrderSnapshot{FulfillmentStatus: data.Order.DisplayFulfillmentStatus}
	if len(data.Order.Fulfillments.Nodes) > 0 {
		fulfillment := data.Order.Fulfillments.Nodes[0]
		if fulfillment.DisplayStatus != "" {
			snapshot.FulfillmentStatus = fulfillment.DisplayStatus
		}
		snapshot.EstimatedDeliveryAt = fulfillment.EstimatedDeliveryAt
		snapshot.DeliveredAt = fulfillment.DeliveredAt
		if len(fulfillment.TrackingInfo) > 0 {
			snapshot.TrackingCompany = fulfillment.TrackingInfo[0].Company
			snapshot.TrackingNumber = fulfillment.TrackingInfo[0].Number
			snapshot.TrackingURL = fulfillment.TrackingInfo[0].URL
		}
	}
	return snapshot, nil
}

func (c *AdminClient) AddOrderTags(ctx context.Context, orderID string, tags []string) error {
	const mutation = `mutation TagPawrdOrder($id: ID!, $tags: [String!]!) {
	  tagsAdd(id: $id, tags: $tags) { userErrors { field message } }
	}`
	var data struct {
		TagsAdd struct {
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"tagsAdd"`
	}
	if err := c.execute(ctx, mutation, map[string]any{"id": orderID, "tags": tags}, &data); err != nil {
		return err
	}
	if len(data.TagsAdd.UserErrors) > 0 {
		return fmt.Errorf("shopify tagsAdd: %s", data.TagsAdd.UserErrors[0].Message)
	}
	return nil
}

func (c *AdminClient) RequestReturn(ctx context.Context, orderID, reason, note string) (*AdminReturnResult, error) {
	const returnablesQuery = `query PawrdReturnables($orderId: ID!) {
	  returnableFulfillments(orderId: $orderId, first: 50) {
	    nodes {
	      returnableFulfillmentLineItems(first: 100) {
	        nodes { quantity fulfillmentLineItem { id } }
	      }
	    }
	  }
	}`
	var returnables struct {
		ReturnableFulfillments struct {
			Nodes []struct {
				Items struct {
					Nodes []struct {
						Quantity            int `json:"quantity"`
						FulfillmentLineItem struct {
							ID string `json:"id"`
						} `json:"fulfillmentLineItem"`
					} `json:"nodes"`
				} `json:"returnableFulfillmentLineItems"`
			} `json:"nodes"`
		} `json:"returnableFulfillments"`
	}
	if err := c.execute(ctx, returnablesQuery, map[string]any{"orderId": orderID}, &returnables); err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	for _, fulfillment := range returnables.ReturnableFulfillments.Nodes {
		for _, item := range fulfillment.Items.Nodes {
			if item.Quantity > 0 && item.FulfillmentLineItem.ID != "" {
				items = append(items, map[string]any{
					"fulfillmentLineItemId": item.FulfillmentLineItem.ID,
					"quantity":              item.Quantity,
					"returnReason":          reason,
					"customerNote":          note,
				})
			}
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("this order has no returnable fulfilled items")
	}
	const mutation = `mutation RequestPawrdReturn($input: ReturnRequestInput!) {
	  returnRequest(input: $input) {
	    return { id name status }
	    userErrors { field message }
	  }
	}`
	var data struct {
		ReturnRequest struct {
			Return     *AdminReturnResult `json:"return"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"returnRequest"`
	}
	input := map[string]any{"orderId": orderID, "returnLineItems": items}
	if err := c.execute(ctx, mutation, map[string]any{"input": input}, &data); err != nil {
		return nil, err
	}
	if len(data.ReturnRequest.UserErrors) > 0 {
		return nil, fmt.Errorf("shopify returnRequest: %s", data.ReturnRequest.UserErrors[0].Message)
	}
	if data.ReturnRequest.Return == nil {
		return nil, fmt.Errorf("shopify returnRequest returned no return")
	}
	return data.ReturnRequest.Return, nil
}
