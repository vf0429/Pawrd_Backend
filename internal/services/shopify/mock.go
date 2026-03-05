package shopify

import (
	"fmt"
	"time"
)

// MockClient implements the same interface as Client but returns static data
type MockClient struct {
	products []Product
}

// NewMockClient creates a mock Shopify client with sample pet products
func NewMockClient() *MockClient {
	return &MockClient{
		products: generateMockProducts(),
	}
}

// FetchProducts returns mock products with pagination support
func (m *MockClient) FetchProducts(first int, after string) ([]Product, bool, string, error) {
	if first <= 0 {
		first = 20
	}

	// Parse cursor if provided
	startIndex := 0
	if after != "" {
		// Simple cursor parsing (index-based)
		var cursorIndex int
		if _, err := fmt.Sscanf(after, "cursor_%d", &cursorIndex); err == nil {
			startIndex = cursorIndex
		}
	}

	// Calculate end index
	endIndex := startIndex + first
	if endIndex > len(m.products) {
		endIndex = len(m.products)
	}

	// Get products for this page
	products := m.products[startIndex:endIndex]

	// Determine if there are more products
	hasMore := endIndex < len(m.products)

	// Generate next cursor
	nextCursor := ""
	if hasMore {
		nextCursor = fmt.Sprintf("cursor_%d", endIndex)
	}

	return products, hasMore, nextCursor, nil
}

// FetchProductByHandle returns a single mock product by handle
func (m *MockClient) FetchProductByHandle(handle string) (*Product, error) {
	for _, p := range m.products {
		if p.Handle == handle {
			return &p, nil
		}
	}
	return nil, &ClientError{
		StatusCode: 404,
		Message:    fmt.Sprintf("product with handle '%s' not found", handle),
	}
}

