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
func (s *RedisAccountSource) FetchAccounts(ctx context.Context) ([]DiscoveredAccount, error) {
	ids, err := s.client.SMembers(ctx, s.key).Result()
	if err != nil {
		return nil, err
	}
	accounts := make([]DiscoveredAccount, len(ids))
	for i, id := range ids {
		accounts[i] = DiscoveredAccount{ID: id}
	}
	return accounts, nil
}
