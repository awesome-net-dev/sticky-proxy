package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

type BackendManager struct {
	failures sync.Map
}

func NewBackendManager(r *Redis) *BackendManager {
	return &BackendManager{}
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
	if !b.available(backend) {
		slog.Warn("backend unavailable, circuit open", "backend", backend)
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return
	}

	target, _ := url.Parse(backend)
	proxy := httputil.NewSingleHostReverseProxy(target)

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

	f.count++
	if f.count >= 3 {
		f.until = time.Now().Add(time.Minute)
		slog.Warn("backend circuit breaker opened", "backend", backend, "failureCount", f.count)
	}
}

func (b *BackendManager) available(backend string) bool {
	v, ok := b.failures.Load(backend)
	if !ok {
		return true
	}
	f := v.(*failure)

	if time.Now().After(f.until) {
		b.failures.Delete(backend)
		slog.Info("backend circuit breaker reset", "backend", backend)
		return true
	}
	return false
}

type failure struct {
	count int
	until time.Time
}
