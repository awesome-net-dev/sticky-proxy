package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// HealthChecker actively probes backend /healthz endpoints and updates
// the Redis backends:active set when backends become unhealthy or recover.
type HealthChecker struct {
	redis *Redis

	interval           time.Duration
	unhealthyThreshold int
	healthyThreshold   int
	httpTimeout        time.Duration

	cancel context.CancelFunc

	mu     sync.Mutex
	states map[string]*backendHealth
}

type backendHealth struct {
	consecutiveFails     int
	consecutiveSuccesses int
	healthy              bool
}

// NewHealthChecker creates a HealthChecker with sensible defaults.
func NewHealthChecker(r *Redis) *HealthChecker {
	return &HealthChecker{
		redis:              r,
		interval:           10 * time.Second,
		unhealthyThreshold: 3,
		healthyThreshold:   1,
		httpTimeout:        3 * time.Second,
		states:             make(map[string]*backendHealth),
	}
}

// Start launches the background health-check loop. It blocks until ctx is
// cancelled, so callers should invoke it in a goroutine.
func (hc *HealthChecker) Start(ctx context.Context) {
	ctx, hc.cancel = context.WithCancel(ctx)

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	// Run an initial check immediately on startup.
	hc.checkAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.checkAll(ctx)
		}
	}
}

// Stop cancels the background loop.
func (hc *HealthChecker) Stop() {
	if hc.cancel != nil {
		hc.cancel()
	}
}

// HealthyCount returns the number of backends currently considered healthy.
func (hc *HealthChecker) HealthyCount() int {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	count := 0
	for _, s := range hc.states {
		if s.healthy {
			count++
		}
	}
	return count
}

// checkAll refreshes the backend list from Redis, probes each one, and
// reconciles the Redis set based on the results.
func (hc *HealthChecker) checkAll(ctx context.Context) {
	backends, err := hc.redis.ActiveBackends(ctx)
	if err != nil {
		slog.Error("health checker: failed to fetch backends from redis", "error", err)
		return
	}

	// Keep the Redis circuit-breaker fallback list warm.
	hc.redis.RefreshBackendList(ctx)

	// Merge newly discovered backends into the state map.
	hc.mu.Lock()
	knownURLs := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		knownURLs[b] = struct{}{}
		if _, exists := hc.states[b]; !exists {
			hc.states[b] = &backendHealth{healthy: true}
		}
	}
	// Also include previously-known backends that may have been removed
	// (they could recover and should be re-added).
	allBackends := make([]string, 0, len(hc.states))
	for b := range hc.states {
		allBackends = append(allBackends, b)
	}
	hc.mu.Unlock()

	client := &http.Client{Timeout: hc.httpTimeout}

	// Bounded-concurrency fan-out: probe all backends concurrently
	// with a semaphore limiting to 10 concurrent probes.
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for _, backend := range allBackends {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(b string) {
			defer wg.Done()
			defer func() { <-sem }()
			hc.probe(ctx, client, b)
		}(backend)
	}
	wg.Wait()
}

// probe performs a single health check against a backend and updates state.
func (hc *HealthChecker) probe(ctx context.Context, client *http.Client, backend string) {
	url := fmt.Sprintf("%s/healthz", backend)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		hc.recordResult(ctx, backend, false)
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		hc.recordResult(ctx, backend, false)
		return
	}
	_ = resp.Body.Close()

	hc.recordResult(ctx, backend, resp.StatusCode == http.StatusOK)
}

// recordResult updates counters and triggers Redis mutations on state
// transitions.
func (hc *HealthChecker) recordResult(ctx context.Context, backend string, success bool) {
	hc.mu.Lock()
	state, exists := hc.states[backend]
	if !exists {
		state = &backendHealth{healthy: true}
		hc.states[backend] = state
	}

	wasHealthy := state.healthy

	if success {
		state.consecutiveFails = 0
		state.consecutiveSuccesses++

		if !wasHealthy && state.consecutiveSuccesses >= hc.healthyThreshold {
			state.healthy = true
			hc.mu.Unlock()
			slog.Info("backend became healthy, adding to active set", "backend", backend)
			if err := hc.redis.AddBackend(ctx, backend); err != nil {
				slog.Error("health checker: failed to add backend", "backend", backend, "error", err)
			}
			return
		}
	} else {
		state.consecutiveSuccesses = 0
		state.consecutiveFails++

		if wasHealthy && state.consecutiveFails >= hc.unhealthyThreshold {
			state.healthy = false
			hc.mu.Unlock()
			slog.Warn("backend became unhealthy, removing from active set", "backend", backend, "failures", hc.unhealthyThreshold)
			if err := hc.redis.RemoveBackend(ctx, backend); err != nil {
				slog.Error("health checker: failed to remove backend", "backend", backend, "error", err)
			}
			return
		}
	}

	hc.mu.Unlock()
}
