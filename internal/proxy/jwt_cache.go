package proxy

import (
	"math/rand/v2"
	"sync"
	"time"
)

type CachedJWT struct {
	UserID   string
	OrgID    string
	TenantID string
	Exp      time.Time
}

type JWTCache struct {
	data    sync.Map // map[token]string -> CachedJWT
	maxSize int
	stopCh  chan struct{}
}

func NewJWTCache(maxSize int) *JWTCache {
	c := &JWTCache{
		maxSize: maxSize,
		stopCh:  make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

func (c *JWTCache) Get(token string) (*CachedJWT, bool) {
	v, ok := c.data.Load(token)
	if !ok {
		return nil, false
	}

	cached := v.(*CachedJWT)
	if time.Now().After(cached.Exp) {
		c.data.Delete(token)
		return nil, false
	}

	return cached, true
}

func (c *JWTCache) Set(token string, jwt *CachedJWT) {
	c.data.Store(token, jwt)
}

// sweep deletes expired entries, then random-evicts if over maxSize.
func (c *JWTCache) sweep() {
	now := time.Now()
	var keys []string

	c.data.Range(func(key, value any) bool {
		cached := value.(*CachedJWT)
		if now.After(cached.Exp) {
			c.data.Delete(key)
		} else {
			keys = append(keys, key.(string))
		}
		return true
	})

	if c.maxSize > 0 && len(keys) > c.maxSize {
		rand.Shuffle(len(keys), func(i, j int) {
			keys[i], keys[j] = keys[j], keys[i]
		})
		for _, k := range keys[c.maxSize:] {
			c.data.Delete(k)
		}
	}
}

func (c *JWTCache) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.sweep()
		case <-c.stopCh:
			return
		}
	}
}

// Stop terminates the background cleanup goroutine.
func (c *JWTCache) Stop() {
	close(c.stopCh)
}
