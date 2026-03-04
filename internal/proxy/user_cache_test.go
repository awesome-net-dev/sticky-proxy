package proxy

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestUserCache_SetAndGet(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)

	cache.Set("user-1", "http://backend-1:8080")

	got, err := cache.Get("user-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "http://backend-1:8080" {
		t.Errorf("expected %q, got %q", "http://backend-1:8080", got)
	}
}

func TestUserCache_MissForUnknownUser(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)

	_, err := cache.Get("nonexistent-user")
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("expected ErrCacheMiss, got %v", err)
	}
}

func TestUserCache_ExpiredEntry(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)
	// Override the TTL to a very short duration for testing
	cache.ttl = time.Millisecond

	cache.Set("user-ttl", "http://backend-ttl:8080")

	// Wait for the entry to expire
	time.Sleep(5 * time.Millisecond)

	_, err := cache.Get("user-ttl")
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("expected ErrCacheMiss for expired entry, got %v", err)
	}
}

func TestUserCache_MultipleUsers(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)

	users := map[string]string{
		"user-a": "http://backend-a:8080",
		"user-b": "http://backend-b:8080",
		"user-c": "http://backend-c:8080",
	}

	for k, v := range users {
		cache.Set(k, v)
	}

	for k, want := range users {
		got, err := cache.Get(k)
		if err != nil {
			t.Errorf("user %s: unexpected error %v", k, err)
			continue
		}
		if got != want {
			t.Errorf("user %s: expected %q, got %q", k, want, got)
		}
	}
}

func TestUserCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			cache.Set("concurrent-user", "http://backend:8080")
		}(i)

		go func(id int) {
			defer wg.Done()
			cache.Get("concurrent-user")
		}(i)
	}

	wg.Wait()
	// If we get here without a data race, the test passes.
}

func TestUserCache_OverwriteExistingEntry(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)

	cache.Set("user-overwrite", "http://old-backend:8080")
	cache.Set("user-overwrite", "http://new-backend:8080")

	got, err := cache.Get("user-overwrite")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != "http://new-backend:8080" {
		t.Errorf("expected %q after overwrite, got %q", "http://new-backend:8080", got)
	}
}

func TestUserCache_ExpiredEntryIsDeleted(t *testing.T) {
	t.Parallel()
	cache := NewUserCache(24 * time.Hour)
	cache.ttl = time.Millisecond

	cache.Set("user-expire-del", "http://backend:8080")
	time.Sleep(5 * time.Millisecond)

	// First Get triggers deletion
	_, err := cache.Get("user-expire-del")
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("expected ErrCacheMiss, got %v", err)
	}

	// Second Get confirms deletion
	_, err = cache.Get("user-expire-del")
	if !errors.Is(err, ErrCacheMiss) {
		t.Fatalf("expected ErrCacheMiss after deletion, got %v", err)
	}
}
