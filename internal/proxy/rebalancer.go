package proxy

import (
	"context"
	"log/slog"
	"sort"
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
	strategy    RebalanceStrategy
	store       Store
	hooks       *HookClient
	cache       *UserCache
	connTracker *ConnTracker
	notifier    CacheNotifier // optional, for cross-replica cache invalidation
	holdMgr     *HoldManager  // optional, holds requests during transitions
	rebalancing atomic.Bool
}

// NewRebalancer creates a Rebalancer with the given strategy.
func NewRebalancer(strategy RebalanceStrategy, store Store, hooks *HookClient, cache *UserCache, ct *ConnTracker, notifier CacheNotifier, holdMgr *HoldManager) *Rebalancer {
	return &Rebalancer{
		strategy:    strategy,
		store:       store,
		hooks:       hooks,
		cache:       cache,
		connTracker: ct,
		notifier:    notifier,
		holdMgr:     holdMgr,
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
	sort.Strings(activeBackends)

	assignments, err := rb.store.GetAllAssignments(ctx)
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

	// Hold requests for affected users during the entire reassignment.
	if rb.holdMgr != nil {
		holdKeys := make([]string, len(moves))
		for i, m := range moves {
			holdKeys[i] = m.RoutingKey
		}
		rb.holdMgr.MarkTransition(holdKeys)
		defer rb.holdMgr.ClearTransition(holdKeys)
	}

	// 1. Group unassigns by FromBackend, send batch hooks.
	if rb.hooks != nil {
		byBackend := make(map[string][]string)
		for _, m := range moves {
			byBackend[m.FromBackend] = append(byBackend[m.FromBackend], m.RoutingKey)
		}
		for backend, keys := range byBackend {
			rb.hooks.SendUnassign(ctx, backend, keys)
		}
	}

	// 2. Bulk delete old assignments.
	routingKeys := make([]string, len(moves))
	for i, m := range moves {
		routingKeys[i] = m.RoutingKey
	}
	if err := rb.store.BulkDeleteAssignments(ctx, routingKeys); err != nil {
		slog.Error("rebalancer: bulk delete failed", "error", err)
	}

	// 3. Invalidate cache + close WebSocket connections.
	for _, m := range moves {
		rb.cache.Invalidate(m.RoutingKey)
		if rb.connTracker != nil {
			rb.connTracker.CloseConns(m.RoutingKey)
		}
	}

	// 3b. Notify other replicas to invalidate their caches for affected backends.
	if rb.notifier != nil {
		notified := make(map[string]struct{})
		for _, m := range moves {
			if _, ok := notified[m.FromBackend]; !ok {
				notified[m.FromBackend] = struct{}{}
				publishNotification(ctx, rb.notifier, m.FromBackend)
			}
		}
	}

	// Bail out if context was cancelled during unassign/delete phase.
	if ctx.Err() != nil {
		slog.Warn("rebalancer: context cancelled after delete phase", "error", ctx.Err())
		return
	}

	// 4. Compute new assignments: use ToBackend if set (consistent-hash),
	//    otherwise round-robin across active backends (least-loaded).
	newAssignments := make(map[string]string, len(moves))
	var unrouted []string
	for _, m := range moves {
		if m.ToBackend != "" {
			newAssignments[m.RoutingKey] = m.ToBackend
		} else {
			unrouted = append(unrouted, m.RoutingKey)
		}
	}
	if len(unrouted) > 0 && len(activeBackends) > 0 {
		for i, key := range unrouted {
			newAssignments[key] = activeBackends[i%len(activeBackends)]
		}
	}

	// 5. Bulk assign new backends.
	assigned, err := rb.store.BulkAssign(ctx, newAssignments)
	if err != nil {
		slog.Error("rebalancer: bulk assign failed", "error", err)
	}

	// 6. Group assigns by backend, send batch hooks.
	if rb.hooks != nil && len(assigned) > 0 {
		byBackend := make(map[string][]string)
		for routingKey, backend := range assigned {
			byBackend[backend] = append(byBackend[backend], routingKey)
		}
		for backend, keys := range byBackend {
			rb.hooks.SendAssign(ctx, backend, keys)
		}
	}

	AddRebalanceMoves(uint64(len(assigned)))
	slog.Info("rebalancer: completed", "moves", len(assigned))
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
