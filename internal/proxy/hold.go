package proxy

import (
	"context"
	"sync"
	"time"
)

// HoldManager tracks routing keys that are "in transition" during drains and
// rebalances. Requests for transitioning keys are held (blocked) until the
// transition completes or the hold timeout expires, preventing clients from
// seeing errors during reassignment.
type HoldManager struct {
	mu      sync.Mutex
	holds   map[string]chan struct{}
	timeout time.Duration
}

// NewHoldManager creates a HoldManager with the given per-request hold timeout.
func NewHoldManager(timeout time.Duration) *HoldManager {
	return &HoldManager{
		holds:   make(map[string]chan struct{}),
		timeout: timeout,
	}
}

// MarkTransition marks routing keys as "in transition". Requests for these
// keys will be held until ClearTransition is called. Keys already in
// transition are left unchanged.
func (h *HoldManager) MarkTransition(routingKeys []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, key := range routingKeys {
		if _, ok := h.holds[key]; !ok {
			h.holds[key] = make(chan struct{})
		}
	}
}

// ClearTransition releases all waiters for the given routing keys and removes
// them from the transition set.
func (h *HoldManager) ClearTransition(routingKeys []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, key := range routingKeys {
		if ch, ok := h.holds[key]; ok {
			close(ch)
			delete(h.holds, key)
		}
	}
}

// Wait blocks until the routing key's transition completes, the hold timeout
// expires, or the context is cancelled. Returns true if the transition
// completed, false on timeout or cancellation. Returns false immediately if
// the key is not in transition.
func (h *HoldManager) Wait(ctx context.Context, routingKey string) bool {
	h.mu.Lock()
	ch, ok := h.holds[routingKey]
	h.mu.Unlock()
	if !ok {
		return false
	}

	IncHoldRequests()

	timer := time.NewTimer(h.timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		IncHoldTimeouts()
		return false
	case <-ctx.Done():
		return false
	}
}
