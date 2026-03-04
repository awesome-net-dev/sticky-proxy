package proxy

import (
	"errors"
	"sync"
	"time"
)

var ErrCacheMiss = errors.New("cache miss")

type cacheEntry struct {
	backend string
	exp     time.Time
}

type UserCache struct {
	ttl  time.Duration
	data sync.Map // map[string]*cacheEntry
}

func NewUserCache() *UserCache {
	return &UserCache{
		ttl: 24 * time.Hour,
	}
}

func (c *UserCache) Get(key string) (string, error) {
	v, ok := c.data.Load(key)
	if !ok {
		return "", ErrCacheMiss
	}

	entry := v.(*cacheEntry)
	if time.Now().After(entry.exp) {
		c.data.Delete(key)
		return "", ErrCacheMiss
	}

	return entry.backend, nil
}

func (c *UserCache) Set(key, backend string) {
	c.data.Store(key, &cacheEntry{
		backend: backend,
		exp:     time.Now().Add(c.ttl),
	})
}

// Invalidate deletes a specific user's sticky mapping from the local cache.
func (c *UserCache) Invalidate(userID string) {
	c.data.Delete(userID)
}

// InvalidateBackend scans all cached mappings and deletes every entry
// whose backend matches the given address. This is O(n) over the cache
// because sync.Map does not support lookup by value.
func (c *UserCache) InvalidateBackend(backend string) {
	c.data.Range(func(key, value any) bool {
		entry := value.(*cacheEntry)
		if entry.backend == backend {
			c.data.Delete(key)
		}
		return true
	})
}
