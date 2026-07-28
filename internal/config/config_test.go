package config

import (
	"strings"
	"testing"
)

func TestValidateShopifyAdminConfigAcceptsClientCredentials(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:          "pawrd.myshopify.com",
		ShopifyClientID:        "client-id",
		ShopifyClientSecret:    "client-secret",
		ShopifyAdminAPIVersion: "2026-07",
	}
	if err := cfg.ValidateShopifyAdminConfig(); err != nil {
		t.Fatalf("expected client credentials to be accepted: %v", err)
	}
}

func TestValidateShopifyAdminConfigAcceptsLegacyToken(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:           "pawrd.myshopify.com",
		ShopifyAdminAccessToken: "legacy-token",
		ShopifyAdminAPIVersion:  "2026-07",
	}
	if err := cfg.ValidateShopifyAdminConfig(); err != nil {
		t.Fatalf("expected legacy token to be accepted: %v", err)
	}
}

func TestValidateShopifyAdminConfigRejectsPartialClientCredentials(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:          "pawrd.myshopify.com",
		ShopifyClientID:        "client-id",
		ShopifyAdminAPIVersion: "2026-07",
	}
	err := cfg.ValidateShopifyAdminConfig()
	if err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("expected partial credentials error, got %v", err)
	}
}

func TestValidateShopifyConfigAcceptsPrivateToken(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontAPIVersion:   "2026-07",
		ShopifyStorefrontPrivateToken: "private-token",
	}
	if err := cfg.ValidateShopifyConfig(); err != nil {
		t.Fatalf("ValidateShopifyConfig() error = %v", err)
	}
}

func TestValidateShopifyConfigAcceptsLegacyPublicToken(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:               "example.myshopify.com",
		ShopifyStorefrontAPIVersion: "2026-07",
		ShopifyStorefrontToken:      "public-token",
	}
	if err := cfg.ValidateShopifyConfig(); err != nil {
		t.Fatalf("ValidateShopifyConfig() error = %v", err)
	}
}

func TestValidateShopifyConfigRequiresToken(t *testing.T) {
	cfg := &Config{ShopifyDomain: "example.myshopify.com", ShopifyStorefrontAPIVersion: "2026-07"}
	if err := cfg.ValidateShopifyConfig(); err == nil {
		t.Fatal("ValidateShopifyConfig() error = nil, want missing token error")
	}
}

func TestLoadConfigKeepsDangerousShopFeaturesDisabledByDefault(t *testing.T) {
	for _, key := range []string{
		"HICUSTOM_ENABLED",
		"SHOPIFY_AUTO_REQUEST_FULFILLMENT",
		"SHOPIFY_STOREFRONT_API_VERSION",
		"SHOP_CHECKOUT_ENABLED",
		"SHOP_CHECKOUT_QUOTE_TTL_SECONDS",
		"STRIPE_LIVE_MODE_ENABLED",
		"DATABASE_URL",
	} {
		t.Setenv(key, "")
	}

	cfg := LoadConfig()

	if cfg.HiCustomEnabled {
		t.Fatal("HiCustomEnabled = true, want safe default false")
	}
	if cfg.ShopifyAutoRequestFulfillment {
		t.Fatal("ShopifyAutoRequestFulfillment = true, want safe default false")
	}
	if cfg.ShopCheckoutEnabled {
		t.Fatal("ShopCheckoutEnabled = true, want safe default false")
	}
	if cfg.ShopifyStorefrontAPIVersion != "2026-07" {
		t.Fatalf("ShopifyStorefrontAPIVersion = %q, want 2026-07", cfg.ShopifyStorefrontAPIVersion)
	}
	if cfg.ShopCheckoutQuoteTTLSeconds != 600 {
		t.Fatalf("ShopCheckoutQuoteTTLSeconds = %d, want 600", cfg.ShopCheckoutQuoteTTLSeconds)
	}
	if cfg.StripeLiveModeEnabled {
		t.Fatal("StripeLiveModeEnabled = true, want safe default false")
	}
}

func TestValidateHiCustomConfigFailsClosedWhileDisabled(t *testing.T) {
	cfg := &Config{
		HiCustomAppKey:    "configured-key",
		HiCustomAppSecret: "configured-secret",
	}
	err := cfg.ValidateHiCustomConfig()
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled gate error, got %v", err)
	}
}

