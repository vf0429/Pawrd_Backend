package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	MapsAPIKey                    string
	DBHost                        string
	DBPort                        string
	DBUser                        string
	DBPassword                    string
	DBName                        string
	PythonRAGBaseURL              string
	GoRAGBaseURL                  string
	PythonRAGTimeoutSeconds       int
	GoRAGTimeoutSeconds           int
	RAGLLMBaseURL                 string
	RAGLLMModel                   string
	RAGLLMAPIKey                  string
	RAGLLMTimeoutSeconds          int
	RAGRerankEnabled              bool
	RAGRerankBaseURL              string
	RAGRerankModel                string
	RAGRerankAPIKey               string
	RAGRerankTopN                 int
	RAGRerankTimeoutSeconds       int
	ChatRAGRuntime                string
	MerchantFacadeBaseURL         string
	MerchantFacadeAppKey          string
	ShopifyDomain                 string
	ShopifyStorefrontPrivateToken string
	ShopifyStorefrontToken        string
	UseMockShopify                bool
	HiCustomBaseURL               string
	HiCustomAppKey                string
	HiCustomAppSecret             string
	UseMockHiCustom               bool
	StripeSecretKey               string
	StripePublishableKey          string
	StripeWebhookSecret           string
}

func LoadConfig() *Config {
	_ = godotenv.Load() // Ignore error if .env doesn't exist
	mapsKey := os.Getenv("MAPS_API_KEY")

	return &Config{
		MapsAPIKey:                    mapsKey,
		DBHost:                        getEnvOrDefault("DB_HOST", "localhost"),
		DBPort:                        getEnvOrDefault("DB_PORT", "5432"),
		DBUser:                        getEnvOrDefault("DB_USER", "postgres"),
		DBPassword:                    getEnvOrDefault("DB_PASSWORD", "postgres"),
		DBName:                        getEnvOrDefault("DB_NAME", "pawrd"),
		PythonRAGBaseURL:              strings.TrimSpace(getEnvOrDefault("PYTHON_RAG_BASE_URL", "http://127.0.0.1:8098")),
		GoRAGBaseURL:                  strings.TrimSpace(getEnvOrDefault("GO_RAG_BASE_URL", "http://127.0.0.1:8012/api/rag/go")),
		PythonRAGTimeoutSeconds:       getEnvAsIntOrDefault("PYTHON_RAG_TIMEOUT_SECONDS", 90),
		GoRAGTimeoutSeconds:           getEnvAsIntOrDefault("GO_RAG_TIMEOUT_SECONDS", 90),
		RAGLLMBaseURL:                 strings.TrimSpace(getEnvOrDefault("HK_INSURANCE_RAG_LLM_BASE_URL", "")),
		RAGLLMModel:                   strings.TrimSpace(getEnvOrDefault("HK_INSURANCE_RAG_LLM_MODEL", "")),
		RAGLLMAPIKey:                  strings.TrimSpace(os.Getenv("HK_INSURANCE_RAG_LLM_API_KEY")),
		RAGLLMTimeoutSeconds:          getEnvAsIntOrDefault("HK_INSURANCE_RAG_LLM_TIMEOUT_SECONDS", 45),
		RAGRerankEnabled:              getEnvAsBoolOrDefault("HK_INSURANCE_RAG_RERANK_ENABLED", false),
		RAGRerankBaseURL:              strings.TrimSpace(getEnvOrDefault("HK_INSURANCE_RAG_RERANK_BASE_URL", "")),
		RAGRerankModel:                strings.TrimSpace(getEnvOrDefault("HK_INSURANCE_RAG_RERANK_MODEL", "")),
		RAGRerankAPIKey:               strings.TrimSpace(os.Getenv("HK_INSURANCE_RAG_RERANK_API_KEY")),
		RAGRerankTopN:                 getEnvAsIntOrDefault("HK_INSURANCE_RAG_RERANK_TOP_N", 6),
		RAGRerankTimeoutSeconds:       getEnvAsIntOrDefault("HK_INSURANCE_RAG_RERANK_TIMEOUT_SECONDS", 20),
		ChatRAGRuntime:                strings.ToLower(strings.TrimSpace(getEnvOrDefault("CHAT_RAG_RUNTIME", "go"))),
		MerchantFacadeBaseURL:         strings.TrimSpace(getEnvOrDefault("MERCHANT_FACADE_BASE_URL", "http://127.0.0.1:8090")),
		MerchantFacadeAppKey:          strings.TrimSpace(os.Getenv("MERCHANT_FACADE_APP_KEY")),
		ShopifyDomain:                 strings.TrimSpace(os.Getenv("SHOPIFY_DOMAIN")),
		ShopifyStorefrontPrivateToken: strings.TrimSpace(os.Getenv("SHOPIFY_STOREFRONT_PRIVATE_TOKEN")),
		ShopifyStorefrontToken:        strings.TrimSpace(os.Getenv("SHOPIFY_STOREFRONT_TOKEN")),
		UseMockShopify:                os.Getenv("USE_MOCK_SHOPIFY") == "true",
		HiCustomBaseURL:               strings.TrimSpace(getEnvOrDefault("HICUSTOM_BASE_URL", "https://open.hicustom.com")),
		HiCustomAppKey:                strings.TrimSpace(os.Getenv("HICUSTOM_APP_KEY")),
		HiCustomAppSecret:             strings.TrimSpace(os.Getenv("HICUSTOM_APP_SECRET")),
		UseMockHiCustom:               os.Getenv("USE_MOCK_HICUSTOM") == "true",
		StripeSecretKey:               strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		StripePublishableKey:          strings.TrimSpace(os.Getenv("STRIPE_PUBLISHABLE_KEY")),
		StripeWebhookSecret:           strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")),
	}
}

// ValidateShopifyConfig checks if Shopify configuration is properly set
func (c *Config) ValidateShopifyConfig() error {
	if c.ShopifyDomain == "" {
		return fmt.Errorf("SHOPIFY_DOMAIN environment variable is required")
	}
	if c.ShopifyStorefrontPrivateToken == "" && c.ShopifyStorefrontToken == "" {
		return fmt.Errorf("SHOPIFY_STOREFRONT_PRIVATE_TOKEN environment variable is required (SHOPIFY_STOREFRONT_TOKEN is supported only as a legacy fallback)")
	}
	return nil
}

// ValidateHiCustomConfig checks if HiCustom configuration is properly set.
func (c *Config) ValidateHiCustomConfig() error {
	if c.HiCustomAppKey == "" {
		return fmt.Errorf("HICUSTOM_APP_KEY environment variable is required")
	}
	if c.HiCustomAppSecret == "" {
		return fmt.Errorf("HICUSTOM_APP_SECRET environment variable is required")
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

func getEnvAsIntOrDefault(key string, defaultValue int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(val)
	if err != nil || parsed <= 0 {
		return defaultValue
	}
	return parsed
}

func getEnvAsBoolOrDefault(key string, defaultValue bool) bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if val == "" {
		return defaultValue
	}
	switch val {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}
