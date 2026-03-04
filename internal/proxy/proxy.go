package proxy

import (
	"context"
	"net/http"
	"sync/atomic"
)

type Proxy struct {
	redis       *Redis
	cache       *UserCache
	backends    *BackendManager
	jwtCache    *JWTCache
	rateLimiter *RateLimiter
}

func New() (*Proxy, error) {
	r, err := NewRedis()
	if err != nil {
		return nil, err
	}

	b := NewBackendManager(r)
	b.Start()

	return &Proxy{
		redis:       r,
		cache:       NewUserCache(),
		backends:    b,
		jwtCache:    NewJWTCache(),
		rateLimiter: NewRateLimiter(100, 200),
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

	if !p.rateLimiter.Allow(jwtData.UserID) {
		atomic.AddUint64(&rateLimited, 1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	stickyKey := jwtData.UserID
	backend, err := p.cache.Get(stickyKey)
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
