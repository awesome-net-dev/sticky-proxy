package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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
	redis            *Redis
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
	closers          []io.Closer
}

func New(cfg *config.Config) (*Proxy, error) {
	r, err := NewRedis(cfg.RedisAddr, cfg.RedisPoolSize, cfg.RedisMinIdleConns, cfg.RedisCBThreshold, cfg.RedisCBCooldown)
	if err != nil {
		return nil, err
	}

	var hooks *HookClient
	if cfg.HooksEnabled {
		hooks = NewHookClient(cfg.HooksTimeout, cfg.HooksRetries)
	}

	cache := NewUserCache(cfg.CacheTTL)
	ct := NewConnTracker()
	b := NewBackendManager(r, cache, cfg.EvictionThreshold, cfg.EvictionCooldown, hooks)
	b.Start()

	drain := NewDrainManager(r, hooks, cache, ct, cfg.RoutingMode, cfg.DrainTimeout, cfg.DrainMaxConcurrent)

	var discovery *AccountDiscovery
	var closers []io.Closer
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
		discovery = NewAccountDiscovery(source, cfg.AccountsRefreshInterval, r, hooks)
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
		rebalancer = NewRebalancer(strategy, cfg.RebalanceMaxConcurrent, r, hooks, cache, ct)
	}

	hc := NewHealthChecker(r)
	hc.drain = drain
	hc.drainOnUnhealthy = cfg.DrainOnUnhealthy
	hc.rebalancer = rebalancer
	hc.rebalanceOnScale = cfg.RebalanceOnScale

	var backendDisc BackendDiscoverer
	switch cfg.BackendDiscovery {
	case "dns":
		backendDisc = NewBackendDiscovery(cfg.BackendDiscoveryHost, cfg.BackendDiscoveryPort, cfg.BackendDiscoveryInterval, r)
	case "kubernetes":
		k8sDisc, k8sErr := NewKubernetesBackendDiscovery(
			cfg.BackendDiscoveryNamespace,
			cfg.BackendDiscoverySelector,
			cfg.BackendDiscoveryPortName,
			r, drain,
		)
		if k8sErr != nil {
			return nil, k8sErr
		}
		backendDisc = k8sDisc
	}

	slog.Info("proxy initialized")

	return &Proxy{
		redis:            r,
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
	backend, err := p.cache.Get(stickyKey)
	if err == nil && (!p.backends.Available(backend) || (p.drain != nil && p.drain.IsDraining(backend))) {
		p.cache.Invalidate(stickyKey)
		backend = ""
		err = ErrCacheMiss
	}

	if err != nil {
		var assignErr error
		if p.routingMode == "assignment" {
			backend, assignErr = p.assignViaTable(ctx, stickyKey)
		} else {
			backend, assignErr = p.redis.AssignBackend(ctx, stickyKey, p.backends.Hash(stickyKey))
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
		slog.Debug("assigned backend via redis", "userId", stickyKey, "backend", backend, "mode", p.routingMode)
		if p.hooks != nil {
			go p.hooks.SendAssign(context.Background(), backend, stickyKey)
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
	p.jwtCache.Stop()
	p.cache.Stop()
	p.rateLimiter.Stop()
}

// assignViaTable uses the assignment-table routing mode (Redis hash).
func (p *Proxy) assignViaTable(ctx context.Context, routingKey string) (string, error) {
	if p.redis.isCBOpen() {
		IncRedisCBFallbacks()
		fb := p.redis.hashFallback(p.backends.Hash(routingKey))
		if fb != "" {
			return fb, nil
		}
	}

	a, err := p.redis.AssignViaTable(ctx, routingKey)
	if err != nil || a == nil {
		p.redis.recordCBFailure()
		IncRedisFailures()
		fb := p.redis.hashFallback(p.backends.Hash(routingKey))
		if fb != "" {
			IncRedisCBFallbacks()
			return fb, nil
		}
		return "", err
	}
	p.redis.recordCBSuccess()
	return a.Backend, nil
}

// isWebSocket returns true if the request is a WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
