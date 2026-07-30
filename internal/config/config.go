package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/wangwuxing777/Pawrd_Backend/internal/auth"
)

type Config struct {
	MapsAPIKey                    string
	JWTSecret                     string
	DatabaseURL                   string
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
	ShopifyStorefrontAPIVersion   string
	ShopifyStorefrontPrivateToken string
	ShopifyStorefrontToken        string
	ShopifyAdminAccessToken       string
	ShopifyClientID               string
	ShopifyClientSecret           string
	ShopifyAdminAPIVersion        string
	ShopifyWebhookSecret          string
	ShopifyWebhookCallbackURL     string
	ShopifyAutoRequestFulfillment bool
	UseMockShopify                bool
	ShopCheckoutEnabled           bool
	ShopCheckoutQuoteTTLSeconds   int
	HiCustomBaseURL               string
	HiCustomAppKey                string
	HiCustomAppSecret             string
	HiCustomEnabled               bool
	UseMockHiCustom               bool
	StripeSecretKey               string
	StripePublishableKey          string
	StripeWebhookSecret           string
	StripeLiveModeEnabled         bool
	ShopAdminKey                  string
}

func LoadConfig() *Config {
	_ = godotenv.Load() // Ignore error if .env doesn't exist
	mapsKey := os.Getenv("MAPS_API_KEY")

	return &Config{
		MapsAPIKey:                    mapsKey,
		JWTSecret:                     strings.TrimSpace(os.Getenv("JWT_SECRET")),
		DatabaseURL:                   strings.TrimSpace(os.Getenv("DATABASE_URL")),
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
		ShopifyStorefrontAPIVersion:   strings.TrimSpace(getEnvOrDefault("SHOPIFY_STOREFRONT_API_VERSION", "2026-07")),
		ShopifyStorefrontPrivateToken: strings.TrimSpace(os.Getenv("SHOPIFY_STOREFRONT_PRIVATE_TOKEN")),
		ShopifyStorefrontToken:        strings.TrimSpace(os.Getenv("SHOPIFY_STOREFRONT_TOKEN")),
		ShopifyAdminAccessToken:       strings.TrimSpace(os.Getenv("SHOPIFY_ADMIN_ACCESS_TOKEN")),
		ShopifyClientID:               strings.TrimSpace(os.Getenv("SHOPIFY_CLIENT_ID")),
		ShopifyClientSecret:           strings.TrimSpace(os.Getenv("SHOPIFY_CLIENT_SECRET")),
		ShopifyAdminAPIVersion:        strings.TrimSpace(getEnvOrDefault("SHOPIFY_ADMIN_API_VERSION", "2026-07")),
		ShopifyWebhookSecret:          strings.TrimSpace(os.Getenv("SHOPIFY_WEBHOOK_SECRET")),
		ShopifyWebhookCallbackURL:     strings.TrimSpace(os.Getenv("SHOPIFY_WEBHOOK_CALLBACK_URL")),
		ShopifyAutoRequestFulfillment: getEnvAsBoolOrDefault("SHOPIFY_AUTO_REQUEST_FULFILLMENT", false),
		UseMockShopify:                os.Getenv("USE_MOCK_SHOPIFY") == "true",
		ShopCheckoutEnabled:           getEnvAsBoolOrDefault("SHOP_CHECKOUT_ENABLED", false),
		ShopCheckoutQuoteTTLSeconds:   getEnvAsIntOrDefault("SHOP_CHECKOUT_QUOTE_TTL_SECONDS", 600),
		HiCustomBaseURL:               strings.TrimSpace(getEnvOrDefault("HICUSTOM_BASE_URL", "https://open.hicustom.com")),
		HiCustomAppKey:                strings.TrimSpace(os.Getenv("HICUSTOM_APP_KEY")),
		HiCustomAppSecret:             strings.TrimSpace(os.Getenv("HICUSTOM_APP_SECRET")),
		HiCustomEnabled:               getEnvAsBoolOrDefault("HICUSTOM_ENABLED", false),
		UseMockHiCustom:               os.Getenv("USE_MOCK_HICUSTOM") == "true",
		StripeSecretKey:               strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")),
		StripePublishableKey:          strings.TrimSpace(os.Getenv("STRIPE_PUBLISHABLE_KEY")),
		StripeWebhookSecret:           strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")),
		StripeLiveModeEnabled:         getEnvAsBoolOrDefault("STRIPE_LIVE_MODE_ENABLED", false),
		ShopAdminKey:                  strings.TrimSpace(os.Getenv("SHOP_ADMIN_KEY")),
	}
}

