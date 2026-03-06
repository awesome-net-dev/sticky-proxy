package proxy

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ConnTracker tracks active WebSocket connections by routing key.
// When a user is drained or rebalanced, their connections are closed
// so the client reconnects and gets routed to the new backend.
type ConnTracker struct {
	mu    sync.Mutex
	conns map[string]map[*websocket.Conn]struct{}
}

// NewConnTracker creates an empty connection tracker.
func NewConnTracker() *ConnTracker {
	return &ConnTracker{
		conns: make(map[string]map[*websocket.Conn]struct{}),
	}
}

// Add registers a WebSocket connection for the given routing key.
func (ct *ConnTracker) Add(routingKey string, conn *websocket.Conn) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.conns[routingKey] == nil {
		ct.conns[routingKey] = make(map[*websocket.Conn]struct{})
	}
	ct.conns[routingKey][conn] = struct{}{}
}

// Remove unregisters a WebSocket connection for the given routing key.
func (ct *ConnTracker) Remove(routingKey string, conn *websocket.Conn) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if s := ct.conns[routingKey]; s != nil {
		delete(s, conn)
		if len(s) == 0 {
			delete(ct.conns, routingKey)
		}
	}
}

// CloseConns sends a GoingAway close frame and closes all WebSocket
// connections for the given routing key. The close frame tells clients
// the backend was reassigned and they should reconnect.
func (ct *ConnTracker) CloseConns(routingKey string) {
	ct.mu.Lock()
	conns := ct.conns[routingKey]
	delete(ct.conns, routingKey)
	ct.mu.Unlock()

	for conn := range conns {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "backend reassigned"),
			time.Now().Add(5*time.Second),
		)
		_ = conn.Close()
	}
}

// Count returns the number of tracked connections for a routing key.
func (ct *ConnTracker) Count(routingKey string) int {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return len(ct.conns[routingKey])
}
