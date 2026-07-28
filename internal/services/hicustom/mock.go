package hicustom

import (
	"fmt"
	"strings"
)

// MockClient returns sample blank products so the catalog + designer flow can
// be developed end-to-end without HiCustom credentials. Mirrors shopify.MockClient.
type MockClient struct {
	products []BlankProduct
}

func NewMockClient() *MockClient {
	return &MockClient{products: mockBlankProducts()}
}

func (m *MockClient) FetchBlankProducts(first int, after string) (*ProductListResult, error) {
	if first <= 0 {
		first = 20
	}
	start := 0
	if after != "" {
		var idx int
		if _, err := fmt.Sscanf(after, "cursor_%d", &idx); err == nil {
			start = idx
		}
	}
	end := start + first
	if end > len(m.products) {
		end = len(m.products)
	}
	hasMore := end < len(m.products)
	next := ""
	if hasMore {
		next = fmt.Sprintf("cursor_%d", end)
	}
	return &ProductListResult{
		Products:   m.products[start:end],
		HasMore:    hasMore,
		NextCursor: next,
	}, nil
}

func (m *MockClient) FetchBlankProductBySKU(sku string) (*BlankProduct, error) {
	for i := range m.products {
		if strings.EqualFold(m.products[i].SKU, sku) {
			p := m.products[i]
			return &p, nil
		}
	}
	return nil, fmt.Errorf("blank product %q not found", sku)
}

func (m *MockClient) DesignerURL(sku string) (string, error) {
	if _, err := m.FetchBlankProductBySKU(sku); err != nil {
		return "", err
	}
	// Dev designer URL — points at a local/placeholder page in mock mode.
	return fmt.Sprintf("https://designer.hicustom.example/designer?blank_sku=%s&mock=1", url_QueryEscape(sku)), nil
}

func (m *MockClient) CreateOrder(req CreateOrderRequest) (*CreateOrderResult, error) {
	return &CreateOrderResult{
		HiCustomOrderID: fmt.Sprintf("HC-MOCK-%s", req.OrderNo),
		Status:          "pending",
	}, nil
}

// url_QueryEscape is a tiny helper to avoid importing net/url just for the mock.
func url_QueryEscape(s string) string {
	r := strings.NewReplacer(" ", "%20", "/", "%2F")
	return r.Replace(s)
}

func mockBlankProducts() []BlankProduct {
	return []BlankProduct{
		{
			SKU: "BLANK-DOG-HOODIE", Title: "Dog Hoodie (Blank)", Category: "Apparel",
			Description: "Customizable dog hoodie — upload your pet's photo.",
			Price: "12.00", CurrencyCode: "HKD", Available: true,
			CoverURL: "https://placehold.co/600x600?text=Dog+Hoodie",
		},
		{
			SKU: "BLANK-LEASH", Title: "Pet Leash (Blank)", Category: "Accessory",
			Description: "Customizable pet leash with your own design.",
			Price: "8.50", CurrencyCode: "HKD", Available: true,
			CoverURL: "https://placehold.co/600x600?text=Pet+Leash",
		},
		{
			SKU: "BLANK-PHONE-CASE", Title: "Phone Case (Blank)", Category: "Accessory",
			Description: "Customizable phone case — put your pet on it.",
			Price: "6.00", CurrencyCode: "HKD", Available: true,
			CoverURL: "https://placehold.co/600x600?text=Phone+Case",
		},
		{
			SKU: "BLANK-FRISBEE", Title: "Pet Frisbee (Blank)", Category: "Toy",
			Description: "Customizable flying disc for playtime.",
			Price: "5.00", CurrencyCode: "HKD", Available: true,
			CoverURL: "https://placehold.co/600x600?text=Frisbee",
		},
	}
}
