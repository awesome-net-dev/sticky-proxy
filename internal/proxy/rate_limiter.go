package proxy

import (
	"sync"
	"time"
)

// userLimiter is a per-user token bucket.
type userLimiter struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
}

// RateLimiter enforces per-user rate limits using a token bucket algorithm.
// Each user gets an independent bucket that refills at a constant rate and
// allows short bursts up to the configured maximum.
type RateLimiter struct {
	rate     float64 // tokens added per second
	burst    int     // maximum tokens (bucket capacity)
	limiters sync.Map
	stopCh   chan struct{}
}

// NewRateLimiter creates a RateLimiter and starts a background goroutine
// that periodically evicts stale user buckets to bound memory usage.
//
// rate  – tokens replenished per second per user (e.g. 100).
// burst – maximum tokens a user can accumulate (e.g. 200).
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		rate:   rate,
		burst:  burst,
		stopCh: make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Allow reports whether the user identified by userID may proceed.
// It atomically refills the bucket based on elapsed time, then tries to
// consume one token.  Returns false when the bucket is empty (rate limited).
func (rl *RateLimiter) Allow(userID string) bool {
	now := time.Now()

	val, _ := rl.limiters.LoadOrStore(userID, &userLimiter{
		tokens:     float64(rl.burst),
		lastRefill: now,
	})
	ul := val.(*userLimiter)

	ul.mu.Lock()
	defer ul.mu.Unlock()

	elapsed := now.Sub(ul.lastRefill).Seconds()
	ul.tokens += elapsed * rl.rate
	if ul.tokens > float64(rl.burst) {
		ul.tokens = float64(rl.burst)
	}
	ul.lastRefill = now

	if ul.tokens >= 1 {
		ul.tokens--
		return true
	}
	return false
}

// cleanup runs on a ticker and removes user buckets that have not been
// touched for more than 5 minutes.  This prevents unbounded growth of the
// limiters map when users disconnect.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	const staleThreshold = 5 * time.Minute

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			rl.limiters.Range(func(key, value any) bool {
				ul := value.(*userLimiter)
				ul.mu.Lock()
				idle := now.Sub(ul.lastRefill)
				ul.mu.Unlock()
				if idle > staleThreshold {
					rl.limiters.Delete(key)
				}
				return true
			})
		case <-rl.stopCh:
			return
		}
	}
}

// Stop terminates the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}
