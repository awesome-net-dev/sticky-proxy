package ownership

import (
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestChecker(rdb *redis.Client, addr string, onEvict func(string)) *Checker {
	c := New(rdb, addr, onEvict)
	c.interval = 100 * time.Millisecond
	return c
}

func TestTrackAndUntrack(t *testing.T) {
	c := &Checker{users: make(map[string]struct{})}

	c.Track("user1")
	c.Track("user2")
	if c.TrackedCount() != 2 {
		t.Fatalf("expected 2 tracked, got %d", c.TrackedCount())
	}

	c.Untrack("user1")
	if c.TrackedCount() != 1 {
		t.Fatalf("expected 1 tracked, got %d", c.TrackedCount())
	}

	// Duplicate track is safe.
	c.Track("user2")
	if c.TrackedCount() != 1 {
		t.Fatalf("expected 1 tracked after duplicate, got %d", c.TrackedCount())
	}
}

func TestCheckEvictsLostOwnership(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Skip("redis not available:", err)
	}

	// Clean up test keys.
	defer rdb.Del(t.Context(), "sticky:evict-test-user1", "sticky:evict-test-user2", "sticky:evict-test-user3")

	myAddr := "http://backend1:5678"

	// user1: owned by us
	rdb.Set(t.Context(), "sticky:evict-test-user1", myAddr, time.Minute)
	// user2: owned by someone else
	rdb.Set(t.Context(), "sticky:evict-test-user2", "http://backend2:5678", time.Minute)
	// user3: key missing (expired/deleted)

	var mu sync.Mutex
	evicted := make(map[string]bool)

	c := newTestChecker(rdb, myAddr, func(userID string) {
		mu.Lock()
		evicted[userID] = true
		mu.Unlock()
	})

	c.Track("evict-test-user1")
	c.Track("evict-test-user2")
	c.Track("evict-test-user3")

	// Run a single check pass.
	c.check()

	mu.Lock()
	defer mu.Unlock()

	if evicted["evict-test-user1"] {
		t.Error("user1 should NOT have been evicted (we own it)")
	}
	if !evicted["evict-test-user2"] {
		t.Error("user2 should have been evicted (different owner)")
	}
	if !evicted["evict-test-user3"] {
		t.Error("user3 should have been evicted (key missing)")
	}

	// user1 should still be tracked; user2 and user3 should be untracked.
	if c.TrackedCount() != 1 {
		t.Errorf("expected 1 tracked user after eviction, got %d", c.TrackedCount())
	}
}

func TestCheckEmptyUsersIsNoop(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		t.Skip("redis not available:", err)
	}

	called := false
	c := newTestChecker(rdb, "http://backend1:5678", func(string) {
		called = true
	})

	c.check()

	if called {
		t.Error("onEvict should not have been called with no tracked users")
	}
}
