package proxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"sticky-proxy/internal/config"
)

// BackendDiscoverer is implemented by backend discovery mechanisms (DNS, Kubernetes).
type BackendDiscoverer interface {
	Start(ctx context.Context)
	Stop()
}

type Proxy struct {
	redis            *Redis // nil when ASSIGNMENT_STORE is memory or postgres
	store            Store
	cache            *UserCache
	backends         *BackendManager
	jwtCache         *JWTCache
	jwtSecret        []byte
	routingClaim     string
	routingMode      string
	hooks            *HookClient
	drain            *DrainManager
	connTracker      *ConnTracker
	discovery        *AccountDiscovery
	backendDiscovery BackendDiscoverer
	HealthChecker    *HealthChecker
	rateLimiter      *RateLimiter
	notifier         CacheNotifier       // optional, for cross-replica cache invalidation
	holdMgr          *HoldManager        // optional, holds requests during assignment transitions
	poisonPill       *PoisonPillDetector // optional, quarantines accounts that crash backends
	closers          []io.Closer
}

func New(cfg *config.Config) (*Proxy, error) {
	var hooks *HookClient
	if cfg.HooksEnabled {
		hooks = NewHookClient(cfg.HooksTimeout, cfg.HooksRetries)
	}

	var r *Redis
	var store Store
	var notifier CacheNotifier
	var closers []io.Closer

	switch cfg.AssignmentStore {
	case "memory":
		store = NewMemoryStore()
	case "postgres":
		pgStore, pgErr := NewPostgresStore(cfg.PostgresDSN)
		if pgErr != nil {
			return nil, pgErr
		}
		store = pgStore
		notifier = NewPostgresCacheNotifier(cfg.PostgresDSN, pgStore.DB())
		closers = append(closers, pgStore)
	default: // "redis"
		var err error
		r, err = NewRedis(cfg.RedisAddr, cfg.RedisPoolSize, cfg.RedisMinIdleConns, cfg.RedisCBThreshold, cfg.RedisCBCooldown)
		if err != nil {
			return nil, err
		}
		store = NewRedisStore(r)
		notifier = NewRedisCacheNotifier(r.client)
	}

	cache := NewUserCache(cfg.CacheTTL)
	ct := NewConnTracker()
	b := NewBackendManager(store, r, cache, cfg.RoutingMode, cfg.EvictionThreshold, cfg.EvictionCooldown, hooks, notifier)
	b.Start()

	var holdMgr *HoldManager
	if cfg.HoldDuringTransition {
		holdMgr = NewHoldManager(cfg.HoldTimeout)
	}

	tLock := &TransitionLock{}
	drain := NewDrainManager(store, r, hooks, cache, ct, cfg.RoutingMode, cfg.DrainTimeout, notifier, holdMgr, tLock)

	var discovery *AccountDiscovery
	if cfg.AccountsDiscovery != "" {
		var source AccountSource
		switch cfg.AccountsDiscovery {
		case "redis":
			source = NewRedisAccountSource(r.client, cfg.AccountsQuery)
		case "http":
			source = NewHTTPAccountSource(cfg.AccountsQuery)
		case "postgres":
			pgSource, pgErr := NewPostgresAccountSource(cfg.PostgresDSN, cfg.AccountsQuery)
			if pgErr != nil {
				return nil, pgErr
			}
			source = pgSource
			closers = append(closers, pgSource)
		}
		discovery = NewAccountDiscovery(source, cfg.AccountsRefreshInterval, store, hooks)
	}

	var rebalancer *Rebalancer
	if cfg.RebalanceStrategy != "none" {
		var strategy RebalanceStrategy
		switch cfg.RebalanceStrategy {
		case "least-loaded":
			strategy = &LeastLoadedStrategy{}
		case "consistent-hash":
			strategy = &ConsistentHashStrategy{}
		}
		rebalancer = NewRebalancer(strategy, store, hooks, cache, ct, notifier, holdMgr, tLock, cfg.WSSwapOnRebalance)
	}

	hc := NewHealthChecker(store, r)
	hc.drain = drain
	hc.drainOnUnhealthy = cfg.DrainOnUnhealthy
	hc.rebalancer = rebalancer
	hc.rebalanceOnScale = cfg.RebalanceOnScale

	var backendDisc BackendDiscoverer
	switch cfg.BackendDiscovery {
	case "dns":
		backendDisc = NewBackendDiscovery(cfg.BackendDiscoveryHost, cfg.BackendDiscoveryPort, cfg.BackendDiscoveryInterval, store)
	case "kubernetes":
		k8sDisc, k8sErr := NewKubernetesBackendDiscovery(
			cfg.BackendDiscoveryNamespace,
			cfg.BackendDiscoverySelector,
			cfg.BackendDiscoveryPortName,
			store, drain,
		)
		if k8sErr != nil {
			return nil, k8sErr
		}
		backendDisc = k8sDisc
	}

	var poisonPill *PoisonPillDetector
	if cfg.PoisonPillAction == "quarantine" {
		poisonPill = NewPoisonPillDetector(cfg.PoisonPillThreshold, cfg.PoisonPillWindow)
	}

	slog.Info("proxy initialized", "assignment_store", cfg.AssignmentStore)

	return &Proxy{
		redis:            r,
		store:            store,
		cache:            cache,
		backends:         b,
		jwtCache:         NewJWTCache(cfg.JWTCacheMaxSize),
		jwtSecret:        []byte(cfg.JWTSecret),
		routingClaim:     cfg.RoutingClaim,
		routingMode:      cfg.RoutingMode,
		hooks:            hooks,
		drain:            drain,
		connTracker:      ct,
		discovery:        discovery,
		backendDiscovery: backendDisc,
		HealthChecker:    hc,
		rateLimiter:      NewRateLimiter(100, 200),
		notifier:         notifier,
		holdMgr:          holdMgr,
		poisonPill:       poisonPill,
		closers:          closers,
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
	jwtData, err := extractUserIDFromJWT(authHeader, p.jwtCache, p.jwtSecret, p.routingClaim)
	if err != nil {
		IncAuthFailures()
		slog.Warn("unauthorized request", "error", err, "path", r.URL.Path)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if !p.rateLimiter.Allow(jwtData.RoutingKey) {
		IncRateLimited()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	stickyKey := jwtData.RoutingKey

	// Block quarantined accounts (poison pill detection).
	if p.poisonPill != nil && p.poisonPill.IsQuarantined(stickyKey) {
		IncPoisonPillBlocked()
		http.Error(w, "account quarantined", http.StatusServiceUnavailable)
		return
	}

	// Hold request if user is mid-transition (drain/rebalance in progress).
	if p.holdMgr != nil {
		p.holdMgr.Wait(ctx, stickyKey)
	}

	backend, err := p.cache.Get(stickyKey)
	if err == nil && (!p.backends.Available(backend) || (p.drain != nil && p.drain.IsDraining(backend))) {
		// Track forced reassignment for poison pill detection.
		if p.poisonPill != nil && p.poisonPill.RecordReassignment(stickyKey) {
			IncPoisonPillBlocked()
			http.Error(w, "account quarantined", http.StatusServiceUnavailable)
			return
		}
		p.cache.Invalidate(stickyKey)
		backend = ""
		err = ErrCacheMiss
	}

	if err != nil {
		var assignErr error
		if p.routingMode == "assignment" {
			backend, assignErr = p.assignViaTable(ctx, stickyKey)
		} else {
			backend, assignErr = p.assignViaHash(ctx, stickyKey)
		}
		if assignErr != nil {
			IncCacheMisses()
			IncBackendErrors()
			slog.Error("failed to assign backend", "userId", stickyKey, "error", assignErr)
			http.Error(w, "no backend", http.StatusServiceUnavailable)
			return
		}
		IncCacheHitsRedis()
		p.cache.Set(stickyKey, backend)
		slog.Debug("assigned backend", "userId", stickyKey, "backend", backend, "mode", p.routingMode)
		if p.hooks != nil {
			go p.hooks.SendAssign(context.Background(), backend, []string{stickyKey})
		}
	} else {
		IncCacheHitsLocal()
		slog.Debug("cache hit", "userId", stickyKey, "backend", backend)
	}

	r.Header.Set("X-User-ID", stickyKey)

	if isWS {
		proxyWebSocket(w, r, backend, stickyKey, p.connTracker)
		return
	}

	IncBackendRequests(backend)
	p.backends.ProxyRequest(w, r, backend)
}

// Stop gracefully shuts down background goroutines owned by the proxy.
// StartDiscovery launches the account discovery loop if configured.
func (p *Proxy) StartDiscovery(ctx context.Context) {
	if p.discovery != nil {
		go p.discovery.Start(ctx)
	}
	if p.backendDiscovery != nil {
		go p.backendDiscovery.Start(ctx)
	}
	if p.notifier != nil {
		go subscribeDebouncedNotifier(ctx, p.notifier, p.cache, 100*time.Millisecond)
	}
}

func (p *Proxy) Stop() {
	if p.discovery != nil {
		p.discovery.Stop()
	}
	if p.backendDiscovery != nil {
		p.backendDiscovery.Stop()
	}
	for _, c := range p.closers {
		_ = c.Close()
	}
	if p.poisonPill != nil {
		p.poisonPill.Stop()
	}
	p.jwtCache.Stop()
	p.cache.Stop()
	p.rateLimiter.Stop()
}

// assignViaTable uses the assignment-table routing mode.
func (p *Proxy) assignViaTable(ctx context.Context, routingKey string) (string, error) {
	// Redis circuit breaker: try hash fallback if CB is open.
	if p.redis != nil && p.redis.isCBOpen() {
		IncRedisCBFallbacks()
		fb := p.redis.hashFallback(p.backends.Hash(routingKey))
		if fb != "" {
			return fb, nil
		}
	}

	a, err := p.store.AssignLeastLoaded(ctx, routingKey)
	if err != nil || a == nil {
		if p.redis != nil {
			p.redis.recordCBFailure()
			IncRedisFailures()
			fb := p.redis.hashFallback(p.backends.Hash(routingKey))
			if fb != "" {
				IncRedisCBFallbacks()
				return fb, nil
			}
		}
		return "", err
	}
	if p.redis != nil {
		p.redis.recordCBSuccess()
	}
	return a.Backend, nil
}

// assignViaHash picks a backend using CRC32 hash over the active backend list.
func (p *Proxy) assignViaHash(ctx context.Context, routingKey string) (string, error) {
	backends, err := p.store.ActiveBackends(ctx)
	if err != nil {
		return "", err
	}
	if len(backends) == 0 {
		return "", errors.New("no active backends")
	}
	sort.Strings(backends)
	hash := p.backends.Hash(routingKey)
	return backends[hash%uint32(len(backends))], nil
}

// isWebSocket returns true if the request is a WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
