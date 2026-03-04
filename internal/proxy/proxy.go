package proxy

import (
	"context"
	"net/http"
	"sync/atomic"
)

type Proxy struct {
	redis    *Redis
	cache    *UserCache
	backends *BackendManager
	jwtCache *JWTCache
}

func New() (*Proxy, error) {
	r, err := NewRedis()
	if err != nil {
		return nil, err
	}

	cache := NewUserCache()
	b := NewBackendManager(r, cache)
	b.Start()

	return &Proxy{
		redis:    r,
		cache:    cache,
		backends: b,
		jwtCache: NewJWTCache(),
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&totalRequests, 1)

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	stickyKey := jwtData.UserID
	backend, err := p.cache.Get(stickyKey)
	if err == nil && !p.backends.Available(backend) {
		// Backend was cached but has been evicted; discard stale entry
		// so we fall through to Redis re-assignment below.
		p.cache.Invalidate(stickyKey)
		backend = ""
		err = ErrCacheMiss
	}

	if err != nil {
		backend, err = p.redis.AssignBackend(
			context.Background(),
			stickyKey,
			p.backends.Hash(stickyKey),
		)
		if err != nil {
			atomic.AddUint64(&backendErrors, 1)
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		p.cache.Set(stickyKey, backend)
	}

	p.backends.ProxyRequest(w, r, backend)
}
