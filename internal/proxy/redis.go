package proxy

import (
	"context"
	_ "embed"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client *redis.Client
	script *redis.Script
}

//go:embed sticky.lua
var stickyLua string

func NewRedis() (*Redis, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "redis:6379",
	})

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
		atomic.AddUint64(&redisFailures, 1)
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
