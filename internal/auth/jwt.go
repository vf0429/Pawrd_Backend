package auth

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const jwtIssuer = "pawrd"

// Claims is the payload stored inside every Pawrd JWT.
type Claims struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	jwt.RegisteredClaims
}

// ValidateJWTSecret rejects absent, short, and known placeholder signing keys.
// Tests must explicitly inject JWT_SECRET; production must never silently use a
// public development key.
func ValidateJWTSecret(value string) error {
	secret := strings.TrimSpace(value)
	if len(secret) < 32 {
		return fmt.Errorf("JWT_SECRET must contain at least 32 characters")
	}
	switch strings.ToLower(secret) {
	case "pawrd-dev-secret-change-before-production",
		"change-this-jwt-secret-before-production",
		"replace-with-a-long-random-jwt-secret",
		"replace_with_at_least_32_random_characters",
		"your-jwt-secret-change-before-production":
		return fmt.Errorf("JWT_SECRET must not use a public placeholder or development default")
	}
	return nil
}

func secret() ([]byte, error) {
	value := strings.TrimSpace(os.Getenv("JWT_SECRET"))
	if err := ValidateJWTSecret(value); err != nil {
		return nil, err
	}
	return []byte(value), nil
}

// GenerateToken mints a 1-year JWT for the given user.
// 1-year expiry = stay logged in like Instagram / 小红书 — no forced re-login.
func GenerateToken(userID, email, name string) (string, error) {
	signingKey, err := secret()
	if err != nil {
		return "", err
	}
	claims := Claims{
		UserID: userID,
		Email:  email,
		Name:   name,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(365 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    jwtIssuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(signingKey)
}

// ValidateToken parses and verifies a JWT string, returning its claims.
func ValidateToken(tokenString string) (*Claims, error) {
	signingKey, err := secret()
	if err != nil {
		return nil, err
	}
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return signingKey, nil
	}, jwt.WithIssuer(jwtIssuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}
