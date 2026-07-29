package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTRequiresExplicitStrongSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	if _, err := GenerateToken("user-1", "user@example.com", "User"); err == nil {
		t.Fatal("GenerateToken accepted a missing JWT_SECRET")
	}

	t.Setenv("JWT_SECRET", "pawrd-dev-secret-change-before-production")
	if _, err := GenerateToken("user-1", "user@example.com", "User"); err == nil {
		t.Fatal("GenerateToken accepted the public development JWT secret")
	}
}

func TestJWTValidatesIssuerAndHS256(t *testing.T) {
	secretValue := "test-only-jwt-secret-at-least-32-characters"
	t.Setenv("JWT_SECRET", secretValue)

	token, err := GenerateToken("user-1", "user@example.com", "User")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken(valid token) error = %v", err)
	}
	if claims.UserID != "user-1" || claims.Issuer != jwtIssuer {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	wrongIssuer := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "not-pawrd",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	wrongIssuerToken, err := wrongIssuer.SignedString([]byte(secretValue))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateToken(wrongIssuerToken); err == nil {
		t.Fatal("ValidateToken accepted a token from the wrong issuer")
	}

	wrongAlgorithm := jwt.NewWithClaims(jwt.SigningMethodHS384, Claims{
		UserID: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	wrongAlgorithmToken, err := wrongAlgorithm.SignedString([]byte(secretValue))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateToken(wrongAlgorithmToken); err == nil {
		t.Fatal("ValidateToken accepted a non-HS256 token")
	}
}
