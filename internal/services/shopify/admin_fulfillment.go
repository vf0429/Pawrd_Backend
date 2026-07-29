package shopify

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type AdminFulfillmentRequestItem struct {
	FulfillmentOrderID       string `json:"fulfillmentOrderId"`
	Status                   string `json:"status"`
	RequestStatus            string `json:"requestStatus"`
	AssignedLocationID       string `json:"assignedLocationId,omitempty"`
	AssignedLocationName     string `json:"assignedLocationName,omitempty"`
	FulfillmentServiceID     string `json:"fulfillmentServiceId,omitempty"`
	FulfillmentServiceHandle string `json:"fulfillmentServiceHandle,omitempty"`
	FulfillmentServiceName   string `json:"fulfillmentServiceName,omitempty"`
	SkipReason               string `json:"skipReason,omitempty"`
	TerminalNoRequest        bool   `json:"terminalNoRequest,omitempty"`
}

type AdminFulfillmentRequestResult struct {
	Requested        []AdminFulfillmentRequestItem `json:"requested"`
	AlreadyRequested []AdminFulfillmentRequestItem `json:"alreadyRequested"`
	Skipped          []AdminFulfillmentRequestItem `json:"skipped"`
}

var ErrFulfillmentRequestBlocked = errors.New("Shopify fulfillment request is blocked")

type AdminFulfillmentRequester interface {
	RequestOrderFulfillment(context.Context, string) (*AdminFulfillmentRequestResult, error)
}

type adminFulfillmentOrder struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	RequestStatus    string `json:"requestStatus"`
	SupportedActions []struct {
		Action string `json:"action"`
	} `json:"supportedActions"`
	AssignedLocation struct {
		Name     string `json:"name"`
		Location *struct {
			ID                   string `json:"id"`
			Name                 string `json:"name"`
			IsFulfillmentService bool   `json:"isFulfillmentService"`
			FulfillmentService   *struct {
				ID          string `json:"id"`
				Handle      string `json:"handle"`
				ServiceName string `json:"serviceName"`
			} `json:"fulfillmentService"`
		} `json:"location"`
	} `json:"assignedLocation"`
}

