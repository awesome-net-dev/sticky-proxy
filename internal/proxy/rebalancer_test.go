package proxy

import (
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
