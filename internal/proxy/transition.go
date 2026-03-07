package proxy

import (
	"context"
	"sync"
)

// DistributedLocker provides a cross-replica advisory lock so that only one
// proxy instance executes a drain or rebalance at a time. Implementations
// must be safe for concurrent use.
type DistributedLocker interface {
	// TryLock attempts to acquire the distributed lock. Returns true if
	// the lock was acquired, false if another replica holds it.
	TryLock(ctx context.Context) (bool, error)

	// Unlock releases the distributed lock. Must only be called after a
	// successful TryLock.
	Unlock(ctx context.Context) error
}

// TransitionLock prevents concurrent drain and rebalance operations both
// within a single replica (via a local mutex) and across replicas (via an
// optional DistributedLocker).
type TransitionLock struct {
	mu   sync.Mutex
	dist DistributedLocker // nil = single-replica mode (local mutex only)
}

// NewTransitionLock creates a TransitionLock. Pass nil for single-replica mode.
func NewTransitionLock(dist DistributedLocker) *TransitionLock {
	return &TransitionLock{dist: dist}
}

// Lock acquires both the distributed lock (if configured) and the local mutex.
// If the distributed lock cannot be acquired, Lock returns false and the caller
// should skip the operation (another replica is handling it).
func (t *TransitionLock) Lock(ctx context.Context) bool {
	if t.dist != nil {
		acquired, err := t.dist.TryLock(ctx)
		if err != nil || !acquired {
			return false
		}
	}
	t.mu.Lock()
	return true
}

// Unlock releases the local mutex and the distributed lock (if configured).
func (t *TransitionLock) Unlock(ctx context.Context) {
	t.mu.Unlock()
	if t.dist != nil {
		_ = t.dist.Unlock(ctx)
	}
}