// ValidateAuthConfig ensures every authenticated endpoint uses a private,
// sufficiently strong signing key. It is a process-wide startup requirement,
// not only a checkout requirement.
func (c *Config) ValidateAuthConfig() error {
	if err := auth.ValidateJWTSecret(c.JWTSecret); err != nil {
		return err
	}
	return nil
}

// ValidateShopOperationalSecurity validates any configured secret that can
// mutate an existing order, even when new checkout is disabled. Empty values
// deliberately leave the corresponding operator/webhook endpoint unavailable.
func (c *Config) ValidateShopOperationalSecurity() error {
	if err := validateOptionalSecret("SHOP_ADMIN_KEY", c.ShopAdminKey, 32, ""); err != nil {
		return err
	}
	if err := validateOptionalSecret("STRIPE_WEBHOOK_SECRET", c.StripeWebhookSecret, 24, "whsec_"); err != nil {
		return err
	}
	if err := validateOptionalSecret("SHOPIFY_WEBHOOK_SECRET", c.ShopifyWebhookSecret, 24, ""); err != nil {
		return err
	}
	if strings.TrimSpace(c.ShopifyWebhookCallbackURL) != "" {
		if err := validateShopifyWebhookCallbackURL(c.ShopifyWebhookCallbackURL); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) ValidateShopifyAdminConfig() error {
	if c.ShopifyDomain == "" {
		return fmt.Errorf("SHOPIFY_DOMAIN environment variable is required")
	}
	hasClientID := c.ShopifyClientID != ""
	hasClientSecret := c.ShopifyClientSecret != ""
	if hasClientID != hasClientSecret {
		return fmt.Errorf("SHOPIFY_CLIENT_ID and SHOPIFY_CLIENT_SECRET must be configured together")
	}
	if !hasClientID && c.ShopifyAdminAccessToken == "" {
		return fmt.Errorf("Shopify Admin credentials are required: configure SHOPIFY_CLIENT_ID and SHOPIFY_CLIENT_SECRET, or legacy SHOPIFY_ADMIN_ACCESS_TOKEN")
	}
	if c.ShopifyAdminAPIVersion == "" {
		return fmt.Errorf("SHOPIFY_ADMIN_API_VERSION environment variable is required")
	}
	return nil
}

// ValidateShopifyConfig checks if Shopify configuration is properly set
func (c *Config) ValidateShopifyConfig() error {
	if c.ShopifyDomain == "" {
		return fmt.Errorf("SHOPIFY_DOMAIN environment variable is required")
	}
	if c.ShopifyStorefrontPrivateToken == "" && c.ShopifyStorefrontToken == "" {
		return fmt.Errorf("SHOPIFY_STOREFRONT_PRIVATE_TOKEN environment variable is required (SHOPIFY_STOREFRONT_TOKEN is supported only as a legacy fallback)")
	}
	if strings.TrimSpace(c.ShopifyStorefrontAPIVersion) == "" {
		return fmt.Errorf("SHOPIFY_STOREFRONT_API_VERSION environment variable is required")
	}
	return nil
}

// ValidateHiCustomConfig checks if HiCustom configuration is properly set.
func (c *Config) ValidateHiCustomConfig() error {
	if !c.HiCustomEnabled {
		return fmt.Errorf("HiCustom is disabled; set HICUSTOM_ENABLED=true only after its checkout and fulfillment pipeline is production-ready")
	}
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
	secretMode, err := stripeKeyMode(c.StripeSecretKey, "sk")
	if err != nil {
		return fmt.Errorf("STRIPE_SECRET_KEY %w", err)
	}
	publishableMode, err := stripeKeyMode(c.StripePublishableKey, "pk")
	if err != nil {
		return fmt.Errorf("STRIPE_PUBLISHABLE_KEY %w", err)
	}
	if secretMode != publishableMode {
		return fmt.Errorf("Stripe secret and publishable keys must use the same test/live mode")
	}
	expectedMode := "test"
	if c.StripeLiveModeEnabled {
		expectedMode = "live"
	}
	if secretMode != expectedMode {
		return fmt.Errorf(
			"Stripe keys are %s mode but STRIPE_LIVE_MODE_ENABLED requires %s mode",
			secretMode,
			expectedMode,
		)
	}
	return nil
}

func stripeKeyMode(key, prefix string) (string, error) {
	key = strings.TrimSpace(key)
	switch {
	case strings.HasPrefix(key, prefix+"_test_"):
		return "test", nil
	case strings.HasPrefix(key, prefix+"_live_"):
		return "live", nil
	default:
		return "", fmt.Errorf("must start with %s_test_ or %s_live_", prefix, prefix)
	}
}

// ValidateShopCheckoutConfig guards the point where Pawrd can create a
// chargeable PaymentIntent. A catalog can operate without Admin/webhook
// credentials, but checkout must fail closed unless the complete Shopify and
// Stripe fulfillment path is configured.
func (c *Config) ValidateShopCheckoutConfig() error {
	if !c.ShopCheckoutEnabled {
		return fmt.Errorf("shop checkout is disabled; set SHOP_CHECKOUT_ENABLED=true only with production-ready payment infrastructure")
	}
	if err := validatePostgresDatabaseURL(c.DatabaseURL); err != nil {
		return err
	}
	if c.UseMockShopify {
		return fmt.Errorf("checkout is disabled while USE_MOCK_SHOPIFY=true")
	}
	if err := c.ValidateAuthConfig(); err != nil {
		return err
	}
	if err := c.ValidateShopOperationalSecurity(); err != nil {
		return err
	}
	if strings.TrimSpace(c.ShopAdminKey) == "" {
		return fmt.Errorf("SHOP_ADMIN_KEY is required before accepting checkout")
	}
	if err := c.ValidateShopifyConfig(); err != nil {
		return err
	}
	if err := c.ValidateShopifyAdminConfig(); err != nil {
		return err
	}
	if err := c.ValidateStripeConfig(); err != nil {
		return err
	}
	if strings.TrimSpace(c.StripeWebhookSecret) == "" {
		return fmt.Errorf("STRIPE_WEBHOOK_SECRET environment variable is required before accepting checkout")
	}
	if strings.TrimSpace(c.ShopifyWebhookSecret) == "" {
		return fmt.Errorf("SHOPIFY_WEBHOOK_SECRET environment variable is required before accepting checkout")
	}
	if strings.TrimSpace(c.ShopifyWebhookCallbackURL) == "" {
		return fmt.Errorf("SHOPIFY_WEBHOOK_CALLBACK_URL is required before accepting checkout")
	}
	return nil
}

func validateOptionalSecret(name, raw string, minLength int, requiredPrefix string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{
		"replace_me", "replace-with", "replace_with", "changeme", "change_me",
		"change-before", "placeholder", "example", "your_", "your-", "test-only",
		"test_only",
	} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("%s must not use a documented placeholder value", name)
		}
	}
	if len(value) < minLength {
		return fmt.Errorf("%s must contain at least %d characters", name, minLength)
	}
	if requiredPrefix != "" && !strings.HasPrefix(value, requiredPrefix) {
		return fmt.Errorf("%s must start with %s", name, requiredPrefix)
	}
	return nil
}

