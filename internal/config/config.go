package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	RAGServiceURL          string
	MapsAPIKey             string
	DBHost                 string
	DBPort                 string
	DBUser                 string
	DBPassword             string
	DBName                 string
	ShopifyDomain          string
	ShopifyStorefrontToken string
	UseMockShopify         bool
	StripeSecretKey        string
	StripePublishableKey   string
}

func LoadConfig() *Config {
	_ = godotenv.Load() // Ignore error if .env doesn't exist

	ragURL := os.Getenv("RAG_SERVICE_URL")
	if ragURL == "" {
		ragURL = "http://localhost:8001"
	}

	mapsKey := os.Getenv("MAPS_API_KEY")

	return &Config{
		RAGServiceURL:          ragURL,
		MapsAPIKey:             mapsKey,
		DBHost:                 getEnvOrDefault("DB_HOST", "localhost"),
		DBPort:                 getEnvOrDefault("DB_PORT", "5432"),
		DBUser:                 getEnvOrDefault("DB_USER", "postgres"),
		DBPassword:             getEnvOrDefault("DB_PASSWORD", "postgres"),
		DBName:                 getEnvOrDefault("DB_NAME", "pawrd"),
		ShopifyDomain:          strings.TrimSpace(os.Getenv("SHOPIFY_DOMAIN")),
		ShopifyStorefrontToken: strings.TrimSpace(os.Getenv("SHOPIFY_STOREFRONT_TOKEN")),
		UseMockShopify:         os.Getenv("USE_MOCK_SHOPIFY") == "true",
		StripeSecretKey:        strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		StripePublishableKey:   strings.TrimSpace(os.Getenv("STRIPE_PUBLISHABLE_KEY")),
	}
}

// ValidateShopifyConfig checks if Shopify configuration is properly set
func (c *Config) ValidateShopifyConfig() error {
	if c.ShopifyDomain == "" {
		return fmt.Errorf("SHOPIFY_DOMAIN environment variable is required")
	}
	if c.ShopifyStorefrontToken == "" {
		return fmt.Errorf("SHOPIFY_STOREFRONT_TOKEN environment variable is required")
	}
	return nil
}

// ValidateStripeConfig checks if Stripe configuration is properly set.
func (c *Config) ValidateStripeConfig() error {
	if c.StripeSecretKey == "" {
		return fmt.Errorf("STRIPE_SECRET_KEY environment variable is required")
	}
	if c.StripePublishableKey == "" {
		return fmt.Errorf("STRIPE_PUBLISHABLE_KEY environment variable is required")
	}
	return nil
}

func getEnvOrDefault(key, defaultValue string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	return val
}
