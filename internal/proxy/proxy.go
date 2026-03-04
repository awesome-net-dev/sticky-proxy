package proxy

import (
	"context"
	"log/slog"
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

	b := NewBackendManager(r)
	b.Start()

	slog.Info("proxy initialized")

	return &Proxy{
		redis:    r,
		cache:    NewUserCache(),
		backends: b,
		jwtCache: NewJWTCache(),
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&totalRequests, 1)

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache)
	if err != nil {
		slog.Warn("unauthorized request", "error", err, "path", r.URL.Path)
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
			slog.Error("failed to assign backend", "userId", stickyKey, "error", err)
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		p.cache.Set(stickyKey, backend)
		slog.Debug("assigned backend via redis", "userId", stickyKey, "backend", backend)
	} else {
		slog.Debug("cache hit", "userId", stickyKey, "backend", backend)
	}

	p.backends.ProxyRequest(w, r, backend)
}
