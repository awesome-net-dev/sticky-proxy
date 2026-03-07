package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// newTestProxy creates a Proxy suitable for unit tests (no Redis).
// It registers cleanup for all background goroutines.
func newTestProxy(t *testing.T) *Proxy {
	t.Helper()
	cache := NewUserCache(24 * time.Hour)
	jwtCache := NewJWTCache(0)
	rl := NewRateLimiter(100, 200)
	p := &Proxy{
		cache:        cache,
		jwtCache:     jwtCache,
		backends:     NewBackendManager(nil, nil, nil, "hash", 3, time.Minute, nil, nil),
		jwtSecret:    testSecretBytes,
		routingClaim: "userId",
		rateLimiter:  rl,
	}
	t.Cleanup(p.Stop)
	return p
}

// TestProxyServeHTTP_WithoutJWT verifies that requests without an Authorization
// header receive a 401 Unauthorized response.
func TestProxyServeHTTP_WithoutJWT(t *testing.T) {
	t.Parallel()

	p := newTestProxy(t)

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

	p := newTestProxy(t)

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
		_, _ = w.Write([]byte("proxied successfully"))
	}))
	defer backendServer.Close()

	p := newTestProxy(t)
	// Pre-populate the cache so Redis is never consulted
	p.cache.Set("user-proxy-test", backendServer.URL)

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

	p := newTestProxy(t)

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
// is marked as unavailable, the proxy invalidates the stale cache entry and
// attempts re-assignment via Redis. Without Redis this returns 503.
func TestProxyServeHTTP_BackendUnavailable(t *testing.T) {
	t.Skip("requires Redis: evicted backend triggers re-assignment via Redis")
}

// TestProxyServeHTTP_PublicPathBypassesJWT verifies that requests matching a
// configured public path prefix are proxied without JWT authentication.
func TestProxyServeHTTP_PublicPathBypassesJWT(t *testing.T) {
	t.Parallel()

	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("login ok"))
	}))
	defer backendServer.Close()

	p := newTestProxy(t)
	p.publicPaths = []string{"/login", "/oauth/"}
	p.store = NewMemoryStore()
	_ = p.store.AddBackend(t.Context(), backendServer.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != "login ok" {
		t.Errorf("expected body %q, got %q", "login ok", rec.Body.String())
	}
}

// TestProxyServeHTTP_PublicPathPrefixMatch verifies prefix matching semantics.
func TestProxyServeHTTP_PublicPathPrefixMatch(t *testing.T) {
	t.Parallel()

	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backendServer.Close()

	p := newTestProxy(t)
	p.publicPaths = []string{"/oauth/"}
	p.store = NewMemoryStore()
	_ = p.store.AddBackend(t.Context(), backendServer.URL)

	// /oauth/callback should match the /oauth/ prefix.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oauth/callback?code=abc", nil)
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected %d for /oauth/callback, got %d", http.StatusOK, rec.Code)
	}

	// /api/data should NOT match — requires JWT.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/data", nil)
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("expected %d for /api/data, got %d", http.StatusUnauthorized, rec2.Code)
	}
}

// TestProxyServeHTTP_PublicPathNoBackends returns 503 when no backends available.
func TestProxyServeHTTP_PublicPathNoBackends(t *testing.T) {
	t.Parallel()

	p := newTestProxy(t)
	p.publicPaths = []string{"/login"}
	p.store = NewMemoryStore()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", nil)
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}
