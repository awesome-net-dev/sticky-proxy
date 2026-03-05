package proxy

import (
	"context"
	_ "embed"
	"encoding/json"
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
	client       *redis.Client
	script       *redis.Script
	assignScript *redis.Script

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

//go:embed assign.lua
var assignLua string

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
		client:       rdb,
		script:       redis.NewScript(stickyLua),
		assignScript: redis.NewScript(assignLua),
		cbThreshold:  cbThreshold,
		cbCooldown:   cbCooldown,
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

// GetUsersForBackend scans sticky:* keys and returns all user IDs
// whose current assignment matches the given backend.
func (r *Redis) GetUsersForBackend(ctx context.Context, backend string) ([]string, error) {
	var users []string
	var cursor uint64
	for {
		keys, next, err := r.client.Scan(ctx, cursor, "sticky:*", 100).Result()
		if err != nil {
			return users, err
		}
		for _, key := range keys {
			val, err := r.client.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			if val == backend {
				// Extract userId from "sticky:{userId}"
				users = append(users, key[len("sticky:"):])
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return users, nil
}

// --- Assignment-table mode methods ---

// AssignViaTable runs the assign.lua script to atomically look up or create
// an assignment in the Redis "assignments" hash. Uses least-loaded selection.
func (r *Redis) AssignViaTable(ctx context.Context, routingKey string) (*Assignment, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.assignScript.Run(
		ctx,
		r.client,
		[]string{"assignments", "backends:active", "assignment:counts"},
		routingKey,
		now,
	).Result()

	if err != nil || res == nil {
		return nil, err
	}

	return unmarshalAssignment(res.(string))
}

// GetAssignment retrieves a single assignment from the Redis hash.
func (r *Redis) GetAssignment(ctx context.Context, routingKey string) (*Assignment, error) {
	val, err := r.client.HGet(ctx, "assignments", routingKey).Result()
	if err != nil {
		return nil, err
	}
	return unmarshalAssignment(val)
}

// DeleteAssignment removes an assignment and decrements the backend count.
func (r *Redis) DeleteAssignment(ctx context.Context, routingKey string) error {
	val, err := r.client.HGet(ctx, "assignments", routingKey).Result()
	if err != nil {
		return err
	}
	a, err := unmarshalAssignment(val)
	if err != nil {
		return err
	}
	pipe := r.client.Pipeline()
	pipe.HDel(ctx, "assignments", routingKey)
	pipe.HIncrBy(ctx, "assignment:counts", a.Backend, -1)
	_, err = pipe.Exec(ctx)
	return err
}

// GetAllAssignments returns all assignments from the Redis hash.
func (r *Redis) GetAllAssignments(ctx context.Context) (map[string]*Assignment, error) {
	vals, err := r.client.HGetAll(ctx, "assignments").Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Assignment, len(vals))
	for k, v := range vals {
		a, err := unmarshalAssignment(v)
		if err != nil {
			continue
		}
		result[k] = a
	}
	return result, nil
}

// GetBackendUsersFromTable scans the assignments hash and returns all
// routing keys assigned to the given backend.
func (r *Redis) GetBackendUsersFromTable(ctx context.Context, backend string) ([]string, error) {
	var users []string
	var cursor uint64
	for {
		keys, next, err := r.client.HScan(ctx, "assignments", cursor, "*", 100).Result()
		if err != nil {
			return users, err
		}
		// HScan returns alternating key, value pairs
		for i := 0; i+1 < len(keys); i += 2 {
			var a Assignment
			if err := json.Unmarshal([]byte(keys[i+1]), &a); err != nil {
				continue
			}
			if a.Backend == backend {
				users = append(users, keys[i])
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return users, nil
}
