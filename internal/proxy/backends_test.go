package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBackendManager_AvailableByDefault(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	if !bm.Available("http://backend:8080") {
		t.Fatal("new backend should be available by default")
	}
}

func TestBackendManager_UnavailableAfterFailures(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	backend := "http://failing-backend:8080"

	// Record 3 failures (the threshold in the code)
	bm.recordFailure(backend)
	bm.recordFailure(backend)
	bm.recordFailure(backend)

	if bm.Available(backend) {
		t.Fatal("backend should be unavailable after 3 failures")
	}
}

func TestBackendManager_StillAvailableBeforeThreshold(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	backend := "http://partial-fail:8080"

	// Record fewer than 3 failures
	bm.recordFailure(backend)
	bm.recordFailure(backend)

	// The failure struct has count=2, but until is zero-value (before Now()),
	// so available() will delete the entry and return true.
	// This is the actual behavior of the code.
	if !bm.Available(backend) {
		t.Fatal("backend should still be available before reaching threshold")
	}
}

func TestBackendManager_RecoveryAfterCooldown(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	backend := "http://recover-backend:8080"

	// Reach the failure threshold
	bm.recordFailure(backend)
	bm.recordFailure(backend)
	bm.recordFailure(backend)

	if bm.Available(backend) {
		t.Fatal("backend should be unavailable immediately after failures")
	}

	// Manually set the cooldown to expire in the past
	v, ok := bm.failures.Load(backend)
	if !ok {
		t.Fatal("failure entry should exist")
	}
	f := v.(*failure)
	f.mu.Lock()
	f.until = time.Now().Add(-time.Second)
	f.mu.Unlock()

	if !bm.Available(backend) {
		t.Fatal("backend should be available after cooldown expires")
	}
}

func TestBackendManager_ProxyRequest_Success(t *testing.T) {
	t.Parallel()

	// Create a test backend server
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from backend"))
	}))
	defer backendServer.Close()

	bm := NewBackendManager(nil, nil, 3, time.Minute)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)

	bm.ProxyRequest(rec, req, backendServer.URL)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != "hello from backend" {
		t.Errorf("expected body %q, got %q", "hello from backend", rec.Body.String())
	}
}

func TestBackendManager_ProxyRequest_UnavailableBackend(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	backend := "http://unavailable-backend:8080"

	// Make backend unavailable
	bm.recordFailure(backend)
	bm.recordFailure(backend)
	bm.recordFailure(backend)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)

	bm.ProxyRequest(rec, req, backend)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestBackendManager_ProxyRequest_BackendError(t *testing.T) {
	t.Parallel()

	// Create a server that immediately closes the connection to simulate an error.
	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close the connection to trigger a proxy error
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack failed: %v", err)
		}
		conn.Close()
	}))
	defer backendServer.Close()

	bm := NewBackendManager(nil, nil, 3, time.Minute)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)

	bm.ProxyRequest(rec, req, backendServer.URL)

	// The error handler should have been triggered
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected status %d, got %d", http.StatusBadGateway, rec.Code)
	}
}

func TestBackendManager_Hash(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	h1 := bm.Hash("user-1")
	h2 := bm.Hash("user-1")
	h3 := bm.Hash("user-2")

	if h1 != h2 {
		t.Errorf("Hash should be deterministic: %d != %d", h1, h2)
	}
	if h1 == h3 {
		t.Error("different users should produce different hashes")
	}
}

func TestBackendManager_MultipleBackendFailures(t *testing.T) {
	t.Parallel()
	bm := NewBackendManager(nil, nil, 3, time.Minute)

	backend1 := "http://backend-1:8080"
	backend2 := "http://backend-2:8080"

	// Fail backend1 but not backend2
	bm.recordFailure(backend1)
	bm.recordFailure(backend1)
	bm.recordFailure(backend1)

	if bm.Available(backend1) {
		t.Fatal("backend1 should be unavailable")
	}
	if !bm.Available(backend2) {
		t.Fatal("backend2 should still be available")
	}
}
