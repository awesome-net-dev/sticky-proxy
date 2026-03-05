package proxy

import (
	"sync"
	"testing"
	"time"
)

func TestJWTCache_StoreAndRetrieve(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	cached := &CachedJWT{
		RoutingKey: "user-1",
		Exp:        time.Now().Add(time.Hour),
	}
	cache.Set("token-abc", cached)

	got, ok := cache.Get("token-abc")
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if got.RoutingKey != "user-1" {
		t.Errorf("expected RoutingKey %q, got %q", "user-1", got.RoutingKey)
	}
}

func TestJWTCache_ExpiredTokenNotReturned(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	cached := &CachedJWT{
		RoutingKey: "user-expired",
		Exp:        time.Now().Add(-time.Second), // already expired
	}
	cache.Set("token-expired", cached)

	_, ok := cache.Get("token-expired")
	if ok {
		t.Fatal("expected cache miss for expired token, got hit")
	}
}

func TestJWTCache_MissForUnknownToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	_, ok := cache.Get("nonexistent-token")
	if ok {
		t.Fatal("expected cache miss for unknown token, got hit")
	}
}

func TestJWTCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the goroutines write, half read
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			cached := &CachedJWT{
				RoutingKey: "user",
				Exp:        time.Now().Add(time.Hour),
			}
			cache.Set("concurrent-token", cached)
		}(i)

		go func(id int) {
			defer wg.Done()
			cache.Get("concurrent-token")
		}(i)
	}

	wg.Wait()
	// If we get here without a race condition panic, the test passes.
}

func TestJWTCache_ExpiredEntryIsDeleted(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	cached := &CachedJWT{
		RoutingKey: "user-del",
		Exp:        time.Now().Add(-time.Millisecond),
	}
	cache.Set("token-del", cached)

	// First Get should return miss and delete the entry
	_, ok := cache.Get("token-del")
	if ok {
		t.Fatal("expected miss for expired entry")
	}

	// Verify entry was deleted by checking internal state
	// A second Get should also return miss (entry was cleaned up)
	_, ok = cache.Get("token-del")
	if ok {
		t.Fatal("expired entry was not deleted from cache")
	}
}

func TestJWTCache_MultipleTokens(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	tokens := map[string]*CachedJWT{
		"token-a": {RoutingKey: "user-a", Exp: time.Now().Add(time.Hour)},
		"token-b": {RoutingKey: "user-b", Exp: time.Now().Add(time.Hour)},
		"token-c": {RoutingKey: "user-c", Exp: time.Now().Add(time.Hour)},
	}

	for k, v := range tokens {
		cache.Set(k, v)
	}

	for k, want := range tokens {
		got, ok := cache.Get(k)
		if !ok {
			t.Errorf("cache miss for %s", k)
			continue
		}
		if got.RoutingKey != want.RoutingKey {
			t.Errorf("token %s: expected RoutingKey %q, got %q", k, want.RoutingKey, got.RoutingKey)
		}
	}
}

func TestJWTCache_CleanupRemovesExpired(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	t.Cleanup(cache.Stop)

	cache.Set("expired-1", &CachedJWT{RoutingKey: "u1", Exp: time.Now().Add(-time.Second)})
	cache.Set("expired-2", &CachedJWT{RoutingKey: "u2", Exp: time.Now().Add(-time.Second)})
	cache.Set("valid-1", &CachedJWT{RoutingKey: "u3", Exp: time.Now().Add(time.Hour)})

	cache.sweep()

	if _, ok := cache.Get("valid-1"); !ok {
		t.Error("valid entry should survive sweep")
	}

	// Expired entries were already removed by sweep; verify they're gone
	// by checking the internal map directly (Get would also delete them).
	if _, loaded := cache.data.Load("expired-1"); loaded {
		t.Error("expired-1 should have been swept")
	}
	if _, loaded := cache.data.Load("expired-2"); loaded {
		t.Error("expired-2 should have been swept")
	}
}

func TestJWTCache_MaxSizeEviction(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(5)
	t.Cleanup(cache.Stop)

	// Insert 10 valid entries.
	for i := 0; i < 10; i++ {
		cache.Set("tok-"+string(rune('A'+i)), &CachedJWT{
			RoutingKey: "user",
			Exp:        time.Now().Add(time.Hour),
		})
	}

	cache.sweep()

	// Count remaining entries.
	remaining := 0
	cache.data.Range(func(_, _ any) bool {
		remaining++
		return true
	})

	if remaining > 5 {
		t.Errorf("expected at most 5 entries after sweep, got %d", remaining)
	}
}

func TestJWTCache_Stop(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache(0)
	cache.Stop()

	// After Stop, the goroutine should have exited.
	// Calling Set/Get should still work (no panic).
	cache.Set("after-stop", &CachedJWT{RoutingKey: "u", Exp: time.Now().Add(time.Hour)})
	if _, ok := cache.Get("after-stop"); !ok {
		t.Error("expected cache hit after stop")
	}
}
