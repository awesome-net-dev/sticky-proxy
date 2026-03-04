package proxy

import (
	"context"
	_ "embed"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client *redis.Client
	script *redis.Script
}

//go:embed sticky.lua
var stickyLua string

func NewRedis(addr string, poolSize int) (*Redis, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		PoolSize:     poolSize,
		MinIdleConns: 20,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolTimeout:  4 * time.Second,
	})

	slog.Info("redis client initialized", "addr", addr)

	return &Redis{
		client: rdb,
		script: redis.NewScript(stickyLua),
	}, nil
}

func (r *Redis) AssignBackend(
	ctx context.Context,
	userID string,
	hash uint32,
) (string, error) {

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
		IncRedisFailures()
		slog.Error("redis assign backend failed", "userId", userID, "error", err)
		return "", err
	}

	return res.(string), nil
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
