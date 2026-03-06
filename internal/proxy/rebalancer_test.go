package proxy

import (
	"context"
	"testing"
	"time"
)

func TestLeastLoadedStrategy_ComputeMoves(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2", "b3"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u2": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u3": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u4": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u5": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u6": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment"},
	}

	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	// b1 has 5, ideal is 2 (6/3), excess = 5 - 2 - 1 = 2 moves
	if len(moves) != 2 {
		t.Errorf("expected 2 moves, got %d", len(moves))
	}
	for _, m := range moves {
		if m.FromBackend != "b1" {
			t.Errorf("expected moves from b1, got %s", m.FromBackend)
		}
	}
}

func TestLeastLoadedStrategy_AlreadyBalanced(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u2": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment"},
	}

	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) != 0 {
		t.Errorf("expected 0 moves for balanced state, got %d", len(moves))
	}
}

func TestConsistentHashStrategy_ComputeMoves(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2", "b3"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u2": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment"},
	}

	s := &ConsistentHashStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	// Moves depend on hash values; just verify structure
	for _, m := range moves {
		if m.FromBackend == "" || m.ToBackend == "" {
			t.Errorf("move should have both from and to backends")
		}
		if m.RoutingKey == "" {
			t.Errorf("move should have a routing key")
		}
	}
}

func TestLeastLoadedStrategy_EmptyBackends(t *testing.T) {
	t.Parallel()

	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(map[string]*Assignment{}, nil)

	if moves != nil {
		t.Errorf("expected nil moves for empty backends, got %v", moves)
	}
}

func TestLeastLoadedStrategy_WeightedMoves(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	assignments := map[string]*Assignment{
		"heavy":  {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 10},
		"light1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
		"light2": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
	}

	// Total weight = 12, ideal per backend = 6.
	// b1 has weight 11, b2 has weight 1.
	// Excess for b1 = 11 - 6 - 1 = 4 -> should move some users.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) == 0 {
		t.Fatal("expected at least 1 move from overloaded b1")
	}
	for _, m := range moves {
		if m.FromBackend != "b1" {
			t.Errorf("expected moves from b1, got %s", m.FromBackend)
		}
	}
}

func TestLeastLoadedStrategy_WeightedBalanced(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	assignments := map[string]*Assignment{
		"a": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"b": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
	}

	// Total weight = 10, ideal = 5. Each backend has 5 → balanced.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) != 0 {
		t.Errorf("expected 0 moves for balanced weighted state, got %d", len(moves))
	}
}

func TestLeastLoadedStrategy_MixedWeightsStopsAtExcess(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2", "b3"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"u2": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"u3": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"u4": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
		"u5": {Backend: "b3", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
	}

	// Total weight = 17, ideal = 5 (17/3).
	// b1 weight = 15, excess = 15 - 5 - 1 = 9.
	// Moving users from b1 should stop once moved weight >= excess.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	totalMoved := 0
	for _, m := range moves {
		if m.FromBackend != "b1" {
			t.Errorf("expected moves from b1, got %s", m.FromBackend)
		}
		totalMoved += assignments[m.RoutingKey].EffectiveWeight()
	}

	if totalMoved < 9 {
		t.Errorf("expected to move at least weight 9, moved %d", totalMoved)
	}
}

func TestLeastLoadedStrategy_DefaultWeightOne(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	// Weight=0 should be treated as 1 by EffectiveWeight.
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u2": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u3": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u4": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u5": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
	}

	// Total effective weight = 5, ideal = 2.
	// b1 effective weight = 4, excess = 4 - 2 - 1 = 1.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) != 1 {
		t.Errorf("expected 1 move, got %d", len(moves))
	}
}

func TestRebalancer_PreservesWeightsInReassignment(t *testing.T) {
	t.Parallel()

	store := newFakeStore([]string{"b1", "b2"})
	store.assignments["heavy"] = &Assignment{Backend: "b1", Weight: 10, Source: "assignment", AssignedAt: time.Now()}
	store.assignments["light"] = &Assignment{Backend: "b1", Weight: 1, Source: "assignment", AssignedAt: time.Now()}
	store.assignments["other"] = &Assignment{Backend: "b2", Weight: 1, Source: "assignment", AssignedAt: time.Now()}

	cache := NewUserCache(time.Minute)
	t.Cleanup(cache.Stop)

	rb := NewRebalancer(&LeastLoadedStrategy{}, store, nil, cache, nil, nil, nil, true)
	rb.rebalance(context.Background(), []string{"b1", "b2"})

	store.mu.Lock()
	defer store.mu.Unlock()

	// Verify that BulkAssign was called with correct weights.
	if len(store.bulkCalls) == 0 {
		t.Fatal("expected at least 1 BulkAssign call")
	}

	lastCall := store.bulkCalls[len(store.bulkCalls)-1]
	for key, entry := range lastCall {
		switch key {
		case "heavy":
			if entry.Weight != 10 {
				t.Errorf("heavy reassigned with weight %d, want 10", entry.Weight)
			}
		case "light":
			if entry.Weight != 1 {
				t.Errorf("light reassigned with weight %d, want 1", entry.Weight)
			}
		}
	}
}
