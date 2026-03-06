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
	store             Store
	redis             *Redis // optional, for hash-mode sticky key invalidation
	routingMode       string
	hooks             *HookClient
	notifier          CacheNotifier // optional, for cross-replica cache invalidation
}

func NewBackendManager(store Store, r *Redis, cache *UserCache, routingMode string, evictionThreshold int, evictionCooldown time.Duration, hooks *HookClient, notifier CacheNotifier) *BackendManager {
	return &BackendManager{
		evictionThreshold: evictionThreshold,
		evictionCooldown:  evictionCooldown,
		cache:             cache,
		store:             store,
		redis:             r,
		routingMode:       routingMode,
		hooks:             hooks,
		notifier:          notifier,
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
		go b.invalidateStickyMappings(backend)
	}
}

// invalidateStickyMappings clears all sticky session mappings for a backend
// that has been evicted, so users are re-assigned on the next request.
// If hooks are enabled, unassign hooks are sent before deleting mappings.
func (b *BackendManager) invalidateStickyMappings(backend string) {
	ctx := context.Background()

	if b.routingMode == "assignment" && b.store != nil {
		users, err := b.store.GetBackendUsers(ctx, backend)
		if err != nil {
			slog.Error("failed to get users for backend", "backend", backend, "error", err)
		} else if len(users) > 0 {
			if b.hooks != nil {
				b.hooks.SendUnassign(ctx, backend, users)
			}
			if delErr := b.store.BulkDeleteAssignments(ctx, users); delErr != nil {
				slog.Error("failed to delete assignments", "backend", backend, "error", delErr)
			}
		}
	} else if b.redis != nil {
		users, err := b.redis.GetUsersForBackend(ctx, backend)
		if err != nil {
			slog.Error("failed to get users for backend", "backend", backend, "error", err)
		} else if b.hooks != nil && len(users) > 0 {
			b.hooks.SendUnassign(ctx, backend, users)
		}
		if err := b.redis.InvalidateBackend(ctx, backend); err != nil {
			slog.Error("failed to invalidate redis mappings", "backend", backend, "error", err)
		}
	} else if b.hooks != nil && b.cache != nil {
		// Hash mode without Redis: get affected users from local cache.
		users := b.cache.UsersForBackend(backend)
		if len(users) > 0 {
			b.hooks.SendUnassign(ctx, backend, users)
		}
	}

	if b.cache != nil {
		b.cache.InvalidateBackend(backend)
	}

	if b.notifier != nil {
		if err := b.notifier.Publish(ctx, backend); err != nil {
			slog.Error("cache notifier: publish failed", "backend", backend, "error", err)
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
