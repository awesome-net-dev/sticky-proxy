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
	}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&totalRequests, 1)

	authHeader := r.Header.Get("Authorization")
	jwtData, err := extractUserIDFromJWT(authHeader)
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
