package proxy

import (
	"context"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type BackendManager struct {
	failures sync.Map
	cache    *UserCache
	redis    *Redis
}

func NewBackendManager(r *Redis, cache *UserCache) *BackendManager {
	return &BackendManager{
		cache: cache,
		redis: r,
	}
}

func (b *BackendManager) Start() {}

func (b *BackendManager) Hash(userID string) uint32 {
	return HashUser(userID)
}

func (b *BackendManager) ProxyRequest(
	w http.ResponseWriter,
	r *http.Request,
	backend string,
) {
	if !b.Available(backend) {
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return
	}

	target, _ := url.Parse(backend)
	proxy := httputil.NewSingleHostReverseProxy(target)

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		b.recordFailure(backend)
		http.Error(w, "backend error", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}

func (b *BackendManager) recordFailure(backend string) {
	v, _ := b.failures.LoadOrStore(backend, &failure{})
	f := v.(*failure)

	f.count++
	if f.count >= 3 {
		f.until = time.Now().Add(time.Minute)
		b.invalidateStickyMappings(backend)
	}
}

// invalidateStickyMappings clears all sticky session mappings for a backend
// that has been evicted, so users are re-assigned on the next request.
func (b *BackendManager) invalidateStickyMappings(backend string) {
	b.cache.InvalidateBackend(backend)

	if err := b.redis.InvalidateBackend(context.Background(), backend); err != nil {
		log.Printf("failed to invalidate redis mappings for %s: %v", backend, err)
	}
}

// Available reports whether a backend is currently accepting traffic.
// It returns false if the backend has been evicted due to repeated failures.
func (b *BackendManager) Available(backend string) bool {
	v, ok := b.failures.Load(backend)
	if !ok {
		return true
	}
	f := v.(*failure)

	if time.Now().After(f.until) {
		b.failures.Delete(backend)
		return true
	}
	return false
}

type failure struct {
	count int
	until time.Time
}
