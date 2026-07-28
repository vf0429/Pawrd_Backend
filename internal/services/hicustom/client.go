package hicustom

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
)

const (
	defaultTimeout = 30 * time.Second
	maxRetries     = 3
)

// Client is the contract the handlers depend on (mirrors handlers.ShopifyClient).
// Both the real Client and MockClient satisfy it, so USE_MOCK_HICUSTOM swaps impls.
type Client interface {
	FetchBlankProducts(first int, after string) (*ProductListResult, error)
	FetchBlankProductBySKU(sku string) (*BlankProduct, error)
	DesignerURL(sku string) (string, error)
	CreateOrder(req CreateOrderRequest) (*CreateOrderResult, error)
}

// realClient calls the HiCustom open platform. Endpoint paths are documented-
// but-unverified (TODO); the mock client is used in dev until creds exist.
type realClient struct {
	baseURL    string
	auth       *Authenticator
	httpClient *http.Client
}

func NewClient(cfg *config.Config) (Client, error) {
	if cfg.UseMockHiCustom {
		return NewMockClient(), nil
	}
	auth, err := NewAuthenticator(cfg)
	if err != nil {
		return nil, err
	}
	return &realClient{
		baseURL:    cfg.HiCustomBaseURL,
		auth:       auth,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}, nil
}

// call assembles common params + token + sign and performs the HTTP call with
// retry. TODO: GET params vs POST form — switch per endpoint once official docs
// confirm. For now everything is GET-with-query.
func (c *realClient) call(path string, biz map[string]string) (json.RawMessage, error) {
	token, err := c.auth.AccessToken()
	if err != nil {
		return nil, err
	}
	params := c.auth.CommonParams()
	for k, v := range biz {
		params[k] = v
	}
	if token != "" {
		params["access_token"] = token
	}
	params["sign"] = c.auth.Sign(params)

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			return nil, err
		}
		q := url.Values{}
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("hicustom status %d: %s", resp.StatusCode, string(body))
			continue
		}
		var envelope struct {
			Code int             `json:"code"`
			Msg  string          `json:"msg"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		if envelope.Code != 0 { // TODO: align with official error codes
			return nil, fmt.Errorf("hicustom api error: %s", envelope.Msg)
		}
		return envelope.Data, nil
	}
	return nil, fmt.Errorf("hicustom all %d attempts failed: %w", maxRetries, lastErr)
}

// FetchBlankProducts calls 空白产品列表. TODO: confirm path + paging field names.
func (c *realClient) FetchBlankProducts(first int, after string) (*ProductListResult, error) {
	biz := map[string]string{
		"page_size": fmt.Sprintf("%d", first),
	}
	if after != "" {
		biz["page_token"] = after
	}
	data, err := c.call("/open/blank/products/list", biz) // TODO: confirm path
	if err != nil {
		return nil, err
	}
	var resp struct {
		Products   []BlankProduct `json:"products"`
		HasMore    bool           `json:"hasMore"`
		NextCursor string         `json:"nextCursor"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &ProductListResult{Products: resp.Products, HasMore: resp.HasMore, NextCursor: resp.NextCursor}, nil
}

// FetchBlankProductBySKU calls 产品详情. TODO: confirm path + param name.
func (c *realClient) FetchBlankProductBySKU(sku string) (*BlankProduct, error) {
	data, err := c.call("/open/blank/products/detail", map[string]string{"sku": sku}) // TODO: confirm
	if err != nil {
		return nil, err
	}
	var p BlankProduct
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// DesignerURL builds a signed designer URL for the iOS WKWebView / web iframe.
// TODO: confirm whether HiCustom hosts the designer and how the signed token is
// passed. For now this composes a signed query string against baseURL.
func (c *realClient) DesignerURL(sku string) (string, error) {
	params := c.auth.CommonParams()
	params["blank_sku"] = sku
	token, _ := c.auth.AccessToken()
	if token != "" {
		params["access_token"] = token
	}
	params["sign"] = c.auth.Sign(params)

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return c.baseURL + "/designer?" + q.Encode(), nil // TODO: confirm designer host/path
}

// CreateOrder pushes a paid order to HiCustom. TODO: confirm path + body schema;
// likely a POST with JSON body rather than GET. Left as call() for now.
func (c *realClient) CreateOrder(req CreateOrderRequest) (*CreateOrderResult, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	biz := map[string]string{"order": string(raw)} // TODO: confirm transport
	data, err := c.call("/open/order/create", biz)  // TODO: confirm path
	if err != nil {
		return nil, err
	}
	var r CreateOrderResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
