package proxy

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStore is a minimal Store for testing discovery reconciliation.
type fakeStore struct {
	mu          sync.Mutex
	assignments map[string]*Assignment
	backends    []string
	bulkCalls   []map[string]BulkAssignEntry // recorded BulkAssign inputs
}

func newFakeStore(backends []string) *fakeStore {
	return &fakeStore{
		assignments: make(map[string]*Assignment),
		backends:    backends,
	}
}

func (s *fakeStore) AssignLeastLoaded(_ context.Context, _ string) (*Assignment, error) {
	return nil, errNotSupported
}

func (s *fakeStore) GetAssignment(_ context.Context, _ string) (*Assignment, error) {
	return nil, errNotSupported
}

func (s *fakeStore) GetAllAssignments(_ context.Context) (map[string]*Assignment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]*Assignment, len(s.assignments))
	for k, v := range s.assignments {
		out[k] = v
	}
	return out, nil
}

func (s *fakeStore) GetBackendUsers(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (s *fakeStore) BulkAssign(_ context.Context, assignments map[string]BulkAssignEntry) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bulkCalls = append(s.bulkCalls, assignments)
	assigned := make(map[string]string, len(assignments))
	for k, v := range assignments {
		s.assignments[k] = &Assignment{
			Backend:    v.Backend,
			AssignedAt: time.Now(),
			Source:     "assignment",
			Weight:     v.Weight,
		}
		assigned[k] = v.Backend
	}
	return assigned, nil
}

func (s *fakeStore) BulkDeleteAssignments(_ context.Context, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.assignments, k)
	}
	return nil
}

func (s *fakeStore) ActiveBackends(_ context.Context) ([]string, error) {
	return s.backends, nil
}

func (s *fakeStore) AddBackend(_ context.Context, _ string) error    { return nil }
func (s *fakeStore) RemoveBackend(_ context.Context, _ string) error { return nil }
func (s *fakeStore) Ping(_ context.Context) error                    { return nil }
func (s *fakeStore) Close() error                                    { return nil }

// fakeAccountSource returns a fixed list of discovered accounts.
type fakeAccountSource struct {
	accounts []DiscoveredAccount
}

func (s *fakeAccountSource) FetchAccounts(_ context.Context) ([]DiscoveredAccount, error) {
	return s.accounts, nil
}

func TestDiscovery_ReconcileAssignsWeights(t *testing.T) {
	t.Parallel()

	store := newFakeStore([]string{"b1", "b2"})
	source := &fakeAccountSource{
		accounts: []DiscoveredAccount{
			{ID: "heavy", Weight: 10},
			{ID: "light", Weight: 1},
			{ID: "default", Weight: 0},
		},
	}

	d := NewAccountDiscovery(source, time.Hour, store, nil)
	d.reconcile(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.bulkCalls) != 1 {
		t.Fatalf("expected 1 BulkAssign call, got %d", len(store.bulkCalls))
	}

	call := store.bulkCalls[0]
	if len(call) != 3 {
		t.Fatalf("expected 3 assignments, got %d", len(call))
	}

	if call["heavy"].Weight != 10 {
		t.Errorf("heavy weight = %d, want 10", call["heavy"].Weight)
	}
	if call["light"].Weight != 1 {
		t.Errorf("light weight = %d, want 1", call["light"].Weight)
	}
	if call["default"].Weight != 0 {
		t.Errorf("default weight = %d, want 0 (store normalizes)", call["default"].Weight)
	}
}

func TestDiscovery_ReconcileSkipsExistingAssignments(t *testing.T) {
	t.Parallel()

	store := newFakeStore([]string{"b1"})
	// Pre-assign "existing" account.
	store.assignments["existing"] = &Assignment{Backend: "b1", Weight: 5}

	source := &fakeAccountSource{
		accounts: []DiscoveredAccount{
			{ID: "existing", Weight: 5},
			{ID: "new", Weight: 3},
		},
	}

	d := NewAccountDiscovery(source, time.Hour, store, nil)
	d.reconcile(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.bulkCalls) != 1 {
		t.Fatalf("expected 1 BulkAssign call, got %d", len(store.bulkCalls))
	}

	call := store.bulkCalls[0]
	if len(call) != 1 {
		t.Fatalf("expected 1 new assignment, got %d", len(call))
	}
	if _, ok := call["new"]; !ok {
		t.Error("expected 'new' to be assigned")
	}
	if call["new"].Weight != 3 {
		t.Errorf("new weight = %d, want 3", call["new"].Weight)
	}
}

func TestDiscovery_ReconcileNoBackends(t *testing.T) {
	t.Parallel()

	store := newFakeStore(nil) // no backends
	source := &fakeAccountSource{
		accounts: []DiscoveredAccount{{ID: "acct-1", Weight: 1}},
	}

	d := NewAccountDiscovery(source, time.Hour, store, nil)
	d.reconcile(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.bulkCalls) != 0 {
		t.Fatalf("expected 0 BulkAssign calls with no backends, got %d", len(store.bulkCalls))
	}
}

func TestDiscovery_ReconcileAllAlreadyAssigned(t *testing.T) {
	t.Parallel()

	store := newFakeStore([]string{"b1"})
	store.assignments["acct-1"] = &Assignment{Backend: "b1", Weight: 1}

	source := &fakeAccountSource{
		accounts: []DiscoveredAccount{{ID: "acct-1", Weight: 1}},
	}

	d := NewAccountDiscovery(source, time.Hour, store, nil)
	d.reconcile(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.bulkCalls) != 0 {
		t.Fatalf("expected 0 BulkAssign calls when all assigned, got %d", len(store.bulkCalls))
	}
}