func TestValidateShopCheckoutConfigRequiresWebhookSecrets(t *testing.T) {
	cfg := &Config{
		ShopCheckoutEnabled:           true,
		DatabaseURL:                   "postgres://pawrd:secret@db.example.com/pawrd",
		JWTSecret:                     "test-only-jwt-secret-at-least-32-characters",
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontAPIVersion:   "2026-07",
		ShopifyStorefrontPrivateToken: "storefront-token",
		ShopifyAdminAccessToken:       "admin-token",
		ShopifyAdminAPIVersion:        "2026-07",
		ShopifyWebhookCallbackURL:     "https://api.pawrd.com/api/shop/webhooks/shopify",
		StripeSecretKey:               "sk_test_example",
		StripePublishableKey:          "pk_test_example",
		ShopAdminKey:                  "0123456789abcdef0123456789abcdef",
	}

	err := cfg.ValidateShopCheckoutConfig()
	if err == nil || !strings.Contains(err.Error(), "STRIPE_WEBHOOK_SECRET") {
		t.Fatalf("expected Stripe webhook gate, got %v", err)
	}
	cfg.StripeWebhookSecret = "whsec_0123456789abcdef0123456789abcdef"
	err = cfg.ValidateShopCheckoutConfig()
	if err == nil || !strings.Contains(err.Error(), "SHOPIFY_WEBHOOK_SECRET") {
		t.Fatalf("expected Shopify webhook gate, got %v", err)
	}
	cfg.ShopifyWebhookSecret = "abcdef0123456789abcdef0123456789"
	if err := cfg.ValidateShopCheckoutConfig(); err != nil {
		t.Fatalf("expected complete checkout config, got %v", err)
	}
}

func TestValidateShopCheckoutConfigRequiresStrongJWTSecret(t *testing.T) {
	cfg := &Config{
		ShopCheckoutEnabled:           true,
		DatabaseURL:                   "postgres://pawrd:secret@db.example.com/pawrd",
		JWTSecret:                     "too-short",
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontAPIVersion:   "2026-07",
		ShopifyStorefrontPrivateToken: "storefront-token",
		ShopifyAdminAccessToken:       "admin-token",
		ShopifyAdminAPIVersion:        "2026-07",
		ShopifyWebhookSecret:          "abcdef0123456789abcdef0123456789",
		ShopifyWebhookCallbackURL:     "https://api.pawrd.com/api/shop/webhooks/shopify",
		StripeSecretKey:               "sk_test_example",
		StripePublishableKey:          "pk_test_example",
		StripeWebhookSecret:           "whsec_0123456789abcdef0123456789abcdef",
		ShopAdminKey:                  "0123456789abcdef0123456789abcdef",
	}
	err := cfg.ValidateShopCheckoutConfig()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("expected JWT signing-secret gate, got %v", err)
	}
}

func TestValidateShopCheckoutConfigRequiresRefundAdminKeyAndHTTPSCallback(t *testing.T) {
	cfg := &Config{
		ShopCheckoutEnabled:           true,
		DatabaseURL:                   "postgres://pawrd:secret@db.example.com/pawrd",
		JWTSecret:                     "test-only-jwt-secret-at-least-32-characters",
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontAPIVersion:   "2026-07",
		ShopifyStorefrontPrivateToken: "storefront-token",
		ShopifyAdminAccessToken:       "admin-token",
		ShopifyAdminAPIVersion:        "2026-07",
		ShopifyWebhookSecret:          "abcdef0123456789abcdef0123456789",
		StripeSecretKey:               "sk_test_example",
		StripePublishableKey:          "pk_test_example",
		StripeWebhookSecret:           "whsec_0123456789abcdef0123456789abcdef",
	}
	err := cfg.ValidateShopCheckoutConfig()
	if err == nil || !strings.Contains(err.Error(), "SHOP_ADMIN_KEY") {
		t.Fatalf("expected refund admin-key gate, got %v", err)
	}

	cfg.ShopAdminKey = "0123456789abcdef0123456789abcdef"
	cfg.ShopifyWebhookCallbackURL = "http://api.example.com/api/shop/webhooks/shopify"
	err = cfg.ValidateShopCheckoutConfig()
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS callback gate, got %v", err)
	}

	cfg.ShopifyWebhookCallbackURL = "https://api.pawrd.com/api/shop/webhooks/shopify"
	if err := cfg.ValidateShopCheckoutConfig(); err != nil {
		t.Fatalf("expected complete fail-closed checkout config, got %v", err)
	}
}

