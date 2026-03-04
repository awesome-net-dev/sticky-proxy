package proxy

import (
	"testing"
	"time"
)

// newTestRedis creates a Redis struct with a circuit breaker but no live connection.
// Used only for testing CB state transitions and hash fallback.
func newTestRedis(threshold int, cooldown time.Duration) *Redis {
	r := &Redis{
		cbThreshold: threshold,
		cbCooldown:  cooldown,
	}
	r.cachedBackends.Store([]string(nil))
	return r
}

func TestRedisCB_ClosedByDefault(t *testing.T) {
	t.Parallel()
	r := newTestRedis(5, 30*time.Second)

	if r.isCBOpen() {
		t.Fatal("circuit breaker should be closed by default")
	}
}

func TestRedisCB_OpensAfterThreshold(t *testing.T) {
	t.Parallel()
	r := newTestRedis(3, 30*time.Second)

	for i := 0; i < 3; i++ {
		r.recordCBFailure()
	}

	if !r.isCBOpen() {
		t.Fatal("circuit breaker should be open after reaching threshold")
	}
}

func TestRedisCB_FallbackWhenOpen(t *testing.T) {
	t.Parallel()
	r := newTestRedis(1, 30*time.Second)
	r.cachedBackends.Store([]string{"http://b1:8080", "http://b2:8080"})

	// Open the circuit.
	r.recordCBFailure()

	fb := r.hashFallback(42)
	if fb == "" {
		t.Fatal("expected a fallback backend, got empty string")
	}
	if fb != "http://b1:8080" && fb != "http://b2:8080" {
		t.Fatalf("unexpected fallback %q", fb)
	}
}

func TestRedisCB_FallbackNoBackends(t *testing.T) {
	t.Parallel()
	r := newTestRedis(1, 30*time.Second)
	// No cached backends.

	fb := r.hashFallback(42)
	if fb != "" {
		t.Fatalf("expected empty fallback with no backends, got %q", fb)
	}
}

func TestRedisCB_ClosesOnSuccess(t *testing.T) {
	t.Parallel()
	r := newTestRedis(2, 30*time.Second)

	// Open the circuit.
	r.recordCBFailure()
	r.recordCBFailure()
	if !r.isCBOpen() {
		t.Fatal("circuit should be open")
	}

	// Simulate cooldown elapsed by backdating cbOpenedAt.
	r.cbMu.Lock()
	r.cbOpenedAt = time.Now().Add(-31 * time.Second)
	r.cbMu.Unlock()

	// isCBOpen transitions to half-open after cooldown.
	if r.isCBOpen() {
		t.Fatal("circuit should be half-open after cooldown")
	}

	// A success should close it.
	r.recordCBSuccess()
	if r.isCBOpen() {
		t.Fatal("circuit should be closed after success")
	}

	r.cbMu.Lock()
	state := r.cbState
	r.cbMu.Unlock()
	if state != cbClosed {
		t.Fatalf("expected cbClosed, got %d", state)
	}
}

func TestRedisCB_HalfOpenAfterCooldown(t *testing.T) {
	t.Parallel()
	r := newTestRedis(1, 50*time.Millisecond)

	r.recordCBFailure()
	if !r.isCBOpen() {
		t.Fatal("circuit should be open")
	}

	// Wait for cooldown.
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open.
	if r.isCBOpen() {
		t.Fatal("circuit should be half-open after cooldown")
	}

	r.cbMu.Lock()
	state := r.cbState
	r.cbMu.Unlock()
	if state != cbHalfOpen {
		t.Fatalf("expected cbHalfOpen, got %d", state)
	}
}
