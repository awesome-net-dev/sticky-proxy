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
