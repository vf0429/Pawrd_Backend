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
