package proxy

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecretBytes = []byte("test-secret-key-for-unit-tests")

// makeToken creates a signed JWT with the given claims using HMAC-SHA256.
func makeToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := token.SignedString(testSecretBytes)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return s
}

func TestExtractUserIDFromJWT_ValidToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-123",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})

	result, err := extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "userId")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RoutingKey != "user-123" {
		t.Errorf("expected RoutingKey %q, got %q", "user-123", result.RoutingKey)
	}
}

func TestExtractUserIDFromJWT_ExpiredToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-expired",
		"exp":    float64(time.Now().Add(-time.Hour).Unix()),
	})

	_, err := extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "userId")
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestExtractUserIDFromJWT_WrongSigningMethod(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	// Create a token that claims to use RSA but is actually HMAC.
	// jwt.Parse should reject this because of the signing method check.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId": "user-rsa",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})
	// Tamper the header to claim RSA
	token.Header["alg"] = "RS256"
	tokenStr, err := token.SignedString(testSecretBytes)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	_, err = extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "userId")
	if err == nil {
		t.Fatal("expected error for wrong signing method, got nil")
	}
}

func TestExtractUserIDFromJWT_MissingRoutingClaim(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tokenStr := makeToken(t, jwt.MapClaims{
		"sub": "some-subject",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	_, err := extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "userId")
	if err == nil {
		t.Fatal("expected error when routing claim is missing, got nil")
	}
}

func TestExtractUserIDFromJWT_MalformedToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	_, err := extractUserIDFromJWT("Bearer not.a.valid.jwt.token", cache, testSecretBytes, "userId")
	if err == nil {
		t.Fatal("expected error for malformed token, got nil")
	}
}

func TestExtractUserIDFromJWT_EmptyToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	_, err := extractUserIDFromJWT("", cache, testSecretBytes, "userId")
	if err == nil {
		t.Fatal("expected error for empty auth header, got nil")
	}
}

func TestExtractUserIDFromJWT_InvalidAuthHeaderFormat(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tests := []struct {
		name   string
		header string
	}{
		{"no bearer prefix", "Token abc123"},
		{"only bearer", "Bearer"},
		{"empty string", ""},
		{"just spaces", "   "},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := extractUserIDFromJWT(tc.header, cache, testSecretBytes, "userId")
			if err == nil {
				t.Errorf("expected error for header %q, got nil", tc.header)
			}
		})
	}
}

func TestExtractUserIDFromJWT_CachesValidToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "cached-user",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})

	// First call parses the token
	result1, err := extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "userId")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call should hit the cache
	result2, err := extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "userId")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if result1.RoutingKey != result2.RoutingKey {
		t.Errorf("cached result differs: %q vs %q", result1.RoutingKey, result2.RoutingKey)
	}
}

func TestExtractUserIDFromJWT_CustomClaim(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tokenStr := makeToken(t, jwt.MapClaims{
		"sub":       "sub-value",
		"accountId": "acc-42",
		"exp":       float64(time.Now().Add(time.Hour).Unix()),
	})

	// Extract using "sub" claim
	result, err := extractUserIDFromJWT("Bearer "+tokenStr, cache, testSecretBytes, "sub")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RoutingKey != "sub-value" {
		t.Errorf("expected RoutingKey %q, got %q", "sub-value", result.RoutingKey)
	}

	// Use a different cache to avoid hitting the previous result
	cache2 := NewJWTCache(0)
	t.Cleanup(cache2.Stop)

	// Extract using "accountId" claim
	result2, err := extractUserIDFromJWT("Bearer "+tokenStr, cache2, testSecretBytes, "accountId")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result2.RoutingKey != "acc-42" {
		t.Errorf("expected RoutingKey %q, got %q", "acc-42", result2.RoutingKey)
	}
}
