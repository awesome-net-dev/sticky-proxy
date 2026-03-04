package proxy

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-key-for-unit-tests"

func TestMain(m *testing.M) {
	// Set the JWT secret used by extractUserIDFromJWT
	os.Setenv("JWT_SECRET", testSecret)
	jwtSecret = []byte(testSecret)
	os.Exit(m.Run())
}

// makeToken creates a signed JWT with the given claims using HMAC-SHA256.
func makeToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return s
}

func TestExtractUserIDFromJWT_ValidToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-123",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})

	result, err := extractUserIDFromJWT("Bearer "+tokenStr, cache)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.UserID != "user-123" {
		t.Errorf("expected UserID %q, got %q", "user-123", result.UserID)
	}
}

func TestExtractUserIDFromJWT_ExpiredToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-expired",
		"exp":    float64(time.Now().Add(-time.Hour).Unix()),
	})

	_, err := extractUserIDFromJWT("Bearer "+tokenStr, cache)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestExtractUserIDFromJWT_WrongSigningMethod(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	// Create a token that claims to use RSA but is actually HMAC.
	// jwt.Parse should reject this because of the signing method check.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId": "user-rsa",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})
	// Tamper the header to claim RSA
	token.Header["alg"] = "RS256"
	tokenStr, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	_, err = extractUserIDFromJWT("Bearer "+tokenStr, cache)
	if err == nil {
		t.Fatal("expected error for wrong signing method, got nil")
	}
}

func TestExtractUserIDFromJWT_MissingUserIDClaim(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	tokenStr := makeToken(t, jwt.MapClaims{
		"sub": "some-subject",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	})

	result, err := extractUserIDFromJWT("Bearer "+tokenStr, cache)
	if err != nil {
		t.Fatalf("expected no error (token itself is valid), got %v", err)
	}
	// userId claim is missing so it should be empty string
	if result.UserID != "" {
		t.Errorf("expected empty UserID when claim is missing, got %q", result.UserID)
	}
}

func TestExtractUserIDFromJWT_MalformedToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	_, err := extractUserIDFromJWT("Bearer not.a.valid.jwt.token", cache)
	if err == nil {
		t.Fatal("expected error for malformed token, got nil")
	}
}

func TestExtractUserIDFromJWT_EmptyToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	_, err := extractUserIDFromJWT("", cache)
	if err == nil {
		t.Fatal("expected error for empty auth header, got nil")
	}
}

func TestExtractUserIDFromJWT_InvalidAuthHeaderFormat(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

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
			_, err := extractUserIDFromJWT(tc.header, cache)
			if err == nil {
				t.Errorf("expected error for header %q, got nil", tc.header)
			}
		})
	}
}

func TestExtractUserIDFromJWT_CachesValidToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "cached-user",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})

	// First call parses the token
	result1, err := extractUserIDFromJWT("Bearer "+tokenStr, cache)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call should hit the cache
	result2, err := extractUserIDFromJWT("Bearer "+tokenStr, cache)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if result1.UserID != result2.UserID {
		t.Errorf("cached result differs: %q vs %q", result1.UserID, result2.UserID)
	}
}
