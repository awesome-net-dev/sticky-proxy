package proxy

import (
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
	data sync.Map // map[token]string -> CachedJWT
}

func NewJWTCache() *JWTCache {
	return &JWTCache{}
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
