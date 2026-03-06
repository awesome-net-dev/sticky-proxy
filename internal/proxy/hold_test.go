package proxy

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestHoldManager_WaitReturnsImmediatelyWhenNotInTransition(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(5 * time.Second)

	if h.Wait(context.Background(), "user-1") {
		t.Fatal("expected false for non-transitioning key")
	}
}

func TestHoldManager_WaitBlocksUntilClear(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(5 * time.Second)

	h.MarkTransition([]string{"user-1", "user-2"})

	var wg sync.WaitGroup
	results := make([]bool, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = h.Wait(t.Context(), "user-1")
	}()
	go func() {
		defer wg.Done()
		results[1] = h.Wait(t.Context(), "user-2")
	}()

	// Small delay to ensure goroutines are waiting.
	time.Sleep(20 * time.Millisecond)

	h.ClearTransition([]string{"user-1", "user-2"})
	wg.Wait()

	if !results[0] {
		t.Error("expected user-1 wait to return true")
	}
	if !results[1] {
		t.Error("expected user-2 wait to return true")
	}
}

func TestHoldManager_WaitTimesOut(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(50 * time.Millisecond)

	h.MarkTransition([]string{"user-1"})

	start := time.Now()
	result := h.Wait(t.Context(), "user-1")
	elapsed := time.Since(start)

	if result {
		t.Fatal("expected false on timeout")
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected at least 40ms wait, got %v", elapsed)
	}
}

func TestHoldManager_WaitRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(5 * time.Second)

	h.MarkTransition([]string{"user-1"})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	result := h.Wait(ctx, "user-1")
	if result {
		t.Fatal("expected false on context cancellation")
	}
}

func TestHoldManager_MarkIdempotent(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(5 * time.Second)

	h.MarkTransition([]string{"user-1"})
	h.MarkTransition([]string{"user-1"}) // should not create a new channel

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.Wait(t.Context(), "user-1")
	}()

	time.Sleep(20 * time.Millisecond)
	h.ClearTransition([]string{"user-1"})
	wg.Wait()
}

func TestHoldManager_ClearNonExistentKeyIsNoOp(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(time.Second)

	// Should not panic.
	h.ClearTransition([]string{"nonexistent"})
}

func TestHoldManager_MultipleWaitersOnSameKey(t *testing.T) {
	t.Parallel()
	h := NewHoldManager(5 * time.Second)

	h.MarkTransition([]string{"user-1"})

	const waiters = 5
	var wg sync.WaitGroup
	results := make([]bool, waiters)

	for i := range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.Wait(t.Context(), "user-1")
		}()
	}

	time.Sleep(20 * time.Millisecond)
	h.ClearTransition([]string{"user-1"})
	wg.Wait()

	for i, r := range results {
		if !r {
			t.Errorf("waiter %d: expected true", i)
		}
	}
}
