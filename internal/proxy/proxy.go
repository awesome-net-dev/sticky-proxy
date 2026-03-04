package proxy

import (
	"context"
	"net/http"
	"sync/atomic"

	"sticky-proxy/internal/config"
)

type Proxy struct {
	redis    *Redis
	cache    *UserCache
	backends *BackendManager
	jwtCache *JWTCache
	jwtSecret []byte
}

func New(cfg *config.Config) (*Proxy, error) {
	r, err := NewRedis(cfg.RedisAddr, cfg.RedisPoolSize)
	if err != nil {
		return nil, err
	}

	b := NewBackendManager(r, cfg.EvictionThreshold, cfg.EvictionCooldown)
	b.Start()

	return &Proxy{
		redis:     r,
		cache:     NewUserCache(cfg.CacheTTL),
		backends:  b,
		jwtCache:  NewJWTCache(),
		jwtSecret: []byte(cfg.JWTSecret),
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&totalRequests, 1)

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache, p.jwtSecret)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
