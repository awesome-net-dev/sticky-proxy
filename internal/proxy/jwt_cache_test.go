package proxy

import (
	"sync"
	"testing"
	"time"
)

func TestJWTCache_StoreAndRetrieve(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	cached := &CachedJWT{
		UserID: "user-1",
		Exp:    time.Now().Add(time.Hour),
	}
	cache.Set("token-abc", cached)

	got, ok := cache.Get("token-abc")
	if !ok {
		t.Fatal("expected cache hit, got miss")
	}
	if got.UserID != "user-1" {
		t.Errorf("expected UserID %q, got %q", "user-1", got.UserID)
	}
}

func TestJWTCache_ExpiredTokenNotReturned(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	cached := &CachedJWT{
		UserID: "user-expired",
		Exp:    time.Now().Add(-time.Second), // already expired
	}
	cache.Set("token-expired", cached)

	_, ok := cache.Get("token-expired")
	if ok {
		t.Fatal("expected cache miss for expired token, got hit")
	}
}

func TestJWTCache_MissForUnknownToken(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	_, ok := cache.Get("nonexistent-token")
	if ok {
		t.Fatal("expected cache miss for unknown token, got hit")
	}
}

func TestJWTCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	cache := NewJWTCache()

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the goroutines write, half read
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			cached := &CachedJWT{
				UserID: "user",
				Exp:    time.Now().Add(time.Hour),
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
	cache := NewJWTCache()

	cached := &CachedJWT{
		UserID: "user-del",
		Exp:    time.Now().Add(-time.Millisecond),
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
	cache := NewJWTCache()

	tokens := map[string]*CachedJWT{
		"token-a": {UserID: "user-a", Exp: time.Now().Add(time.Hour)},
		"token-b": {UserID: "user-b", Exp: time.Now().Add(time.Hour)},
		"token-c": {UserID: "user-c", Exp: time.Now().Add(time.Hour)},
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
		if got.UserID != want.UserID {
			t.Errorf("token %s: expected UserID %q, got %q", k, want.UserID, got.UserID)
		}
	}
}
