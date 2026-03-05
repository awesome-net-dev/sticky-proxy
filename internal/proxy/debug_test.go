package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDebugRoutingHandler_MissingUser(t *testing.T) {
	t.Parallel()
	p := newTestProxy(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/debug/routing", nil)
	p.DebugRoutingHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestDebugRoutingHandler_LocalCacheHit(t *testing.T) {
	t.Parallel()
	p := newTestProxy(t)
	p.cache.Set("user-debug", "http://backend:8080")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/debug/routing?user=user-debug", nil)
	p.DebugRoutingHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rec.Code)
	}

	var resp debugRoutingResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.User != "user-debug" {
		t.Errorf("expected user %q, got %q", "user-debug", resp.User)
	}
	if resp.Backend != "http://backend:8080" {
		t.Errorf("expected backend %q, got %q", "http://backend:8080", resp.Backend)
	}
	if resp.CacheLayer != "local" {
		t.Errorf("expected cache_layer %q, got %q", "local", resp.CacheLayer)
	}
}

func TestDebugRoutingHandler_CacheMiss(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)
	jwtCache := NewJWTCache(0)
	rl := NewRateLimiter(100, 200)
	p := &Proxy{
		cache:        cache,
		jwtCache:     jwtCache,
		backends:     NewBackendManager(nil, nil, 3, time.Minute, nil),
		jwtSecret:    testSecretBytes,
		routingClaim: "userId",
		routingMode:  "hash",
		rateLimiter:  rl,
	}
	t.Cleanup(p.Stop)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/debug/routing?user=unknown", nil)
	p.DebugRoutingHandler(rec, req)

	var resp debugRoutingResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Backend != "" {
		t.Errorf("expected empty backend, got %q", resp.Backend)
	}
	if resp.CacheLayer != "none" {
		t.Errorf("expected cache_layer %q, got %q", "none", resp.CacheLayer)
	}
}
