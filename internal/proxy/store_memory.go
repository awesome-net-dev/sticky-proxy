package proxy

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// MemoryStore implements Store using in-memory data structures.
// It is designed for hash routing mode where no external store is needed.
// Assignment-specific methods return errors since they are not used in hash mode.
type MemoryStore struct {
	mu       sync.RWMutex
	backends map[string]struct{}
	sorted   []string // pre-sorted, rebuilt on add/remove
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		backends: make(map[string]struct{}),
	}
}

var errNotSupported = errors.New("operation not supported by memory store")

func (s *MemoryStore) AssignLeastLoaded(_ context.Context, _ string) (*Assignment, error) {
	return nil, errNotSupported
}

func (s *MemoryStore) GetAssignment(_ context.Context, _ string) (*Assignment, error) {
	return nil, errNotSupported
}

func (s *MemoryStore) GetAllAssignments(_ context.Context) (map[string]*Assignment, error) {
	return nil, errNotSupported
}

func (s *MemoryStore) GetBackendUsers(_ context.Context, _ string) ([]string, error) {
	return nil, errNotSupported
}

func (s *MemoryStore) BulkAssign(_ context.Context, _ map[string]string) (map[string]string, error) {
	return nil, errNotSupported
}

func (s *MemoryStore) BulkDeleteAssignments(_ context.Context, _ []string) error {
	return errNotSupported
}

func (s *MemoryStore) ActiveBackends(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.sorted))
	copy(out, s.sorted)
	return out, nil
}

func (s *MemoryStore) AddBackend(_ context.Context, backend string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.backends[backend]; exists {
		return nil
	}
	s.backends[backend] = struct{}{}
	s.rebuildSorted()
	return nil
}

func (s *MemoryStore) RemoveBackend(_ context.Context, backend string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.backends, backend)
	s.rebuildSorted()
	return nil
}

// rebuildSorted must be called with mu held.
func (s *MemoryStore) rebuildSorted() {
	s.sorted = make([]string, 0, len(s.backends))
	for b := range s.backends {
		s.sorted = append(s.sorted, b)
	}
	sort.Strings(s.sorted)
}

func (s *MemoryStore) Ping(_ context.Context) error {
	return nil
}

func (s *MemoryStore) Close() error {
	return nil
}
