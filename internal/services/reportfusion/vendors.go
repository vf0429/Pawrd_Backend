package reportfusion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	ImageURLs       []string `json:"image_urls,omitempty"`
	ImageBase64     []string `json:"image_base64,omitempty"`
	ExtractionMode  string   `json:"extraction_mode,omitempty"` // json | markdown
}

type VendorMarkdownResult struct {
	VendorID string `json:"vendor_id"`
	Model    string `json:"model"`
	Markdown string `json:"markdown"`
}

func NewVendorClient(timeout time.Duration) *VendorClient {
	override := strings.TrimSpace(os.Getenv("REPORT_VENDOR_TIMEOUT_SECONDS"))
	if override != "" {
		if secs, err := strconv.Atoi(override); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
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

func (c *VendorClient) ExtractMarkdownFromAll(ctx context.Context, req ExtractRequest) ([]VendorMarkdownResult, error) {
	active := c.ActiveVendors()
	if len(active) == 0 {
		return nil, errors.New("no active vendor endpoints configured")
	}

	req.ExtractionMode = "markdown"

	type oneResult struct {
		result VendorMarkdownResult
		err    error
	}
	ch := make(chan oneResult, len(active))
	for _, vendor := range active {
		v := vendor
		go func() {
			markdown, err := c.extractMarkdownFromVendor(ctx, v, req)
			if err != nil {
				ch <- oneResult{err: fmt.Errorf("%s: %w", v.VendorID, err)}
				return
			}
			ch <- oneResult{
				result: VendorMarkdownResult{
					VendorID: v.VendorID,
					Model:    v.Model,
					Markdown: markdown,
				},
			}
		}()
	}

	var (
		results []VendorMarkdownResult
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
	respBody, err := c.callVendor(ctx, vendor, req)
	if err != nil {
		return nil, err
	}
	return decodeVendorFields(vendor, respBody)
}

func (c *VendorClient) extractMarkdownFromVendor(ctx context.Context, vendor VendorDefinition, req ExtractRequest) (string, error) {
	respBody, err := c.callVendor(ctx, vendor, req)
	if err != nil {
		return "", err
	}
	return decodeVendorMarkdown(vendor, respBody)
}

func (c *VendorClient) callVendor(ctx context.Context, vendor VendorDefinition, req ExtractRequest) ([]byte, error) {
	payload := buildVendorPayload(vendor, req)
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
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBody))
		if len(msg) > 400 {
			msg = msg[:400] + "..."
		}
		return nil, fmt.Errorf("status %d body=%s", resp.StatusCode, msg)
	}
	return respBody, nil
}

func buildVendorPayload(vendor VendorDefinition, req ExtractRequest) map[string]interface{} {
	// SiliconFlow is OpenAI-compatible. Build chat/completions payload with image input.
	if strings.Contains(strings.ToLower(vendor.Endpoint), "siliconflow.cn") {
		mode := strings.TrimSpace(strings.ToLower(req.ExtractionMode))
		if mode == "" {
			mode = "json"
		}
		prompt := "Extract pet health report fields and output pure JSON only: {\"fields\":[{\"metric_key\":\"string\",\"value_number\":number|null,\"value_text\":\"string\",\"unit\":\"string\",\"reference_range\":\"string\",\"qualitative_result\":\"阳性|阴性|可疑|未知\",\"confidence\":0~1}]}. Preserve original table semantics. If value is NoCt, put value_text=\"NoCt\"."
		if mode == "markdown" {
			prompt = "Please transcribe this pet health report into Markdown only. Keep table structure, metrics, values, units, reference ranges and positive/negative results. If unreadable, mark [unclear]. Output Markdown body only, no JSON and no extra explanation."
		}
		contents := []map[string]interface{}{
			{
				"type": "text",
				"text": prompt,
			},
		}
		for _, u := range req.ImageURLs {
			u = strings.TrimSpace(u)
			if u == "" {
				continue
			}
			contents = append(contents, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": u,
				},
			})
		}
		for _, b64 := range req.ImageBase64 {
			b64 = strings.TrimSpace(b64)
			if b64 == "" {
				continue
			}
			contents = append(contents, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": "data:image/jpeg;base64," + b64,
				},
			})
		}

		return map[string]interface{}{
			"model": vendor.Model,
			"messages": []map[string]interface{}{
				{
					"role":    "user",
					"content": contents,
				},
			},
			"temperature": 0,
		}
	}

	// Generic vendor payload: easy to adapt for other providers.
	return map[string]interface{}{
		"model":        vendor.Model,
		"image_urls":   req.ImageURLs,
		"image_base64": req.ImageBase64,
	}
}

