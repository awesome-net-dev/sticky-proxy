package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHookClient_SendAssign(t *testing.T) {
	t.Parallel()

	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hooks/assign" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var payload hookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("failed to decode payload: %v", err)
		}
		if payload.User != "user-1" {
			t.Errorf("expected user %q, got %q", "user-1", payload.User)
		}
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := NewHookClient(5*time.Second, 0)
	hc.SendAssign(t.Context(), srv.URL, "user-1")

	if called.Load() != 1 {
		t.Errorf("expected hook to be called once, got %d", called.Load())
	}
}

func TestHookClient_SendUnassign(t *testing.T) {
	t.Parallel()

	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hooks/unassign" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := NewHookClient(5*time.Second, 0)
	hc.SendUnassign(t.Context(), srv.URL, "user-2")

	if called.Load() != 1 {
		t.Errorf("expected hook to be called once, got %d", called.Load())
	}
}

func TestHookClient_RetryOnFailure(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := NewHookClient(5*time.Second, 2) // 1 attempt + 2 retries = 3 total
	hc.SendAssign(t.Context(), srv.URL, "user-retry")

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestHookClient_TimeoutDoesNotBlock(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // slow backend
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	hc := NewHookClient(100*time.Millisecond, 0)

	start := time.Now()
	hc.SendAssign(t.Context(), srv.URL, "user-timeout")
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("hook should have timed out quickly, took %v", elapsed)
	}
}
