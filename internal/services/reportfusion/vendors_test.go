package reportfusion

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizedVendorEndpointForAnthropicBaseURL(t *testing.T) {
	got := normalizedVendorEndpoint("https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic")
	want := "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1/messages"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestBuildVendorPayloadAnthropicIncludesImageBlocks(t *testing.T) {
	vendor := VendorDefinition{
		VendorID: "bailian",
		Endpoint: "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic",
		Model:    "qwen3.6-plus",
	}

	payload := buildVendorPayload(vendor, ExtractRequest{
		ImageBase64:    []string{"abc123"},
		ExtractionMode: "json",
	})

	messages, ok := payload["messages"].([]map[string]interface{})
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one message, got %#v", payload["messages"])
	}

	content, ok := messages[0]["content"].([]map[string]interface{})
	if !ok || len(content) < 2 {
		t.Fatalf("expected image + text content blocks, got %#v", messages[0]["content"])
	}

	if content[0]["type"] != "image" {
		t.Fatalf("expected first content block to be image, got %#v", content[0])
	}
}

func TestDecodeVendorFieldsAnthropicJSONContent(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": "{\"fields\":[{\"metric_key\":\"wbc\",\"value_number\":23.93,\"unit\":\"10^9/L\",\"reference_range\":\"3.26 - 19.00\",\"confidence\":0.91}]}",
			},
		},
	})

	fields, err := decodeVendorFields(VendorDefinition{VendorID: "bailian"}, body)
	if err != nil {
		t.Fatalf("decodeVendorFields returned error: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("expected 1 field, got %d", len(fields))
	}
	if fields[0].MetricKey != "wbc" {
		t.Fatalf("expected metric key wbc, got %q", fields[0].MetricKey)
	}
	if fields[0].ValueNumber == nil || *fields[0].ValueNumber != 23.93 {
		t.Fatalf("unexpected numeric value: %#v", fields[0].ValueNumber)
	}
}

func TestDecodeVendorMarkdownAnthropicTextContent(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": "## Blood Test\n\nWBC: 23.93"},
		},
	})

	markdown, err := decodeVendorMarkdown(VendorDefinition{VendorID: "bailian"}, body)
	if err != nil {
		t.Fatalf("decodeVendorMarkdown returned error: %v", err)
	}
	if !strings.Contains(markdown, "WBC: 23.93") {
		t.Fatalf("expected markdown content, got %q", markdown)
	}
}
