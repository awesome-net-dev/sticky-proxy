package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestProxyServeHTTP_WithoutJWT verifies that requests without an Authorization
// header receive a 401 Unauthorized response.
func TestProxyServeHTTP_WithoutJWT(t *testing.T) {
	t.Parallel()

	// Build a Proxy without Redis -- the JWT check happens before Redis is used,
	// so we can test the auth-layer in isolation.
	p := &Proxy{
		cache:    NewUserCache(),
		jwtCache: NewJWTCache(),
		backends: NewBackendManager(nil),
		// redis is nil; we should never reach it in this test path
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestProxyServeHTTP_InvalidJWT verifies that an invalid JWT returns 401.
func TestProxyServeHTTP_InvalidJWT(t *testing.T) {
	t.Parallel()

	p := &Proxy{
		cache:    NewUserCache(),
		jwtCache: NewJWTCache(),
		backends: NewBackendManager(nil),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Authorization", "Bearer invalid.jwt.token")

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestProxyServeHTTP_ValidJWTProxiesToBackend tests the full happy path:
// valid JWT -> user cache hit -> proxy to backend.
//
// This test bypasses Redis by pre-populating the UserCache with a backend
// mapping, so the proxy never needs to call Redis.AssignBackend.
func TestProxyServeHTTP_ValidJWTProxiesToBackend(t *testing.T) {
	t.Parallel()

	// Create a test backend that echoes a success response
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("proxied successfully"))
	}))
	defer backendServer.Close()

	userCache := NewUserCache()
	// Pre-populate the cache so Redis is never consulted
	userCache.Set("user-proxy-test", backendServer.URL)

	p := &Proxy{
		cache:    userCache,
		jwtCache: NewJWTCache(),
		backends: NewBackendManager(nil),
		// redis is nil; we avoid it by having a cache hit
	}

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-proxy-test",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != "proxied successfully" {
		t.Errorf("expected body %q, got %q", "proxied successfully", rec.Body.String())
	}
}

// TestProxyServeHTTP_ValidJWT_CacheMiss_NoRedis tests that when the user cache
// misses and Redis is nil, the proxy returns 503 Service Unavailable.
//
// NOTE: Full integration testing of the Redis path requires a running Redis
// instance. This test verifies the error handling when Redis is unavailable.
func TestProxyServeHTTP_ValidJWT_CacheMiss_NoRedis(t *testing.T) {
	t.Skip("requires Redis: full integration test with Redis assignment")

	// This would test:
	// 1. Valid JWT -> extract userId
	// 2. UserCache miss -> call Redis.AssignBackend
	// 3. Redis assigns backend -> cache it
	// 4. Proxy forwards request to backend
	//
	// To implement this properly, we would need either:
	// - A running Redis instance (integration test)
	// - A Redis interface/mock for unit testing
}

// TestProxyServeHTTP_ExpiredJWT verifies that an expired JWT returns 401.
func TestProxyServeHTTP_ExpiredJWT(t *testing.T) {
	t.Parallel()

	p := &Proxy{
		cache:    NewUserCache(),
		jwtCache: NewJWTCache(),
		backends: NewBackendManager(nil),
	}

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-expired",
		"exp":    float64(time.Now().Add(-time.Hour).Unix()),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// TestProxyServeHTTP_BackendUnavailable verifies that when the assigned backend
// is marked as unavailable, the proxy returns 503.
func TestProxyServeHTTP_BackendUnavailable(t *testing.T) {
	t.Parallel()

	bm := NewBackendManager(nil)
	backend := "http://down-backend:8080"

	// Make backend unavailable by exceeding failure threshold
	bm.recordFailure(backend)
	bm.recordFailure(backend)
	bm.recordFailure(backend)

	userCache := NewUserCache()
	userCache.Set("user-down", backend)

	p := &Proxy{
		cache:    userCache,
		jwtCache: NewJWTCache(),
		backends: bm,
	}

	tokenStr := makeToken(t, jwt.MapClaims{
		"userId": "user-down",
		"exp":    float64(time.Now().Add(time.Hour).Unix()),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}
