package proxy

import "context"

// RedisStore adapts *Redis to the Store interface.
type RedisStore struct {
	r *Redis
}

// NewRedisStore creates a Store backed by Redis.
func NewRedisStore(r *Redis) *RedisStore {
	return &RedisStore{r: r}
}

func (s *RedisStore) AssignLeastLoaded(ctx context.Context, routingKey string) (*Assignment, error) {
	return s.r.AssignViaTable(ctx, routingKey)
}

func (s *RedisStore) GetAssignment(ctx context.Context, routingKey string) (*Assignment, error) {
	return s.r.GetAssignment(ctx, routingKey)
}

func (s *RedisStore) GetAllAssignments(ctx context.Context) (map[string]*Assignment, error) {
	return s.r.GetAllAssignments(ctx)
}

func (s *RedisStore) GetBackendUsers(ctx context.Context, backend string) ([]string, error) {
	return s.r.GetBackendUsersFromTable(ctx, backend)
}

func (s *RedisStore) BulkAssign(ctx context.Context, assignments map[string]BulkAssignEntry) (map[string]string, error) {
	return s.r.BulkAssign(ctx, assignments)
}

func (s *RedisStore) BulkDeleteAssignments(ctx context.Context, routingKeys []string) error {
	return s.r.BulkDeleteAssignments(ctx, routingKeys)
}

func (s *RedisStore) ActiveBackends(ctx context.Context) ([]string, error) {
	return s.r.ActiveBackends(ctx)
}

func (s *RedisStore) AddBackend(ctx context.Context, backend string) error {
	return s.r.AddBackend(ctx, backend)
}

func (s *RedisStore) RemoveBackend(ctx context.Context, backend string) error {
	return s.r.RemoveBackend(ctx, backend)
}

func (s *RedisStore) Ping(_ context.Context) error {
	return s.r.Ping()
}

func (s *RedisStore) Close() error {
	return s.r.client.Close()
}