// generateMockProducts creates sample pet products for development
func generateMockProducts() []Product {
	now := time.Now()

	return []Product{
		// Dog Food Products (3)
		{
			ID:          "gid://shopify/Product/100001",
			Title:       "Premium Grain-Free Dog Food",
			Description: "High-quality grain-free dog food made with real chicken. Perfect for dogs with sensitive stomachs. Rich in protein and essential nutrients.",
			Handle:      "premium-grain-free-dog-food",
			ProductType: "Dog Food",
			Vendor:      "PetWell Nutrition",
			Tags:        []string{"dog", "food", "grain-free", "premium"},
			CreatedAt:   now.Add(-30 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "450.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "850.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_1", URL: "https://images.unsplash.com/photo-1589924691995-400dc9ecc119?w=500", AltText: "Premium Dog Food", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200001", Title: "2kg", SKU: "DOG-001-2KG", Price: Money{Amount: "450.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200002", Title: "5kg", SKU: "DOG-001-5KG", Price: Money{Amount: "850.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100002",
			Title:       "Organic Puppy Formula",
			Description: "Specially formulated organic food for growing puppies. Contains DHA for brain development and calcium for strong bones.",
			Handle:      "organic-puppy-formula",
			ProductType: "Dog Food",
			Vendor:      "Nature's Best",
			Tags:        []string{"dog", "puppy", "organic", "food"},
			CreatedAt:   now.Add(-45 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "380.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "720.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_2", URL: "https://images.unsplash.com/photo-1568640347023-a616a30bc3bd?w=500", AltText: "Puppy Formula", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200003", Title: "1.5kg", SKU: "DOG-002-1.5KG", Price: Money{Amount: "380.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200004", Title: "4kg", SKU: "DOG-002-4KG", Price: Money{Amount: "720.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100003",
			Title:       "Senior Dog Wellness Food",
			Description: "Complete nutrition for senior dogs 7+ years. Joint support formula with glucosamine and reduced calories for maintaining healthy weight.",
			Handle:      "senior-dog-wellness-food",
			ProductType: "Dog Food",
			Vendor:      "PetWell Nutrition",
			Tags:        []string{"dog", "senior", "food", "wellness"},
			CreatedAt:   now.Add(-60 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "520.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "980.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_3", URL: "https://images.unsplash.com/photo-1608408891486-f58d5c6c70f7?w=500", AltText: "Senior Dog Food", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200005", Title: "2kg", SKU: "DOG-003-2KG", Price: Money{Amount: "520.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200006", Title: "6kg", SKU: "DOG-003-6KG", Price: Money{Amount: "980.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},

		// Cat Food Products (3)
		{
			ID:          "gid://shopify/Product/100004",
			Title:       "Gourmet Wet Cat Food - Salmon",
			Description: "Premium wet cat food with real salmon chunks. Grain-free recipe rich in omega-3 for healthy coat and skin.",
			Handle:      "gourmet-wet-cat-food-salmon",
			ProductType: "Cat Food",
			Vendor:      "Feline Feast",
			Tags:        []string{"cat", "wet food", "salmon", "grain-free"},
			CreatedAt:   now.Add(-20 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "180.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "680.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_4", URL: "https://images.unsplash.com/photo-1583337130417-3346a1be7dee?w=500", AltText: "Salmon Cat Food", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200007", Title: "6 cans", SKU: "CAT-001-6C", Price: Money{Amount: "180.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200008", Title: "24 cans", SKU: "CAT-001-24C", Price: Money{Amount: "680.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100005",
			Title:       "Indoor Cat Dry Food",
			Description: "Specially formulated for indoor cats with hairball control. High fiber content and reduced calories for less active lifestyles.",
			Handle:      "indoor-cat-dry-food",
			ProductType: "Cat Food",
			Vendor:      "PetWell Nutrition",
			Tags:        []string{"cat", "dry food", "indoor", "hairball"},
			CreatedAt:   now.Add(-35 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "320.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "580.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_5", URL: "https://images.unsplash.com/photo-1514888286974-6c03e2ca1dba?w=500", AltText: "Indoor Cat Food", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200009", Title: "2kg", SKU: "CAT-002-2KG", Price: Money{Amount: "320.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200010", Title: "5kg", SKU: "CAT-002-5KG", Price: Money{Amount: "580.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100006",
			Title:       "Kitten Growth Formula",
			Description: "Complete balanced nutrition for kittens up to 12 months. Supports healthy growth and immune system development.",
			Handle:      "kitten-growth-formula",
			ProductType: "Cat Food",
			Vendor:      "Nature's Best",
			Tags:        []string{"cat", "kitten", "growth", "food"},
			CreatedAt:   now.Add(-40 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "280.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "520.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_6", URL: "https://images.unsplash.com/photo-1574158622682-e40e69881006?w=500", AltText: "Kitten Formula", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200011", Title: "1.5kg", SKU: "CAT-003-1.5KG", Price: Money{Amount: "280.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200012", Title: "4kg", SKU: "CAT-003-4KG", Price: Money{Amount: "520.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},

		// Toys (3)
		{
			ID:          "gid://shopify/Product/100007",
			Title:      "Interactive Dog Puzzle Toy",
			Description: "Keep your dog mentally stimulated with this challenging puzzle toy. Hide treats inside and watch them solve it. Durable and washable.",
			Handle:      "interactive-dog-puzzle-toy",
			ProductType: "Toys",
			Vendor:      "SmartPets",
			Tags:        []string{"dog", "toy", "puzzle", "interactive"},
			CreatedAt:   now.Add(-15 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "198.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "198.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_7", URL: "https://images.unsplash.com/photo-1576201836106-db1758fd1c97?w=500", AltText: "Dog Puzzle Toy", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200013", Title: "Default", SKU: "TOY-001", Price: Money{Amount: "198.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100008",
			Title:       "Feather Wand Cat Toy Set",
			Description: "Set of 3 interactive feather wands with bells. Perfect for bonding time with your cat. Replaceable feathers included.",
			Handle:      "feather-wand-cat-toy-set",
			ProductType: "Toys",
			Vendor:      "Feline Fun",
			Tags:        []string{"cat", "toy", "feather", "interactive"},
			CreatedAt:   now.Add(-25 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "88.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "88.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_8", URL: "https://images.unsplash.com/photo-1545249390-6bdfa286032f?w=500", AltText: "Cat Feather Toy", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200014", Title: "Set of 3", SKU: "TOY-002", Price: Money{Amount: "88.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100009",
			Title:       "Squeaky Plush Bone Toy",
			Description: "Soft plush bone with built-in squeaker. Made with non-toxic materials. Machine washable cover.",
			Handle:      "squeaky-plush-bone-toy",
			ProductType: "Toys",
			Vendor:      "PetWell Store",
			Tags:        []string{"dog", "toy", "plush", "squeaky"},
			CreatedAt:   now.Add(-10 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "68.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "68.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_9", URL: "https://images.unsplash.com/photo-1535294435445-d7249524ef2e?w=500", AltText: "Plush Bone Toy", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200015", Title: "Medium", SKU: "TOY-003", Price: Money{Amount: "68.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},

		// Accessories (3)
		{
			ID:          "gid://shopify/Product/100010",
			Title:       "Reflective Nylon Dog Leash",
			Description: "Durable nylon leash with reflective stitching for night safety. Comfortable padded handle. 1.5m length.",
			Handle:      "reflective-nylon-dog-leash",
			ProductType: "Accessories",
			Vendor:      "SafeWalk",
			Tags:        []string{"dog", "leash", "accessory", "safety"},
			CreatedAt:   now.Add(-18 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "128.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "128.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_10", URL: "https://images.unsplash.com/photo-1601758228041-f3b2795255f1?w=500", AltText: "Dog Leash", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200016", Title: "Standard", SKU: "ACC-001", Price: Money{Amount: "128.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100011",
			Title:       "Stainless Steel Pet Bowl",
			Description: "Non-slip stainless steel bowl with rubber base. Rust-resistant and dishwasher safe. Available in multiple sizes.",
			Handle:      "stainless-steel-pet-bowl",
			ProductType: "Accessories",
			Vendor:      "PetWell Store",
			Tags:        []string{"bowl", "accessory", "stainless steel", "dog", "cat"},
			CreatedAt:   now.Add(-22 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "78.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "128.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_11", URL: "https://images.unsplash.com/photo-1568640347023-a616a30bc3bd?w=500", AltText: "Pet Bowl", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200017", Title: "Small", SKU: "ACC-002-S", Price: Money{Amount: "78.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200018", Title: "Large", SKU: "ACC-002-L", Price: Money{Amount: "128.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100012",
			Title:       "Orthopedic Pet Bed",
			Description: "Memory foam orthopedic bed for joint support. Removable washable cover. Non-slip bottom. Perfect for senior pets.",
			Handle:      "orthopedic-pet-bed",
			ProductType: "Accessories",
			Vendor:      "ComfortPets",
			Tags:        []string{"bed", "accessory", "orthopedic", "comfort"},
			CreatedAt:   now.Add(-28 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "380.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "680.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_12", URL: "https://images.unsplash.com/photo-1591946614720-90a587da4a36?w=500", AltText: "Pet Bed", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200019", Title: "Medium", SKU: "ACC-003-M", Price: Money{Amount: "380.00", CurrencyCode: "HKD"}, AvailableForSale: true},
				{ID: "gid://shopify/ProductVariant/200020", Title: "Large", SKU: "ACC-003-L", Price: Money{Amount: "680.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},

		// Health Products (2)
		{
			ID:          "gid://shopify/Product/100013",
			Title:       "Daily Multivitamin for Dogs",
			Description: "Complete daily vitamin supplement for dogs. Supports immune system, coat health, and energy levels. 60 chewable tablets.",
			Handle:      "daily-multivitamin-dogs",
			ProductType: "Health",
			Vendor:      "PetWell Health",
			Tags:        []string{"health", "vitamin", "supplement", "dog"},
			CreatedAt:   now.Add(-12 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "168.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "168.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_13", URL: "https://images.unsplash.com/photo-1628009368231-7603352989c3?w=500", AltText: "Dog Vitamins", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200021", Title: "60 tablets", SKU: "HLT-001", Price: Money{Amount: "168.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
		{
			ID:          "gid://shopify/Product/100014",
			Title:       "Gentle Oatmeal Pet Shampoo",
			Description: "Hypoallergenic oatmeal shampoo for sensitive skin. pH balanced formula. Tear-free and soap-free. Suitable for dogs and cats.",
			Handle:      "gentle-oatmeal-pet-shampoo",
			ProductType: "Health",
			Vendor:      "Nature's Best",
			Tags:        []string{"health", "grooming", "shampoo", "oatmeal"},
			CreatedAt:   now.Add(-8 * 24 * time.Hour),
			UpdatedAt:   now,
			PriceRange: PriceRange{
				MinVariantPrice: Money{Amount: "98.00", CurrencyCode: "HKD"},
				MaxVariantPrice: Money{Amount: "98.00", CurrencyCode: "HKD"},
			},
			Images: []Image{
				{ID: "img_14", URL: "https://images.unsplash.com/photo-1516734212186-a967f81ad0d7?w=500", AltText: "Pet Shampoo", Width: 500, Height: 500},
			},
			Variants: []Variant{
				{ID: "gid://shopify/ProductVariant/200022", Title: "500ml", SKU: "HLT-002", Price: Money{Amount: "98.00", CurrencyCode: "HKD"}, AvailableForSale: true},
			},
		},
	}
}
