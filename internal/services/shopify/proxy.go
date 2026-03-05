package shopify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/vf0429/Petwell_Backend/internal/config"
)

const (
	defaultTimeout = 30 * time.Second
	maxRetries     = 3
)

// Client handles Shopify Storefront API communication
type Client struct {
	domain          string
	storefrontToken string
	httpClient      *http.Client
}

// NewClient creates a new Shopify Storefront API client
func NewClient(cfg *config.Config) (*Client, error) {
	if err := cfg.ValidateShopifyConfig(); err != nil {
		return nil, err
	}

	return &Client{
		domain:          cfg.ShopifyDomain,
		storefrontToken: cfg.ShopifyStorefrontToken,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}, nil
}

// FetchProducts retrieves products from Shopify Storefront API
func (c *Client) FetchProducts(first int, after string) ([]Product, bool, string, error) {
	if first <= 0 {
		first = 20
	}
	if first > 100 {
		first = 100 // Shopify limit
	}

	query := `
		query GetProducts($first: Int!, $after: String) {
			products(first: $first, after: $after) {
				edges {
					node {
						id
						title
						description
						handle
						productType
						vendor
						tags
						createdAt
						updatedAt
						priceRange {
							minVariantPrice {
								amount
								currencyCode
							}
							maxVariantPrice {
								amount
								currencyCode
							}
						}
						images(first: 10) {
							edges {
								node {
									id
									url
									altText
									width
									height
								}
							}
						}
						variants(first: 10) {
							edges {
								node {
									id
									title
									sku
									price {
										amount
										currencyCode
									}
									image {
										id
										url
										altText
										width
										height
									}
									availableForSale
								}
							}
						}
					}
				}
				pageInfo {
					hasNextPage
					endCursor
				}
			}
		}
	`

	variables := map[string]interface{}{
		"first": first,
	}
	if after != "" {
		variables["after"] = after
	}

	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	data, err := c.executeGraphQL(payload)
	if err != nil {
		return nil, false, "", err
	}

	var response struct {
		Products struct {
			Edges []struct {
				Node rawProduct `json:"node"`
			} `json:"edges"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"products"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, false, "", fmt.Errorf("failed to unmarshal products: %w", err)
	}

	products := make([]Product, 0, len(response.Products.Edges))
	for _, edge := range response.Products.Edges {
		products = append(products, edge.Node.toProduct())
	}

	return products, response.Products.PageInfo.HasNextPage, response.Products.PageInfo.EndCursor, nil
}

// SearchProducts queries Shopify with a full-text search string
func (c *Client) SearchProducts(query string, first int) ([]Product, error) {
	if first <= 0 {
		first = 20
	}
	if first > 100 {
		first = 100
	}

	gqlQuery := `
		query SearchProducts($first: Int!, $query: String!) {
			products(first: $first, query: $query) {
				edges {
					node {
						id title description handle productType vendor tags createdAt updatedAt
						priceRange {
							minVariantPrice { amount currencyCode }
							maxVariantPrice { amount currencyCode }
						}
						images(first: 5) { edges { node { id url altText width height } } }
						variants(first: 5) {
							edges { node { id title sku price { amount currencyCode } availableForSale } }
						}
					}
				}
			}
		}
	`

	payload := map[string]interface{}{
		"query":     gqlQuery,
		"variables": map[string]interface{}{"first": first, "query": query},
	}

	data, err := c.executeGraphQL(payload)
	if err != nil {
		return nil, err
	}

	var response struct {
		Products struct {
			Edges []struct {
				Node rawProduct `json:"node"`
			} `json:"edges"`
		} `json:"products"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search results: %w", err)
	}

	products := make([]Product, 0, len(response.Products.Edges))
	for _, edge := range response.Products.Edges {
		products = append(products, edge.Node.toProduct())
	}
	return products, nil
}

// FetchProductByHandle retrieves a single product by its handle
func (c *Client) FetchProductByHandle(handle string) (*Product, error) {
	query := `
		query GetProductByHandle($handle: String!) {
			product(handle: $handle) {
				id
				title
				description
				handle
				productType
				vendor
				tags
				createdAt
				updatedAt
				priceRange {
					minVariantPrice {
						amount
						currencyCode
					}
					maxVariantPrice {
						amount
						currencyCode
					}
				}
				images(first: 10) {
					edges {
						node {
							id
							url
							altText
							width
							height
						}
					}
				}
				variants(first: 10) {
					edges {
						node {
							id
							title
							sku
							price {
								amount
								currencyCode
							}
							image {
								id
								url
								altText
								width
								height
							}
							availableForSale
						}
					}
				}
			}
		}
	`

	payload := map[string]interface{}{
		"query": query,
		"variables": map[string]string{
			"handle": handle,
		},
	}

	data, err := c.executeGraphQL(payload)
	if err != nil {
		return nil, err
	}

	var response struct {
		Product *rawProduct `json:"product"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal product: %w", err)
	}

	if response.Product == nil {
		return nil, &ClientError{
			StatusCode: http.StatusNotFound,
			Message:    fmt.Sprintf("product with handle '%s' not found", handle),
		}
	}

	product := response.Product.toProduct()
	return &product, nil
}

// executeGraphQL performs the actual GraphQL request with retries
func (c *Client) executeGraphQL(payload map[string]interface{}) (json.RawMessage, error) {
	url := fmt.Sprintf("https://%s/api/2024-01/graphql.json", c.domain)

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonPayload))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shopify-Storefront-Access-Token", c.storefrontToken)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			log.Printf("Shopify request attempt %d failed: %v", attempt+1, err)
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("shopify returned status %d: %s", resp.StatusCode, string(body))
			log.Printf("Shopify request attempt %d failed: %v", attempt+1, lastErr)
			continue
		}

		var graphResp GraphQLResponse
		if err := json.Unmarshal(body, &graphResp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
		}

		if len(graphResp.Errors) > 0 {
			return nil, fmt.Errorf("graphql errors: %v", graphResp.Errors)
		}

		return graphResp.Data, nil
	}

	return nil, fmt.Errorf("all %d attempts failed: %w", maxRetries, lastErr)
}

// rawProduct is the internal representation matching Shopify's response structure
type rawProduct struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Handle      string    `json:"handle"`
	ProductType string    `json:"productType"`
	Vendor      string    `json:"vendor"`
	Tags        []string  `json:"tags"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	PriceRange  struct {
		MinVariantPrice Money `json:"minVariantPrice"`
		MaxVariantPrice Money `json:"maxVariantPrice"`
	} `json:"priceRange"`
	Images struct {
		Edges []struct {
			Node Image `json:"node"`
		} `json:"edges"`
	} `json:"images"`
	Variants struct {
		Edges []struct {
			Node rawVariant `json:"node"`
		} `json:"edges"`
	} `json:"variants"`
}

type rawVariant struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	SKU              string `json:"sku"`
	Price            Money  `json:"price"`
	Image            *Image `json:"image"`
	AvailableForSale bool   `json:"availableForSale"`
}

func (rp *rawProduct) toProduct() Product {
	images := make([]Image, 0, len(rp.Images.Edges))
	for _, edge := range rp.Images.Edges {
		images = append(images, edge.Node)
	}

	variants := make([]Variant, 0, len(rp.Variants.Edges))
	for _, edge := range rp.Variants.Edges {
		variants = append(variants, Variant{
			ID:               edge.Node.ID,
			Title:            edge.Node.Title,
			SKU:              edge.Node.SKU,
			Price:            edge.Node.Price,
			Image:            edge.Node.Image,
			AvailableForSale: edge.Node.AvailableForSale,
		})
	}

	return Product{
		ID:          rp.ID,
		Title:       rp.Title,
		Description: rp.Description,
		Handle:      rp.Handle,
		ProductType: rp.ProductType,
		Vendor:      rp.Vendor,
		Tags:        rp.Tags,
		CreatedAt:   rp.CreatedAt,
		UpdatedAt:   rp.UpdatedAt,
		PriceRange: PriceRange{
			MinVariantPrice: rp.PriceRange.MinVariantPrice,
			MaxVariantPrice: rp.PriceRange.MaxVariantPrice,
		},
		Images:   images,
		Variants: variants,
	}
}
