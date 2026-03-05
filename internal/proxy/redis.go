package proxy

import (
	"context"
	_ "embed"
	"hash/crc32"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// cbState represents circuit breaker states.
type cbState int

const (
	cbClosed   cbState = iota // normal operation
	cbOpen                    // failing, use fallback
	cbHalfOpen                // cooldown elapsed, allow one probe
)

type Redis struct {
	client *redis.Client
	script *redis.Script

	// Circuit breaker fields.
	cbMu        sync.Mutex
	cbFailures  int
	cbState     cbState
	cbOpenedAt  time.Time
	cbThreshold int
	cbCooldown  time.Duration

	// Cached backend list for hash fallback.
	cachedBackends atomic.Value // []string
}

//go:embed sticky.lua
var stickyLua string

func NewRedis(addr string, poolSize, minIdleConns, cbThreshold int, cbCooldown time.Duration) (*Redis, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		PoolSize:     poolSize,
		MinIdleConns: minIdleConns,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolTimeout:  4 * time.Second,
	})

	slog.Info("redis client initialized", "addr", addr)

	r := &Redis{
		client:      rdb,
		script:      redis.NewScript(stickyLua),
		cbThreshold: cbThreshold,
		cbCooldown:  cbCooldown,
	}
	r.cachedBackends.Store([]string(nil))
	return r, nil
}

func (r *Redis) AssignBackend(
	ctx context.Context,
	userID string,
	hash uint32,
) (string, error) {
	// Check circuit breaker state.
	if r.isCBOpen() {
		IncRedisCBFallbacks()
		fb := r.hashFallback(hash)
		if fb != "" {
			return fb, nil
		}
		// No cached backends — fall through to try Redis anyway.
	}

	res, err := r.script.Run(
		ctx,
		r.client,
		[]string{
			"sticky:" + userID,
			"backends:active",
		},
		86400,
		hash,
	).Result()

	if err != nil || res == nil {
		r.recordCBFailure()
		IncRedisFailures()
		slog.Error("redis assign backend failed", "userId", userID, "error", err)
		// On failure, try hash fallback.
		if fb := r.hashFallback(hash); fb != "" {
			IncRedisCBFallbacks()
			return fb, nil
		}
		return "", err
	}

	r.recordCBSuccess()
	return res.(string), nil
}

// RefreshBackendList updates the cached backend list used for hash fallback.
// Called by the health checker after each successful ActiveBackends fetch.
func (r *Redis) RefreshBackendList(ctx context.Context) {
	backends, err := r.ActiveBackends(ctx)
	if err != nil {
		slog.Error("failed to refresh cached backend list", "error", err)
		return
	}
	r.cachedBackends.Store(backends)
}

// hashFallback performs CRC32 modulo routing over cached backends.
func (r *Redis) hashFallback(hash uint32) string {
	backends := r.cachedBackends.Load().([]string)
	if len(backends) == 0 {
		return ""
	}
	// Use a secondary CRC32 to avoid correlation with the input hash.
	idx := crc32.ChecksumIEEE([]byte{
		byte(hash), byte(hash >> 8), byte(hash >> 16), byte(hash >> 24),
	}) % uint32(len(backends))
	return backends[idx]
}

func (r *Redis) isCBOpen() bool {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()

	switch r.cbState {
	case cbOpen:
		if time.Since(r.cbOpenedAt) >= r.cbCooldown {
			r.cbState = cbHalfOpen
			return false // allow one probe
		}
		return true
	case cbHalfOpen:
		return false // allow the probe
	default:
		return false
	}
}

func (r *Redis) recordCBFailure() {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()

	r.cbFailures++
	if r.cbFailures >= r.cbThreshold {
		r.cbState = cbOpen
		r.cbOpenedAt = time.Now()
		slog.Warn("redis circuit breaker opened", "failures", r.cbFailures)
	}
}

func (r *Redis) recordCBSuccess() {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()

	r.cbFailures = 0
	if r.cbState != cbClosed {
		slog.Info("redis circuit breaker closed")
		r.cbState = cbClosed
	}
}

// InvalidateBackend scans all sticky:* keys using SCAN (safe at scale)
// and deletes every key whose value matches the given backend address.
func (r *Redis) InvalidateBackend(ctx context.Context, backend string) error {
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, "sticky:*", 100).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			val, err := r.client.Get(ctx, key).Result()
			if err != nil {
				continue // key may have expired between SCAN and GET
			}
			if val == backend {
				r.client.Del(ctx, key)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

// Ping checks if Redis is alive
func (r *Redis) Ping() error {
	return r.client.Ping(context.Background()).Err()
}

// ActiveBackends returns all members of the backends:active set.
func (r *Redis) ActiveBackends(ctx context.Context) ([]string, error) {
	return r.client.SMembers(ctx, "backends:active").Result()
}

// AddBackend adds a backend URL to the active set.
func (r *Redis) AddBackend(ctx context.Context, backend string) error {
	return r.client.SAdd(ctx, "backends:active", backend).Err()
}

// RemoveBackend removes a backend URL from the active set.
func (r *Redis) RemoveBackend(ctx context.Context, backend string) error {
	return r.client.SRem(ctx, "backends:active", backend).Err()
}
