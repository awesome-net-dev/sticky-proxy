package proxy

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTransitionLock_NilLocker_AlwaysAcquires(t *testing.T) {
	t.Parallel()

	tl := NewTransitionLock(nil)
	ctx := context.Background()

	if !tl.Lock(ctx) {
		t.Fatal("expected Lock to succeed with nil distributed locker")
	}
	tl.Unlock(ctx)
}

func TestTransitionLock_LocalMutualExclusion(t *testing.T) {
	t.Parallel()

	tl := NewTransitionLock(nil)
	ctx := context.Background()

	if !tl.Lock(ctx) {
		t.Fatal("first Lock should succeed")
	}

	// Second Lock from another goroutine should block until Unlock.
	var started, finished atomic.Bool
	started.Store(false)
	finished.Store(false)

	go func() {
		started.Store(true)
		if !tl.Lock(ctx) {
			return
		}
		finished.Store(true)
		tl.Unlock(ctx)
	}()

	// Give goroutine time to start and block on mutex.
	for !started.Load() {
	}

	if finished.Load() {
		t.Fatal("second Lock should block while first is held")
	}

	tl.Unlock(ctx)

	// Wait for second goroutine to finish.
	for !finished.Load() {
	}
}

// fakeDistributedLocker is a test double for DistributedLocker.
type fakeDistributedLocker struct {
	mu       sync.Mutex
	locked   bool
	lockErr  error
	unlocked bool
}

func (f *fakeDistributedLocker) TryLock(_ context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockErr != nil {
		return false, f.lockErr
	}
	if f.locked {
		return false, nil
	}
	f.locked = true
	return true, nil
}

func (f *fakeDistributedLocker) Unlock(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locked = false
	f.unlocked = true
	return nil
}

func TestTransitionLock_DistributedLockAcquired(t *testing.T) {
	t.Parallel()

	dl := &fakeDistributedLocker{}
	tl := NewTransitionLock(dl)
	ctx := context.Background()

	if !tl.Lock(ctx) {
		t.Fatal("expected Lock to succeed when distributed lock is free")
	}
	tl.Unlock(ctx)

	if !dl.unlocked {
		t.Fatal("expected distributed lock to be released on Unlock")
	}
}

func TestTransitionLock_DistributedLockContended(t *testing.T) {
	t.Parallel()

	dl := &fakeDistributedLocker{locked: true} // another replica holds it
	tl := NewTransitionLock(dl)
	ctx := context.Background()

	if tl.Lock(ctx) {
		t.Fatal("expected Lock to fail when distributed lock is held by another replica")
	}
}

func TestTransitionLock_DistributedLockSequential(t *testing.T) {
	t.Parallel()

	dl := &fakeDistributedLocker{}
	tl := NewTransitionLock(dl)
	ctx := context.Background()

	// First lock/unlock cycle.
	if !tl.Lock(ctx) {
		t.Fatal("first Lock should succeed")
	}
	tl.Unlock(ctx)

	// Second lock/unlock cycle — lock should be available again.
	if !tl.Lock(ctx) {
		t.Fatal("second Lock should succeed after Unlock")
	}
	tl.Unlock(ctx)
}
