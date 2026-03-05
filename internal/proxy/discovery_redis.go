package proxy

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// RedisAccountSource fetches account IDs from a Redis set.
type RedisAccountSource struct {
	client *redis.Client
	key    string // Redis set key containing account IDs
}

// NewRedisAccountSource creates a source that reads account IDs from a Redis set.
func NewRedisAccountSource(client *redis.Client, key string) *RedisAccountSource {
	return &RedisAccountSource{client: client, key: key}
}

// FetchAccounts returns all members of the configured Redis set.
func (s *RedisAccountSource) FetchAccounts(ctx context.Context) ([]string, error) {
	return s.client.SMembers(ctx, s.key).Result()
}
