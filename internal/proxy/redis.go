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
