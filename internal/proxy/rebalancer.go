package proxy

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"
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
	strategy          RebalanceStrategy
	store             Store
	hooks             *HookClient
	cache             *UserCache
	connTracker       *ConnTracker
	notifier          CacheNotifier   // optional, for cross-replica cache invalidation
	holdMgr           *HoldManager    // optional, holds requests during transitions
	tLock             *TransitionLock // prevents concurrent drain/rebalance
	wsSwapOnRebalance bool            // true = transparent swap; false = close + reconnect
	rebalancing       atomic.Bool
}

// NewRebalancer creates a Rebalancer with the given strategy.
func NewRebalancer(strategy RebalanceStrategy, store Store, hooks *HookClient, cache *UserCache, ct *ConnTracker, notifier CacheNotifier, holdMgr *HoldManager, tLock *TransitionLock, wsSwap bool) *Rebalancer {
	return &Rebalancer{
		strategy:          strategy,
		store:             store,
		hooks:             hooks,
		cache:             cache,
		connTracker:       ct,
		notifier:          notifier,
		holdMgr:           holdMgr,
		tLock:             tLock,
		wsSwapOnRebalance: wsSwap,
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
	if !rb.tLock.Lock(ctx) {
		slog.Info("rebalancer: skipped, another replica holds the transition lock")
		return
	}
	defer rb.tLock.Unlock(ctx)

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

	// 3. Compute new assignments with preserved weights: use ToBackend if set
	//    (consistent-hash), otherwise round-robin across active backends (least-loaded).
	newAssignments := make(map[string]BulkAssignEntry, len(moves))
	var unrouted []string
	for _, m := range moves {
		w := 0
		if a, ok := assignments[m.RoutingKey]; ok {
			w = a.Weight
		}
		if m.ToBackend != "" {
			newAssignments[m.RoutingKey] = BulkAssignEntry{Backend: m.ToBackend, Weight: w}
		} else {
			unrouted = append(unrouted, m.RoutingKey)
		}
	}
	if len(unrouted) > 0 && len(activeBackends) > 0 {
		for i, key := range unrouted {
			w := 0
			if a, ok := assignments[key]; ok {
				w = a.Weight
			}
			newAssignments[key] = BulkAssignEntry{Backend: activeBackends[i%len(activeBackends)], Weight: w}
		}
	}

	// 4. Bulk assign new backends. Uses a background-derived context so this
	//    critical step completes even if the parent context was cancelled —
	//    old assignments are already deleted, so users have no routing without new ones.
	assignCtx, assignCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer assignCancel()
	assigned, err := rb.store.BulkAssign(assignCtx, newAssignments)
	if err != nil {
		slog.Error("rebalancer: bulk assign failed", "error", err)
	}

	// 5. Invalidate cache AFTER new assignments are created so users always have
	//    a valid assignment to fall back to when the cache is cleared.
	for _, m := range moves {
		rb.cache.Invalidate(m.RoutingKey)
	}
	if rb.notifier != nil {
		notified := make(map[string]struct{})
		for _, m := range moves {
			if _, ok := notified[m.FromBackend]; !ok {
				notified[m.FromBackend] = struct{}{}
				publishNotification(ctx, rb.notifier, m.FromBackend)
			}
		}
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

	// 7. Handle active WebSocket connections for moved users.
	if rb.connTracker != nil {
		for _, m := range moves {
			if newBackend, ok := assigned[m.RoutingKey]; ok && rb.wsSwapOnRebalance {
				rb.connTracker.SwapConns(m.RoutingKey, newBackend)
			} else {
				rb.connTracker.CloseConns(m.RoutingKey)
			}
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

	// Sum weight per backend.
	weights := make(map[string]int, len(activeBackends))
	for _, b := range activeBackends {
		weights[b] = 0
	}
	backendUsers := make(map[string][]string)
	for user, a := range assignments {
		weights[a.Backend] += a.EffectiveWeight()
		backendUsers[a.Backend] = append(backendUsers[a.Backend], user)
	}

	totalWeight := 0
	for _, w := range weights {
		totalWeight += w
	}
	ideal := totalWeight / len(activeBackends)
	if ideal == 0 {
		ideal = 1
	}

	var moves []Move
	// Move users from overloaded backends until weight is within threshold.
	for backend, users := range backendUsers {
		excess := weights[backend] - ideal - 1 // threshold of 1
		if excess <= 0 {
			continue
		}
		moved := 0
		for _, user := range users {
			if moved >= excess {
				break
			}
			moves = append(moves, Move{
				RoutingKey:  user,
				FromBackend: backend,
			})
			moved += assignments[user].EffectiveWeight()
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
