package proxy

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockNotifier is a test CacheNotifier that delivers events from a channel.
type mockNotifier struct {
	events chan string
}

func (m *mockNotifier) Publish(_ context.Context, backend string) error {
	m.events <- backend
	return nil
}

func (m *mockNotifier) Subscribe(_ context.Context, onInvalidate func(backend string)) {
	for backend := range m.events {
		onInvalidate(backend)
	}
}

func TestSubscribeDebouncedNotifier(t *testing.T) {
	t.Parallel()

	cache := NewUserCache(time.Hour)
	t.Cleanup(cache.Stop)

	// Pre-populate cache with entries for two backends.
	cache.Set("user-1", "http://b1:8080")
	cache.Set("user-2", "http://b1:8080")
	cache.Set("user-3", "http://b2:8080")

	notifier := &mockNotifier{events: make(chan string, 16)}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		subscribeDebouncedNotifier(ctx, notifier, cache, 50*time.Millisecond)
	}()

	// Fire 5 events for the same backend in rapid succession.
	for range 5 {
		notifier.events <- "http://b1:8080"
	}

	// Wait for debounce window + margin.
	time.Sleep(150 * time.Millisecond)

	// b1 users should be invalidated.
	if _, err := cache.Get("user-1"); err != ErrCacheMiss {
		t.Error("expected user-1 to be invalidated")
	}
	if _, err := cache.Get("user-2"); err != ErrCacheMiss {
		t.Error("expected user-2 to be invalidated")
	}

	// b2 user should still be cached.
	if _, err := cache.Get("user-3"); err != nil {
		t.Error("expected user-3 to still be cached")
	}

	cancel()
	close(notifier.events)
	wg.Wait()
}
