package proxy

import (
	"context"
	"net/http"
	"strings"
	"time"
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

	return &Proxy{
		redis:    r,
		cache:    NewUserCache(),
		backends: b,
		jwtCache: NewJWTCache(),
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
	if isWebSocket(r) {
		IncWebSocketConnections()
	}

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache)
	if err != nil {
		IncAuthFailures()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	stickyKey := jwtData.UserID
	backend, err := p.cache.Get(stickyKey)
	if err != nil {
		// Local cache miss — try Redis.
		backend, err = p.redis.AssignBackend(
			context.Background(),
			stickyKey,
			p.backends.Hash(stickyKey),
		)
		if err != nil {
			// Redis also failed — this is a total cache miss.
			IncCacheMisses()
			IncBackendErrors()
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		// Redis had (or created) the mapping.
		IncCacheHitsRedis()
		p.cache.Set(stickyKey, backend)
	} else {
		// Local cache hit.
		IncCacheHitsLocal()
	}

	IncBackendRequests(backend)
	p.backends.ProxyRequest(w, r, backend)
}

// isWebSocket returns true if the request is a WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
