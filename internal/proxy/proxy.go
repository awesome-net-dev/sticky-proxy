package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
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
	rateLimiter   *RateLimiter
}

func New(cfg *config.Config) (*Proxy, error) {
	r, err := NewRedis(cfg.RedisAddr, cfg.RedisPoolSize, cfg.RedisMinIdleConns, cfg.RedisCBThreshold, cfg.RedisCBCooldown)
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
		jwtCache:      NewJWTCache(cfg.JWTCacheMaxSize),
		jwtSecret:     []byte(cfg.JWTSecret),
		HealthChecker: NewHealthChecker(r),
		rateLimiter:   NewRateLimiter(100, 200),
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	IncRequests()
	IncActiveConnections()
	defer func() {
		DecActiveConnections()
		RecordRequestDuration(time.Since(start).Seconds())
	}()

	// Track WebSocket upgrade requests.
	isWS := isWebSocket(r)
	if isWS {
		IncWebSocketConnections()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache, p.jwtSecret)
	if err != nil {
		IncAuthFailures()
		slog.Warn("unauthorized request", "error", err, "path", r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if !p.rateLimiter.Allow(jwtData.UserID) {
		IncRateLimited()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	stickyKey := jwtData.UserID
	backend, err := p.cache.Get(stickyKey)
	if err == nil && !p.backends.Available(backend) {
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
			IncCacheMisses()
			IncBackendErrors()
			slog.Error("failed to assign backend", "userId", stickyKey, "error", err)
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		IncCacheHitsRedis()
		p.cache.Set(stickyKey, backend)
		slog.Debug("assigned backend via redis", "userId", stickyKey, "backend", backend)
	} else {
		IncCacheHitsLocal()
		slog.Debug("cache hit", "userId", stickyKey, "backend", backend)
	}

	if isWS {
		proxyWebSocket(w, r, backend)
		return
	}

	IncBackendRequests(backend)
	p.backends.ProxyRequest(w, r, backend)
}

// Stop gracefully shuts down background goroutines owned by the proxy.
func (p *Proxy) Stop() {
	p.jwtCache.Stop()
	p.cache.Stop()
	p.rateLimiter.Stop()
}

// isWebSocket returns true if the request is a WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
