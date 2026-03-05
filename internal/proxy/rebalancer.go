package proxy

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
)

// Move represents a user reassignment from one backend to another.
type Move struct {
	RoutingKey  string
	FromBackend string
	ToBackend   string
}

// RebalanceStrategy computes the set of moves needed to rebalance assignments.
type RebalanceStrategy interface {
	ComputeMoves(assignments map[string]*Assignment, activeBackends []string) []Move
}

// Rebalancer redistributes users across backends when the backend set changes.
type Rebalancer struct {
	strategy      RebalanceStrategy
	maxConcurrent int
	redis         *Redis
	hooks         *HookClient
	cache         *UserCache
	rebalancing   atomic.Bool
}

// NewRebalancer creates a Rebalancer with the given strategy.
func NewRebalancer(strategy RebalanceStrategy, maxConcurrent int, r *Redis, hooks *HookClient, cache *UserCache) *Rebalancer {
	return &Rebalancer{
		strategy:      strategy,
		maxConcurrent: maxConcurrent,
		redis:         r,
		hooks:         hooks,
		cache:         cache,
	}
}

// Trigger starts a rebalance if one is not already in progress.
func (rb *Rebalancer) Trigger(ctx context.Context, activeBackends []string) {
	if !rb.rebalancing.CompareAndSwap(false, true) {
		return // already rebalancing
	}

	go func() {
		defer rb.rebalancing.Store(false)
		rb.rebalance(ctx, activeBackends)
	}()
}

func (rb *Rebalancer) rebalance(ctx context.Context, activeBackends []string) {
	assignments, err := rb.redis.GetAllAssignments(ctx)
	if err != nil {
		slog.Error("rebalancer: failed to get assignments", "error", err)
		return
	}

	moves := rb.strategy.ComputeMoves(assignments, activeBackends)
	if len(moves) == 0 {
		return
	}

	IncRebalances()
	slog.Info("rebalancer: starting", "moves", len(moves))

	sem := make(chan struct{}, rb.maxConcurrent)
	var wg sync.WaitGroup

	for _, m := range moves {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(move Move) {
			defer wg.Done()
			defer func() { <-sem }()
			rb.executeMove(ctx, move)
		}(m)
	}
	wg.Wait()

	slog.Info("rebalancer: completed", "moves", len(moves))
}

func (rb *Rebalancer) executeMove(ctx context.Context, m Move) {
	// Unassign from old backend.
	if rb.hooks != nil {
		rb.hooks.SendUnassign(ctx, m.FromBackend, m.RoutingKey)
	}

	// Delete old assignment.
	_ = rb.redis.DeleteAssignment(ctx, m.RoutingKey)
	rb.cache.Invalidate(m.RoutingKey)

	// Assign to new backend via the Lua script (will pick least-loaded).
	a, err := rb.redis.AssignViaTable(ctx, m.RoutingKey)
	if err != nil || a == nil {
		slog.Warn("rebalancer: failed to reassign", "user", m.RoutingKey, "error", err)
		return
	}

	if rb.hooks != nil {
		rb.hooks.SendAssign(ctx, a.Backend, m.RoutingKey)
	}
	IncRebalanceMoves()
}

// --- Strategies ---

// LeastLoadedStrategy moves users from overloaded backends to restore balance.
type LeastLoadedStrategy struct{}

func (s *LeastLoadedStrategy) ComputeMoves(assignments map[string]*Assignment, activeBackends []string) []Move {
	if len(activeBackends) == 0 {
		return nil
	}

	// Count assignments per backend.
	counts := make(map[string]int, len(activeBackends))
	for _, b := range activeBackends {
		counts[b] = 0
	}
	backendUsers := make(map[string][]string)
	for user, a := range assignments {
		counts[a.Backend]++
		backendUsers[a.Backend] = append(backendUsers[a.Backend], user)
	}

	ideal := len(assignments) / len(activeBackends)
	if ideal == 0 {
		ideal = 1
	}

	var moves []Move
	// Move users from overloaded backends.
	for backend, users := range backendUsers {
		excess := counts[backend] - ideal - 1 // threshold of 1
		if excess <= 0 {
			continue
		}
		for i := 0; i < excess && i < len(users); i++ {
			moves = append(moves, Move{
				RoutingKey:  users[i],
				FromBackend: backend,
			})
		}
	}
	return moves
}

// ConsistentHashStrategy rehashes users and moves those whose target changed.
type ConsistentHashStrategy struct{}

func (s *ConsistentHashStrategy) ComputeMoves(assignments map[string]*Assignment, activeBackends []string) []Move {
	if len(activeBackends) == 0 {
		return nil
	}

	// Sort backends for deterministic selection.
	sorted := make([]string, len(activeBackends))
	copy(sorted, activeBackends)
	sort.Strings(sorted)

	var moves []Move
	for user, a := range assignments {
		hash := HashUser(user)
		target := sorted[hash%uint32(len(sorted))]
		if target != a.Backend {
			moves = append(moves, Move{
				RoutingKey:  user,
				FromBackend: a.Backend,
				ToBackend:   target,
			})
		}
	}
	return moves
}