func decodeVendorMarkdown(vendor VendorDefinition, body []byte) (string, error) {
	var openAICompat struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openAICompat); err == nil && len(openAICompat.Choices) > 0 {
		content := strings.TrimSpace(extractContentText(openAICompat.Choices[0].Message.Content))
		if content != "" {
			return content, nil
		}
	}

	var direct struct {
		Markdown string `json:"markdown"`
		Content  string `json:"content"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(body, &direct); err == nil {
		content := strings.TrimSpace(direct.Markdown)
		if content == "" {
			content = strings.TrimSpace(direct.Content)
		}
		if content == "" {
			content = strings.TrimSpace(direct.Text)
		}
		if content != "" {
			return content, nil
		}
	}

	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 500 {
		snippet = snippet[:500] + "..."
	}
	return "", fmt.Errorf("unable to decode markdown for vendor %s, body=%s", vendor.VendorID, snippet)
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

func decodeVendorFields(vendor VendorDefinition, body []byte) ([]Field, error) {
	// Preferred shape: { "fields": [...] }
	var direct struct {
		Fields []Field `json:"fields"`
	}
	if err := json.Unmarshal(body, &direct); err == nil && len(direct.Fields) > 0 {
		return direct.Fields, nil
	}

	// OpenAI-compatible shape:
	// { "choices":[{"message":{"content":"{\"fields\":[...]}"}}] }
	// or content as array objects: [{"type":"text","text":"..."}]
	var openAICompat struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openAICompat); err == nil && len(openAICompat.Choices) > 0 {
		content := extractContentText(openAICompat.Choices[0].Message.Content)
		content = normalizeContentPayload(content)
		if content != "" {
			var nested struct {
				Fields []Field `json:"fields"`
			}
			if err := json.Unmarshal([]byte(content), &nested); err == nil && len(nested.Fields) > 0 {
				return nested.Fields, nil
			}
			if fields := decodeLooseFieldsJSON([]byte(content)); len(fields) > 0 {
				return fields, nil
			}
		}
	}

	// Fallback: loose parsing for vendor-specific schema variants.
	if fields := decodeLooseFieldsJSON(body); len(fields) > 0 {
		return fields, nil
	}

	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 500 {
		snippet = snippet[:500] + "..."
	}
	return nil, fmt.Errorf("unable to decode fields for vendor %s, body=%s", vendor.VendorID, snippet)
}

func extractContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		parts := make([]string, 0, len(arr))
		for _, item := range arr {
			if text, ok := item["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func normalizeContentPayload(content string) string {
	content = strings.TrimSpace(content)
	content = strings.Trim(content, "`")
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "json")
	content = strings.TrimSpace(content)
	// keep only the first JSON object if model adds commentary
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		return strings.TrimSpace(content[start : end+1])
	}
	return content
}

func decodeLooseFieldsJSON(body []byte) []Field {
	var any map[string]interface{}
	if err := json.Unmarshal(body, &any); err != nil {
		return nil
	}

	// Common alternate keys.
	keys := []string{"data", "result", "output", "extractions", "items", "metrics"}
	for _, key := range keys {
		if raw, ok := any[key]; ok {
			if arr, ok := raw.([]interface{}); ok {
				if parsed := parseFieldArray(arr); len(parsed) > 0 {
					return parsed
				}
			}
			if obj, ok := raw.(map[string]interface{}); ok {
				if nestedRaw, ok := obj["fields"]; ok {
					if arr, ok := nestedRaw.([]interface{}); ok {
						if parsed := parseFieldArray(arr); len(parsed) > 0 {
							return parsed
						}
					}
				}
			}
		}
	}

	if raw, ok := any["fields"]; ok {
		if arr, ok := raw.([]interface{}); ok {
			return parseFieldArray(arr)
		}
	}
	return nil
}

func parseFieldArray(arr []interface{}) []Field {
	out := make([]Field, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		key := pickString(obj, "metric_key", "metricKey", "name", "key")
		if strings.TrimSpace(key) == "" {
			continue
		}
		conf := pickFloat(obj, 0.7, "confidence", "score")
		unit := pickString(obj, "unit")
		text := pickString(obj, "value_text", "valueText", "value")
		num, hasNum := pickNumber(obj, "value_number", "valueNumber", "numeric", "number", "value")

		f := Field{
			MetricKey:         key,
			ValueText:         text,
			Unit:              unit,
			ReferenceRange:    pickString(obj, "reference_range", "referenceRange", "ref_range", "ct_reference_range"),
			QualitativeResult: pickString(obj, "qualitative_result", "qualitativeResult", "result", "conclusion"),
			Confidence:        conf,
		}
		if hasNum {
			f.ValueNumber = &num
			f.ValueText = ""
		}
		out = append(out, f)
	}
	return out
}

func pickString(obj map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if raw, ok := obj[key]; ok {
			if s, ok := raw.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func pickFloat(obj map[string]interface{}, fallback float64, keys ...string) float64 {
	for _, key := range keys {
		if v, ok := pickNumber(obj, key); ok {
			return clamp01(v)
		}
	}
	return fallback
}

func pickNumber(obj map[string]interface{}, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := obj[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case float64:
			return v, true
		case float32:
			return float64(v), true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case json.Number:
			n, err := v.Float64()
			if err == nil {
				return n, true
			}
		case string:
			n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
