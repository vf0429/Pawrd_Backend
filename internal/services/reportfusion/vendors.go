package reportfusion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type VendorClient struct {
	httpClient *http.Client
	vendors    []VendorDefinition
}

type VendorDefinition struct {
	VendorID    string
	Endpoint    string
	APIKey      string
	Model       string
	Reliability float64
}

type ExtractRequest struct {
	ImageURLs   []string `json:"image_urls,omitempty"`
	ImageBase64 []string `json:"image_base64,omitempty"`
}

func NewVendorClient(timeout time.Duration) *VendorClient {
	return &VendorClient{
		httpClient: &http.Client{Timeout: timeout},
		vendors:    loadVendorsFromEnv(),
	}
}

func (c *VendorClient) VendorSettings() []VendorSetting {
	settings := make([]VendorSetting, 0, len(c.vendors))
	for _, v := range c.vendors {
		settings = append(settings, VendorSetting{
			VendorID:    v.VendorID,
			Reliability: v.Reliability,
		})
	}
	return settings
}

func (c *VendorClient) ActiveVendors() []VendorDefinition {
	out := make([]VendorDefinition, 0, len(c.vendors))
	for _, v := range c.vendors {
		if strings.TrimSpace(v.Endpoint) == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func (c *VendorClient) ExtractFromAll(ctx context.Context, req ExtractRequest) ([]VendorResult, error) {
	active := c.ActiveVendors()
	if len(active) == 0 {
		return nil, errors.New("no active vendor endpoints configured")
	}

	type oneResult struct {
		result VendorResult
		err    error
	}
	ch := make(chan oneResult, len(active))
	for _, vendor := range active {
		v := vendor
		go func() {
			start := time.Now()
			fields, err := c.extractFromVendor(ctx, v, req)
			if err != nil {
				ch <- oneResult{err: fmt.Errorf("%s: %w", v.VendorID, err)}
				return
			}
			_ = start
			ch <- oneResult{
				result: VendorResult{
					VendorID: v.VendorID,
					Model:    v.Model,
					Fields:   fields,
				},
			}
		}()
	}

	var (
		results []VendorResult
		errs    []string
	)
	for i := 0; i < len(active); i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			if r.err != nil {
				errs = append(errs, r.err.Error())
				continue
			}
			results = append(results, r.result)
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("all vendor calls failed: %s", strings.Join(errs, "; "))
	}
	return results, nil
}

func (c *VendorClient) extractFromVendor(ctx context.Context, vendor VendorDefinition, req ExtractRequest) ([]Field, error) {
	payload := map[string]interface{}{
		"model":        vendor.Model,
		"image_urls":   req.ImageURLs,
		"image_base64": req.ImageBase64,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, vendor.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(vendor.APIKey) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+vendor.APIKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var decoded struct {
		Fields []Field `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Fields, nil
}

func loadVendorsFromEnv() []VendorDefinition {
	defs := make([]VendorDefinition, 0, 3)
	for i := 1; i <= 3; i++ {
		id := strings.TrimSpace(os.Getenv(fmt.Sprintf("REPORT_AGENT_%d_ID", i)))
		if id == "" {
			id = fmt.Sprintf("vendor_%d", i)
		}
		endpoint := strings.TrimSpace(os.Getenv(fmt.Sprintf("REPORT_AGENT_%d_ENDPOINT", i)))
		apiKey := strings.TrimSpace(os.Getenv(fmt.Sprintf("REPORT_AGENT_%d_API_KEY", i)))
		model := strings.TrimSpace(os.Getenv(fmt.Sprintf("REPORT_AGENT_%d_MODEL", i)))
		if model == "" {
			model = fmt.Sprintf("MODEL_%d", i)
		}
		reliability := parseFloatOrDefault(os.Getenv(fmt.Sprintf("REPORT_AGENT_%d_RELIABILITY", i)), 0.8)
		defs = append(defs, VendorDefinition{
			VendorID:    id,
			Endpoint:    endpoint,
			APIKey:      apiKey,
			Model:       model,
			Reliability: reliability,
		})
	}
	return defs
}

func parseFloatOrDefault(raw string, fallback float64) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
