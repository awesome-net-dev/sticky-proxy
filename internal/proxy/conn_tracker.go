package proxy

import "sync"

// ConnTracker tracks active WebSocket bridges by routing key.
// It supports both closing connections (for drain) and transparent
// backend swaps (for rebalance).
type ConnTracker struct {
	mu    sync.Mutex
	conns map[string]map[*WSBridge]struct{}
}

// NewConnTracker creates an empty connection tracker.
func NewConnTracker() *ConnTracker {
	return &ConnTracker{
		conns: make(map[string]map[*WSBridge]struct{}),
	}
}

// Add registers a WebSocket bridge for the given routing key.
func (ct *ConnTracker) Add(routingKey string, bridge *WSBridge) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.conns[routingKey] == nil {
		ct.conns[routingKey] = make(map[*WSBridge]struct{})
	}
	ct.conns[routingKey][bridge] = struct{}{}
}

// Remove unregisters a WebSocket bridge for the given routing key.
func (ct *ConnTracker) Remove(routingKey string, bridge *WSBridge) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if s := ct.conns[routingKey]; s != nil {
		delete(s, bridge)
		if len(s) == 0 {
			delete(ct.conns, routingKey)
		}
	}
}

// CloseConns sends a GoingAway close frame and closes all client connections
// for the given routing key. Used during drain when the backend is removed.
func (ct *ConnTracker) CloseConns(routingKey string) {
	ct.mu.Lock()
	bridges := ct.conns[routingKey]
	delete(ct.conns, routingKey)
	ct.mu.Unlock()

	for bridge := range bridges {
		bridge.Close()
	}
}

// SwapConns transparently swaps all WebSocket connections for the given
// routing key to a new backend without closing the client connection.
// Used during rebalance.
func (ct *ConnTracker) SwapConns(routingKey string, newBackend string) {
	ct.mu.Lock()
	bridges := make([]*WSBridge, 0, len(ct.conns[routingKey]))
	for bridge := range ct.conns[routingKey] {
		bridges = append(bridges, bridge)
	}
	ct.mu.Unlock()

	for _, bridge := range bridges {
		bridge.SwapBackend(newBackend)
	}
}

// Count returns the number of tracked connections for a routing key.
func (ct *ConnTracker) Count(routingKey string) int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return len(ct.conns[routingKey])
}
