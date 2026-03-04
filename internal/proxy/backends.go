package proxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type BackendManager struct {
	failures          sync.Map
	evictionThreshold int
	evictionCooldown  time.Duration
	transport         *http.Transport
	cache             *UserCache
	redis             *Redis
}

func NewBackendManager(r *Redis, cache *UserCache, evictionThreshold int, evictionCooldown time.Duration) *BackendManager {
	return &BackendManager{
		evictionThreshold: evictionThreshold,
		evictionCooldown:  evictionCooldown,
		cache:             cache,
		redis:             r,
		transport: &http.Transport{
			MaxIdleConns:          1000,
			MaxIdleConnsPerHost:   100,
			MaxConnsPerHost:       250,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
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
		slog.Warn("backend unavailable, circuit open", "backend", backend)
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return
	}

	target, _ := url.Parse(backend)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = b.transport

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		b.recordFailure(backend)
		slog.Error("backend proxy error", "backend", backend, "error", err)
		http.Error(w, "backend error", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}

func (b *BackendManager) recordFailure(backend string) {
	v, _ := b.failures.LoadOrStore(backend, &failure{})
	f := v.(*failure)

	f.mu.Lock()
	f.count++
	shouldEvict := f.count >= b.evictionThreshold
	if shouldEvict {
		f.until = time.Now().Add(b.evictionCooldown)
	}
	count := f.count
	f.mu.Unlock()

	if shouldEvict {
		slog.Warn("backend circuit breaker opened", "backend", backend, "failureCount", count)
		b.invalidateStickyMappings(backend)
	}
}

// invalidateStickyMappings clears all sticky session mappings for a backend
// that has been evicted, so users are re-assigned on the next request.
func (b *BackendManager) invalidateStickyMappings(backend string) {
	if b.cache != nil {
		b.cache.InvalidateBackend(backend)
	}

	if b.redis != nil {
		if err := b.redis.InvalidateBackend(context.Background(), backend); err != nil {
			slog.Error("failed to invalidate redis mappings", "backend", backend, "error", err)
		}
	}
}

// Available reports whether a backend is currently accepting traffic.
func (b *BackendManager) Available(backend string) bool {
	v, ok := b.failures.Load(backend)
	if !ok {
		return true
	}
	f := v.(*failure)

	f.mu.Lock()
	until := f.until
	f.mu.Unlock()

	if time.Now().After(until) {
		b.failures.Delete(backend)
		slog.Info("backend circuit breaker reset", "backend", backend)
		return true
	}
	return false
}

type failure struct {
	mu    sync.Mutex
	count int
	until time.Time
}