// RequestOrderFulfillment submits every currently requestable fulfillment order
// only when Shopify explicitly identifies the assigned service as DSers.
// The complete order is preflighted before the first mutation so a mixed
// DSers/non-DSers order cannot be partially submitted by Pawrd.
func (c *AdminClient) RequestOrderFulfillment(ctx context.Context, orderID string) (*AdminFulfillmentRequestResult, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, fmt.Errorf("Shopify order ID is required")
	}
	const query = `query PawrdOrderFulfillmentRequests($id: ID!) {
		  order(id: $id) {
		    fulfillmentOrders(first: 100) {
		      pageInfo { hasNextPage }
		      nodes {
		        id
		        status
		        requestStatus
		        supportedActions { action }
		        assignedLocation {
		          name
		          location {
		            id
		            name
		            isFulfillmentService
		            fulfillmentService {
		              id
		              handle
		              serviceName
		            }
		          }
		        }
		      }
		    }
		  }
		}`
	var data struct {
		Order *struct {
			FulfillmentOrders struct {
				Nodes    []adminFulfillmentOrder `json:"nodes"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			} `json:"fulfillmentOrders"`
		} `json:"order"`
	}
	if err := c.execute(ctx, query, map[string]any{"id": orderID}, &data); err != nil {
		return nil, err
	}
	if data.Order == nil {
		return nil, fmt.Errorf("Shopify order not found")
	}

	result := &AdminFulfillmentRequestResult{
		Requested:        []AdminFulfillmentRequestItem{},
		AlreadyRequested: []AdminFulfillmentRequestItem{},
		Skipped:          []AdminFulfillmentRequestItem{},
	}
	if len(data.Order.FulfillmentOrders.Nodes) == 0 {
		return result, fmt.Errorf("%w: Shopify returned no visible fulfillment orders", ErrFulfillmentRequestBlocked)
	}
	if data.Order.FulfillmentOrders.PageInfo.HasNextPage {
		return result, fmt.Errorf(
			"%w: Shopify returned more than 100 fulfillment orders; refusing a partial request",
			ErrFulfillmentRequestBlocked,
		)
	}

	type requestCandidate struct {
		order adminFulfillmentOrder
		item  AdminFulfillmentRequestItem
	}
	candidates := make([]requestCandidate, 0, len(data.Order.FulfillmentOrders.Nodes))
	blockingReasons := make([]string, 0)
	for _, fulfillmentOrder := range data.Order.FulfillmentOrders.Nodes {
		item := adminFulfillmentRequestItem(fulfillmentOrder)
		if completedFulfillmentOrderNeedsNoRequest(fulfillmentOrder) {
			item.TerminalNoRequest = true
			item.SkipReason = "fulfillment order is already completed and closed"
			result.Skipped = append(result.Skipped, item)
			continue
		}
		if !isDSersFulfillmentOrder(fulfillmentOrder) {
			item.SkipReason = "assigned fulfillment service is not explicitly identified as DSers"
			result.Skipped = append(result.Skipped, item)
			blockingReasons = append(blockingReasons, fulfillmentBlockReason(item))
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(fulfillmentOrder.RequestStatus)) {
		case "SUBMITTED", "ACCEPTED":
			result.AlreadyRequested = append(result.AlreadyRequested, item)
			continue
		case "UNSUBMITTED":
			// Continue below and submit only when Shopify exposes the
			// REQUEST_FULFILLMENT action.
		default:
			item.SkipReason = fmt.Sprintf(
				"DSers fulfillment request status %q requires operator review",
				strings.TrimSpace(fulfillmentOrder.RequestStatus),
			)
			result.Skipped = append(result.Skipped, item)
			blockingReasons = append(blockingReasons, fulfillmentBlockReason(item))
			continue
		}
		if !supportsFulfillmentRequest(fulfillmentOrder.SupportedActions) {
			item.SkipReason = "Shopify does not expose REQUEST_FULFILLMENT for this DSers fulfillment order"
			result.Skipped = append(result.Skipped, item)
			blockingReasons = append(blockingReasons, fulfillmentBlockReason(item))
			continue
		}
		candidates = append(candidates, requestCandidate{order: fulfillmentOrder, item: item})
	}

	if len(blockingReasons) > 0 {
		for _, candidate := range candidates {
			item := candidate.item
			item.SkipReason = "not submitted because another fulfillment order requires review"
			result.Skipped = append(result.Skipped, item)
		}
		return result, fmt.Errorf(
			"%w: %s",
			ErrFulfillmentRequestBlocked,
			strings.Join(blockingReasons, "; "),
		)
	}

	for _, candidate := range candidates {
		submitted, err := c.submitFulfillmentRequest(ctx, candidate.order.ID)
		if err != nil {
			return result, err
		}
		submitted.AssignedLocationID = candidate.item.AssignedLocationID
		submitted.AssignedLocationName = candidate.item.AssignedLocationName
		submitted.FulfillmentServiceID = candidate.item.FulfillmentServiceID
		submitted.FulfillmentServiceHandle = candidate.item.FulfillmentServiceHandle
		submitted.FulfillmentServiceName = candidate.item.FulfillmentServiceName
		switch strings.ToUpper(strings.TrimSpace(submitted.RequestStatus)) {
		case "SUBMITTED", "ACCEPTED":
			result.Requested = append(result.Requested, *submitted)
		default:
			submitted.SkipReason = fmt.Sprintf(
				"Shopify returned unexpected DSers request status %q after submission",
				strings.TrimSpace(submitted.RequestStatus),
			)
			result.Skipped = append(result.Skipped, *submitted)
			return result, fmt.Errorf(
				"%w: %s",
				ErrFulfillmentRequestBlocked,
				fulfillmentBlockReason(*submitted),
			)
		}
	}
	return result, nil
}

func adminFulfillmentRequestItem(order adminFulfillmentOrder) AdminFulfillmentRequestItem {
	item := AdminFulfillmentRequestItem{
		FulfillmentOrderID:   strings.TrimSpace(order.ID),
		Status:               strings.TrimSpace(order.Status),
		RequestStatus:        strings.TrimSpace(order.RequestStatus),
		AssignedLocationName: strings.TrimSpace(order.AssignedLocation.Name),
	}
	if order.AssignedLocation.Location == nil {
		return item
	}
	location := order.AssignedLocation.Location
	item.AssignedLocationID = strings.TrimSpace(location.ID)
	if name := strings.TrimSpace(location.Name); name != "" {
		item.AssignedLocationName = name
	}
	if location.FulfillmentService == nil {
		return item
	}
	item.FulfillmentServiceID = strings.TrimSpace(location.FulfillmentService.ID)
	item.FulfillmentServiceHandle = strings.TrimSpace(location.FulfillmentService.Handle)
	item.FulfillmentServiceName = strings.TrimSpace(location.FulfillmentService.ServiceName)
	return item
}

func isDSersFulfillmentOrder(order adminFulfillmentOrder) bool {
	location := order.AssignedLocation.Location
	if location == nil || !location.IsFulfillmentService || location.FulfillmentService == nil {
		return false
	}
	service := location.FulfillmentService
	for _, candidate := range []string{
		service.Handle,
		service.ServiceName,
	} {
		switch normalizeFulfillmentServiceIdentifier(candidate) {
		case "dsers", "dsersfulfillment", "dsersfulfillmentservice":
			return true
		}
	}
	return false
}

func normalizeFulfillmentServiceIdentifier(value string) string {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func completedFulfillmentOrderNeedsNoRequest(order adminFulfillmentOrder) bool {
	if !strings.EqualFold(strings.TrimSpace(order.Status), "CLOSED") {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(order.RequestStatus)) {
	case "CLOSED", "REJECTED", "CANCELLATION_REQUESTED", "CANCELLATION_REJECTED":
		return false
	default:
		return true
	}
}

func fulfillmentBlockReason(item AdminFulfillmentRequestItem) string {
	identity := strings.TrimSpace(item.FulfillmentOrderID)
	if identity == "" {
		identity = "unknown fulfillment order"
	}
	return identity + ": " + strings.TrimSpace(item.SkipReason)
}

func supportsFulfillmentRequest(actions []struct {
	Action string `json:"action"`
}) bool {
	for _, action := range actions {
		if strings.EqualFold(action.Action, "REQUEST_FULFILLMENT") {
			return true
		}
	}
	return false
}

func (c *AdminClient) submitFulfillmentRequest(ctx context.Context, fulfillmentOrderID string) (*AdminFulfillmentRequestItem, error) {
	const mutation = `mutation PawrdSubmitFulfillmentRequest($id: ID!) {
	  fulfillmentOrderSubmitFulfillmentRequest(id: $id, notifyCustomer: true) {
	    submittedFulfillmentOrder { id status requestStatus }
	    userErrors { field message }
	  }
	}`
	var data struct {
		Submit struct {
			Submitted *struct {
				ID            string `json:"id"`
				Status        string `json:"status"`
				RequestStatus string `json:"requestStatus"`
			} `json:"submittedFulfillmentOrder"`
			UserErrors []struct {
				Message string `json:"message"`
			} `json:"userErrors"`
		} `json:"fulfillmentOrderSubmitFulfillmentRequest"`
	}
	if err := c.execute(ctx, mutation, map[string]any{"id": fulfillmentOrderID}, &data); err != nil {
		return nil, err
	}
	if len(data.Submit.UserErrors) > 0 {
		return nil, fmt.Errorf("Shopify fulfillment request: %s", data.Submit.UserErrors[0].Message)
	}
	if data.Submit.Submitted == nil {
		return nil, fmt.Errorf("Shopify fulfillment request returned no submitted order")
	}
	return &AdminFulfillmentRequestItem{
		FulfillmentOrderID: data.Submit.Submitted.ID,
		Status:             data.Submit.Submitted.Status,
		RequestStatus:      data.Submit.Submitted.RequestStatus,
	}, nil
}
