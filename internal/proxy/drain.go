package proxy

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DrainManager handles graceful draining of backends by unassigning all users
// before removing the backend from the active pool.
type DrainManager struct {
	store       Store
	redis       *Redis // optional, for hash-mode sticky key operations
	hooks       *HookClient
	cache       *UserCache
	connTracker *ConnTracker
	routingMode string
	timeout     time.Duration
	notifier    CacheNotifier   // optional, for cross-replica cache invalidation
	holdMgr     *HoldManager    // optional, holds requests during transitions
	tLock       *TransitionLock // prevents concurrent drain/rebalance

	mu       sync.Mutex
	draining map[string]context.CancelFunc
}

// NewDrainManager creates a DrainManager.
// The redis parameter is optional and only used for hash-mode sticky key operations.
func NewDrainManager(store Store, r *Redis, hooks *HookClient, cache *UserCache, ct *ConnTracker, routingMode string, timeout time.Duration, notifier CacheNotifier, holdMgr *HoldManager, tLock *TransitionLock) *DrainManager {
	return &DrainManager{
		store:       store,
		redis:       r,
		hooks:       hooks,
		cache:       cache,
		connTracker: ct,
		routingMode: routingMode,
		timeout:     timeout,
		notifier:    notifier,
		holdMgr:     holdMgr,
		tLock:       tLock,
		draining:    make(map[string]context.CancelFunc),
	}
}

// IsDraining returns true if the given backend is currently being drained.
func (d *DrainManager) IsDraining(backend string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.draining[backend]
	return ok
}

// DrainingBackends returns the list of backends currently being drained.
func (d *DrainManager) DrainingBackends() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	backends := make([]string, 0, len(d.draining))
	for b := range d.draining {
		backends = append(backends, b)
	}
	return backends
}

// StartDrain initiates an asynchronous drain of the given backend.
// It returns immediately; the drain runs in the background.
func (d *DrainManager) StartDrain(backend string) {
	d.mu.Lock()
	if _, ok := d.draining[backend]; ok {
		d.mu.Unlock()
		return // already draining
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
	d.draining[backend] = cancel
	d.mu.Unlock()

	IncDrains()
	slog.Info("drain started", "backend", backend)

	go func() {
		defer cancel()
		d.drain(ctx, backend)

		d.mu.Lock()
		delete(d.draining, backend)
		d.mu.Unlock()

		DecDrainingBackends()
		slog.Info("drain completed", "backend", backend)
	}()

	IncDrainingBackends()
}

// CancelDrain cancels an in-progress drain.
func (d *DrainManager) CancelDrain(backend string) {
	d.mu.Lock()
	cancel, ok := d.draining[backend]
	d.mu.Unlock()
	if ok {
		cancel()
	}
}

func (d *DrainManager) drain(ctx context.Context, backend string) {
	d.tLock.Lock()
	defer d.tLock.Unlock()

	var users []string
	var err error
	switch {
	case d.routingMode == "assignment":
		users, err = d.store.GetBackendUsers(ctx, backend)
	case d.redis != nil:
		users, err = d.redis.GetUsersForBackend(ctx, backend)
	default:
		// Hash mode without Redis: get affected users from local cache.
		users = d.cache.UsersForBackend(backend)
	}
	if err != nil {
		slog.Error("drain: failed to get users", "backend", backend, "error", err)
		return
	}

	if d.holdMgr != nil && len(users) > 0 {
		d.holdMgr.MarkTransition(users)
		defer d.holdMgr.ClearTransition(users)
	}

	if len(users) > 0 {
		// Batch unassign hook.
		if d.hooks != nil {
			d.hooks.SendUnassign(ctx, backend, users)
		}

		// Bulk delete assignments/sticky keys.
		if d.routingMode == "assignment" {
			if delErr := d.store.BulkDeleteAssignments(ctx, users); delErr != nil {
				slog.Error("drain: bulk delete failed", "backend", backend, "error", delErr)
			}
		} else if d.redis != nil {
			if delErr := d.redis.BulkDeleteSticky(ctx, users); delErr != nil {
				slog.Error("drain: bulk delete failed", "backend", backend, "error", delErr)
			}
		}

		// Invalidate local cache + close WebSocket connections.
		for _, u := range users {
			d.cache.Invalidate(u)
			if d.connTracker != nil {
				d.connTracker.CloseConns(u)
			}
		}
		AddDrainUsers(uint64(len(users)))
	}

	publishNotification(ctx, d.notifier, backend)

	if err := d.store.RemoveBackend(ctx, backend); err != nil {
		slog.Error("drain: failed to remove backend", "backend", backend, "error", err)
	}
}
