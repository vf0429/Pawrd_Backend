package shopify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRequestOrderFulfillmentSubmitsPureDSersOrders(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var request struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch call {
		case 1:
			if request.Variables["id"] != "gid://shopify/Order/1" {
				t.Errorf("unexpected order id: %#v", request.Variables["id"])
			}
			for _, requiredField := range []string{
				"assignedLocation", "isFulfillmentService", "fulfillmentService",
				"handle", "serviceName", "pageInfo", "hasNextPage",
			} {
				if !strings.Contains(request.Query, requiredField) {
					t.Errorf("fulfillment identity query is missing %s", requiredField)
				}
			}
			_, _ = w.Write([]byte(`{"data":{"order":{"fulfillmentOrders":{"nodes":[
				{
					"id":"gid://shopify/FulfillmentOrder/1",
					"status":"OPEN",
					"requestStatus":"UNSUBMITTED",
					"supportedActions":[{"action":"REQUEST_FULFILLMENT"}],
					"assignedLocation":{
						"name":"dsers-fulfillment-service",
						"location":{
							"id":"gid://shopify/Location/dsers",
							"name":"dsers-fulfillment-service",
							"isFulfillmentService":true,
							"fulfillmentService":{
								"id":"gid://shopify/FulfillmentService/dsers",
								"handle":"dsers-fulfillment-service",
								"serviceName":"DSers Fulfillment Service"
							}
						}
					}
				},
				{
					"id":"gid://shopify/FulfillmentOrder/2",
					"status":"OPEN",
					"requestStatus":"SUBMITTED",
					"supportedActions":[],
					"assignedLocation":{
						"name":"DSers Fulfillment Service",
						"location":{
							"id":"gid://shopify/Location/dsers",
							"name":"DSers Fulfillment Service",
							"isFulfillmentService":true,
							"fulfillmentService":{
								"id":"gid://shopify/FulfillmentService/dsers",
								"handle":"dsers-fulfillment-service",
								"serviceName":"DSers Fulfillment Service"
							}
						}
					}
				},
				{
					"id":"gid://shopify/FulfillmentOrder/3",
					"status":"CLOSED",
					"requestStatus":"ACCEPTED",
					"supportedActions":[],
					"assignedLocation":{"name":"historical location","location":null}
				}
			]}}}}`))
		case 2:
			if !strings.Contains(request.Query, "notifyCustomer: true") {
				t.Error("fulfillment request must notify the customer")
			}
			if request.Variables["id"] != "gid://shopify/FulfillmentOrder/1" {
				t.Errorf("unexpected fulfillment order id: %#v", request.Variables["id"])
			}
			_, _ = w.Write([]byte(`{"data":{"fulfillmentOrderSubmitFulfillmentRequest":{
				"submittedFulfillmentOrder":{"id":"gid://shopify/FulfillmentOrder/1","status":"OPEN","requestStatus":"SUBMITTED"},
				"userErrors":[]}}}`))
		default:
			t.Errorf("unexpected GraphQL call %d", call)
		}
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "token"})
	result, err := client.RequestOrderFulfillment(context.Background(), "gid://shopify/Order/1")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("GraphQL calls=%d, want 2", calls.Load())
	}
	if len(result.Requested) != 1 || result.Requested[0].RequestStatus != "SUBMITTED" {
		t.Fatalf("unexpected requested result: %#v", result.Requested)
	}
	if result.Requested[0].FulfillmentServiceHandle != "dsers-fulfillment-service" {
		t.Fatalf("requested result lost DSers audit identity: %#v", result.Requested[0])
	}
	if len(result.AlreadyRequested) != 1 || len(result.Skipped) != 1 {
		t.Fatalf("unexpected already/skipped result: %#v", result)
	}
	if !result.Skipped[0].TerminalNoRequest ||
		!strings.Contains(result.Skipped[0].SkipReason, "completed") {
		t.Fatalf("completed terminal fulfillment order was not audited: %#v", result.Skipped)
	}
}

func TestRequestOrderFulfillmentIsIdempotentWhenAlreadySubmitted(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"order":{"fulfillmentOrders":{"nodes":[
			{
				"id":"gid://shopify/FulfillmentOrder/2",
				"status":"OPEN",
				"requestStatus":"SUBMITTED",
				"supportedActions":[],
				"assignedLocation":{
					"name":"DSers Fulfillment Service",
					"location":{
						"id":"gid://shopify/Location/dsers",
						"name":"DSers Fulfillment Service",
						"isFulfillmentService":true,
						"fulfillmentService":{
							"id":"gid://shopify/FulfillmentService/dsers",
							"handle":"dsers-fulfillment-service",
							"serviceName":"DSers Fulfillment Service"
						}
					}
				}
			}
		]}}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "token"})
	result, err := client.RequestOrderFulfillment(context.Background(), "gid://shopify/Order/1")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(result.Requested) != 0 || len(result.AlreadyRequested) != 1 {
		t.Fatalf("unexpected idempotent result calls=%d result=%#v", calls.Load(), result)
	}
}