func TestValidateShopOperationalSecurityRejectsPublicPlaceholdersWhenCheckoutIsDisabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{
			name: "admin key",
			cfg:  Config{ShopAdminKey: "replace_with_a_long_random_secret"},
		},
		{
			name: "Stripe webhook",
			cfg:  Config{StripeWebhookSecret: "whsec_replace_me"},
		},
		{
			name: "Shopify webhook",
			cfg:  Config{ShopifyWebhookSecret: "replace_with_shopify_app_secret"},
		},
		{
			name: "callback host",
			cfg: Config{
				ShopifyWebhookCallbackURL: "https://api.example.com/api/shop/webhooks/shopify",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.ShopCheckoutEnabled {
				t.Fatal("test requires checkout to remain disabled")
			}
			if err := tc.cfg.ValidateShopOperationalSecurity(); err == nil {
				t.Fatal("public placeholder unexpectedly passed global shop operations validation")
			}
		})
	}
}

func TestValidateShopOperationalSecurityAllowsEmptyDisabledOperations(t *testing.T) {
	cfg := Config{}
	if err := cfg.ValidateShopOperationalSecurity(); err != nil {
		t.Fatalf("empty optional operations should remain unavailable, not fail startup: %v", err)
	}
}

func TestValidateAuthConfigRejectsMissingAndPublicDevelopmentSecrets(t *testing.T) {
	for _, secret := range []string{
		"",
		"too-short",
		"pawrd-dev-secret-change-before-production",
	} {
		cfg := &Config{JWTSecret: secret}
		if err := cfg.ValidateAuthConfig(); err == nil {
			t.Fatalf("ValidateAuthConfig(%q) error = nil, want fail closed", secret)
		}
	}

	cfg := &Config{JWTSecret: "test-only-jwt-secret-at-least-32-characters"}
	if err := cfg.ValidateAuthConfig(); err != nil {
		t.Fatalf("expected explicit strong test secret to pass: %v", err)
	}
}

func TestValidateShopCheckoutConfigRequiresExplicitEnableAndPostgres(t *testing.T) {
	cfg := &Config{}
	if err := cfg.ValidateShopCheckoutConfig(); err == nil || !strings.Contains(err.Error(), "SHOP_CHECKOUT_ENABLED") {
		t.Fatalf("expected explicit checkout enable gate, got %v", err)
	}

	cfg.ShopCheckoutEnabled = true
	if err := cfg.ValidateShopCheckoutConfig(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("expected durable database gate, got %v", err)
	}

	cfg.DatabaseURL = "file:local.db"
	if err := cfg.ValidateShopCheckoutConfig(); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("expected PostgreSQL URL gate, got %v", err)
	}
}

func TestValidateStripeConfigDefaultsToTestMode(t *testing.T) {
	cfg := &Config{
		StripeSecretKey:      "sk_test_example",
		StripePublishableKey: "pk_test_example",
	}
	if err := cfg.ValidateStripeConfig(); err != nil {
		t.Fatalf("expected test keys to be accepted by default: %v", err)
	}

	cfg.StripeSecretKey = "sk_live_example"
	cfg.StripePublishableKey = "pk_live_example"
	if err := cfg.ValidateStripeConfig(); err == nil || !strings.Contains(err.Error(), "STRIPE_LIVE_MODE_ENABLED") {
		t.Fatalf("expected live keys to be rejected while live mode is disabled, got %v", err)
	}
}

func TestValidateStripeConfigRequiresMatchingExplicitLiveMode(t *testing.T) {
	cfg := &Config{
		StripeSecretKey:       "sk_live_example",
		StripePublishableKey:  "pk_live_example",
		StripeLiveModeEnabled: true,
	}
	if err := cfg.ValidateStripeConfig(); err != nil {
		t.Fatalf("expected explicit live mode to accept live keys: %v", err)
	}

	cfg.StripePublishableKey = "pk_test_example"
	if err := cfg.ValidateStripeConfig(); err == nil || !strings.Contains(err.Error(), "same test/live mode") {
		t.Fatalf("expected mixed Stripe key modes to fail, got %v", err)
	}

	cfg.StripeSecretKey = "sk_test_example"
	if err := cfg.ValidateStripeConfig(); err == nil || !strings.Contains(err.Error(), "requires live mode") {
		t.Fatalf("expected test keys to fail when live mode is enabled, got %v", err)
	}
}
