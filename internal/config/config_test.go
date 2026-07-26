package config

import "testing"

func TestValidateShopifyConfigAcceptsPrivateToken(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:                 "example.myshopify.com",
		ShopifyStorefrontPrivateToken: "private-token",
	}

	if err := cfg.ValidateShopifyConfig(); err != nil {
		t.Fatalf("ValidateShopifyConfig() error = %v", err)
	}
}

func TestValidateShopifyConfigAcceptsLegacyPublicToken(t *testing.T) {
	cfg := &Config{
		ShopifyDomain:          "example.myshopify.com",
		ShopifyStorefrontToken: "public-token",
	}

	if err := cfg.ValidateShopifyConfig(); err != nil {
		t.Fatalf("ValidateShopifyConfig() error = %v", err)
	}
}

func TestValidateShopifyConfigRequiresToken(t *testing.T) {
	cfg := &Config{ShopifyDomain: "example.myshopify.com"}

	if err := cfg.ValidateShopifyConfig(); err == nil {
		t.Fatal("ValidateShopifyConfig() error = nil, want missing token error")
	}
}