func TestRequestOrderFulfillmentRejectsNonDSersBeforeMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"order":{"fulfillmentOrders":{"nodes":[
			{
				"id":"gid://shopify/FulfillmentOrder/other",
				"status":"OPEN",
				"requestStatus":"UNSUBMITTED",
				"supportedActions":[{"action":"REQUEST_FULFILLMENT"}],
				"assignedLocation":{
					"name":"DSers Fulfillment Service",
					"location":{
						"id":"gid://shopify/Location/other",
						"name":"dsers-fulfillment-service",
						"isFulfillmentService":true,
						"fulfillmentService":{
							"id":"gid://shopify/FulfillmentService/other",
							"handle":"other-fulfillment-service",
							"serviceName":"Other Fulfillment Service"
						}
					}
				}
			}
		]}}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "token"})
	result, err := client.RequestOrderFulfillment(context.Background(), "gid://shopify/Order/1")
	if !errors.Is(err, ErrFulfillmentRequestBlocked) {
		t.Fatalf("error=%v, want ErrFulfillmentRequestBlocked", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("non-DSers fulfillment reached a mutation; calls=%d", calls.Load())
	}
	if result == nil || len(result.Requested) != 0 || len(result.Skipped) != 1 {
		t.Fatalf("unexpected blocked result: %#v", result)
	}
	if result.Skipped[0].TerminalNoRequest ||
		!strings.Contains(result.Skipped[0].SkipReason, "not explicitly identified as DSers") {
		t.Fatalf("non-DSers reason was not auditable: %#v", result.Skipped[0])
	}
}

func TestRequestOrderFulfillmentRejectsMixedResultBeforePartialMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"order":{"fulfillmentOrders":{"nodes":[
			{
				"id":"gid://shopify/FulfillmentOrder/requestable",
				"status":"OPEN",
				"requestStatus":"UNSUBMITTED",
				"supportedActions":[{"action":"REQUEST_FULFILLMENT"}],
				"assignedLocation":{
					"name":"DSers Fulfillment Service",
					"location":{
						"id":"gid://shopify/Location/dsers",
						"name":"DSers Fulfillment Service",
						"isFulfillmentService":true,
						"fulfillmentService":{
							"id":"gid://shopify/FulfillmentService/dsers",
							"handle":"dsers-fulfillment-service",
							"serviceName":"DSers Fulfillment Service"
						}
					}
				}
			},
			{
				"id":"gid://shopify/FulfillmentOrder/already",
				"status":"OPEN",
				"requestStatus":"SUBMITTED",
				"supportedActions":[],
				"assignedLocation":{
					"name":"DSers Fulfillment Service",
					"location":{
						"id":"gid://shopify/Location/dsers",
						"name":"DSers Fulfillment Service",
						"isFulfillmentService":true,
						"fulfillmentService":{
							"id":"gid://shopify/FulfillmentService/dsers",
							"handle":"dsers-fulfillment-service",
							"serviceName":"DSers Fulfillment Service"
						}
					}
				}
			},
			{
				"id":"gid://shopify/FulfillmentOrder/blocked",
				"status":"OPEN",
				"requestStatus":"UNSUBMITTED",
				"supportedActions":[{"action":"CREATE_FULFILLMENT"}],
				"assignedLocation":{
					"name":"DSers Fulfillment Service",
					"location":{
						"id":"gid://shopify/Location/dsers",
						"name":"DSers Fulfillment Service",
						"isFulfillmentService":true,
						"fulfillmentService":{
							"id":"gid://shopify/FulfillmentService/dsers",
							"handle":"dsers-fulfillment-service",
							"serviceName":"DSers Fulfillment Service"
						}
					}
				}
			}
		]}}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "token"})
	result, err := client.RequestOrderFulfillment(context.Background(), "gid://shopify/Order/1")
	if !errors.Is(err, ErrFulfillmentRequestBlocked) {
		t.Fatalf("error=%v, want ErrFulfillmentRequestBlocked", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("mixed preflight made a partial mutation; calls=%d", calls.Load())
	}
	if result == nil || len(result.AlreadyRequested) != 1 ||
		len(result.Requested) != 0 || len(result.Skipped) != 2 {
		t.Fatalf("unexpected mixed blocked result: %#v", result)
	}
	var deferred, unsupported bool
	for _, item := range result.Skipped {
		deferred = deferred || strings.Contains(item.SkipReason, "another fulfillment order")
		unsupported = unsupported || strings.Contains(item.SkipReason, "does not expose REQUEST_FULFILLMENT")
	}
	if !deferred || !unsupported {
		t.Fatalf("mixed result did not retain both audit reasons: %#v", result.Skipped)
	}
}

func TestRequestOrderFulfillmentRejectsTruncatedFulfillmentOrderPage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"order":{"fulfillmentOrders":{
			"pageInfo":{"hasNextPage":true},
			"nodes":[{
				"id":"gid://shopify/FulfillmentOrder/first",
				"status":"OPEN",
				"requestStatus":"UNSUBMITTED",
				"supportedActions":[{"action":"REQUEST_FULFILLMENT"}],
				"assignedLocation":{
					"name":"DSers Fulfillment Service",
					"location":{
						"id":"gid://shopify/Location/dsers",
						"name":"DSers Fulfillment Service",
						"isFulfillmentService":true,
						"fulfillmentService":{
							"id":"gid://shopify/FulfillmentService/dsers",
							"handle":"dsers-fulfillment-service",
							"serviceName":"DSers Fulfillment Service"
						}
					}
				}
			}]
		}}}}`))
	}))
	defer server.Close()

	client := newTestAdminClient(server, &adminTokenProvider{staticToken: "token"})
	result, err := client.RequestOrderFulfillment(context.Background(), "gid://shopify/Order/1")
	if !errors.Is(err, ErrFulfillmentRequestBlocked) ||
		!strings.Contains(err.Error(), "more than 100") {
		t.Fatalf("error=%v, want truncated-page block", err)
	}
	if calls.Load() != 1 || result == nil || len(result.Requested) != 0 {
		t.Fatalf("truncated page reached a mutation: calls=%d result=%#v", calls.Load(), result)
	}
}