func validateShopifyWebhookCallbackURL(raw string) error {
	callbackURL := strings.TrimSpace(raw)
	parsedCallback, err := url.ParseRequestURI(callbackURL)
	if err != nil ||
		parsedCallback.Scheme != "https" ||
		parsedCallback.Host == "" ||
		parsedCallback.User != nil ||
		parsedCallback.RawQuery != "" ||
		parsedCallback.Fragment != "" {
		return fmt.Errorf("SHOPIFY_WEBHOOK_CALLBACK_URL must be a valid HTTPS URL without credentials, query, or fragment")
	}
	host := strings.ToLower(strings.TrimSpace(parsedCallback.Hostname()))
	ip := net.ParseIP(host)
	if host == "localhost" ||
		strings.HasSuffix(host, ".localhost") ||
		host == "example.com" ||
		strings.HasSuffix(host, ".example.com") ||
		strings.HasSuffix(host, ".example") ||
		strings.HasSuffix(host, ".invalid") ||
		(ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return fmt.Errorf("SHOPIFY_WEBHOOK_CALLBACK_URL must not use a local or reserved placeholder host")
	}
	if parsedCallback.Path != "/api/shop/webhooks/shopify" {
		return fmt.Errorf("SHOPIFY_WEBHOOK_CALLBACK_URL must end at /api/shop/webhooks/shopify")
	}
	return nil
}

func validatePostgresDatabaseURL(raw string) error {
	databaseURL := strings.TrimSpace(raw)
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required before accepting checkout")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Host == "" {
		return fmt.Errorf("DATABASE_URL must be a valid PostgreSQL URL before accepting checkout")
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
