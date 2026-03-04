package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"sticky-proxy/internal/config"
)

type Proxy struct {
	redis         *Redis
	cache         *UserCache
	backends      *BackendManager
	jwtCache      *JWTCache
	jwtSecret     []byte
	HealthChecker *HealthChecker
}

func New(cfg *config.Config) (*Proxy, error) {
	r, err := NewRedis(cfg.RedisAddr, cfg.RedisPoolSize)
	if err != nil {
		return nil, err
	}

	cache := NewUserCache(cfg.CacheTTL)
	b := NewBackendManager(r, cache, cfg.EvictionThreshold, cfg.EvictionCooldown)
	b.Start()

	slog.Info("proxy initialized")

	return &Proxy{
		redis:         r,
		cache:         cache,
		backends:      b,
		jwtCache:      NewJWTCache(),
		jwtSecret:     []byte(cfg.JWTSecret),
		HealthChecker: NewHealthChecker(r),
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&totalRequests, 1)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache, p.jwtSecret)
	if err != nil {
		slog.Warn("unauthorized request", "error", err, "path", r.URL.Path)
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
			ctx,
			stickyKey,
			p.backends.Hash(stickyKey),
		)
		if err != nil {
			atomic.AddUint64(&backendErrors, 1)
			slog.Error("failed to assign backend", "userId", stickyKey, "error", err)
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		p.cache.Set(stickyKey, backend)
		slog.Debug("assigned backend via redis", "userId", stickyKey, "backend", backend)
	} else {
		slog.Debug("cache hit", "userId", stickyKey, "backend", backend)
	}

	// WebSocket upgrade requests are handled separately because
	// httputil.ReverseProxy does not support the Upgrade handshake.
	if isWebSocketUpgrade(r) {
		proxyWebSocket(w, r, backend)
		return
	}

	p.backends.ProxyRequest(w, r, backend)
}
