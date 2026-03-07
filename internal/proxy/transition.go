package proxy

import "sync"

// TransitionLock prevents concurrent drain and rebalance operations.
// Both DrainManager and Rebalancer must acquire this lock before
// modifying assignments, preventing interleaved state mutations.
type TransitionLock struct {
	mu sync.Mutex
}

func (t *TransitionLock) Lock()   { t.mu.Lock() }
func (t *TransitionLock) Unlock() { t.mu.Unlock() }
