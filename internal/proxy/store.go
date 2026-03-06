package proxy

import "context"

// Store abstracts assignment and backend management operations.
// Implemented by RedisStore and PostgresStore.
type Store interface {
	// AssignLeastLoaded atomically assigns a routing key to the least-loaded
	// backend, or returns the existing assignment if one exists.
	AssignLeastLoaded(ctx context.Context, routingKey string) (*Assignment, error)

	// GetAssignment returns the current assignment for a routing key.
	GetAssignment(ctx context.Context, routingKey string) (*Assignment, error)

	// GetAllAssignments returns all current assignments.
	GetAllAssignments(ctx context.Context) (map[string]*Assignment, error)

	// GetBackendUsers returns all routing keys assigned to a backend.
	GetBackendUsers(ctx context.Context, backend string) ([]string, error)

	// BulkAssign assigns multiple routing keys to backends.
	// Uses set-if-not-exists semantics to avoid overwriting live assignments.
	// Returns the map of actually-assigned routingKey -> backend.
	BulkAssign(ctx context.Context, assignments map[string]string) (map[string]string, error)

	// BulkDeleteAssignments removes assignments for the given routing keys.
	BulkDeleteAssignments(ctx context.Context, routingKeys []string) error

	// ActiveBackends returns URLs of all healthy backends.
	ActiveBackends(ctx context.Context) ([]string, error)

	// AddBackend registers a backend as healthy/active.
	AddBackend(ctx context.Context, backend string) error

	// RemoveBackend marks a backend as unhealthy/inactive.
	RemoveBackend(ctx context.Context, backend string) error

	// Ping checks store connectivity.
	Ping(ctx context.Context) error

	// Close releases resources.
	Close() error
}
