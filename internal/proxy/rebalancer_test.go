package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestLeastLoadedStrategy_ComputeMoves(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2", "b3"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u2": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u3": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u4": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u5": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u6": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment"},
	}

	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	// b1 has 5, ideal is 2 (6/3), excess = 5 - 2 - 1 = 2 moves
	if len(moves) != 2 {
		t.Errorf("expected 2 moves, got %d", len(moves))
	}
	for _, m := range moves {
		if m.FromBackend != "b1" {
			t.Errorf("expected moves from b1, got %s", m.FromBackend)
		}
	}
}

func TestLeastLoadedStrategy_AlreadyBalanced(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u2": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment"},
	}

	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) != 0 {
		t.Errorf("expected 0 moves for balanced state, got %d", len(moves))
	}
}

func TestConsistentHashStrategy_ComputeMoves(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2", "b3"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment"},
		"u2": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment"},
	}

	s := &ConsistentHashStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	// Moves depend on hash values; just verify structure
	for _, m := range moves {
		if m.FromBackend == "" || m.ToBackend == "" {
			t.Errorf("move should have both from and to backends")
		}
		if m.RoutingKey == "" {
			t.Errorf("move should have a routing key")
		}
	}
}

func TestLeastLoadedStrategy_EmptyBackends(t *testing.T) {
	t.Parallel()

	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(map[string]*Assignment{}, nil)

	if moves != nil {
		t.Errorf("expected nil moves for empty backends, got %v", moves)
	}
}

func TestLeastLoadedStrategy_WeightedMoves(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	assignments := map[string]*Assignment{
		"heavy":  {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 10},
		"light1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
		"light2": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
	}

	// Total weight = 12, ideal per backend = 6.
	// b1 has weight 11, b2 has weight 1.
	// Excess for b1 = 11 - 6 - 1 = 4 -> should move some users.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) == 0 {
		t.Fatal("expected at least 1 move from overloaded b1")
	}
	for _, m := range moves {
		if m.FromBackend != "b1" {
			t.Errorf("expected moves from b1, got %s", m.FromBackend)
		}
	}
}

func TestLeastLoadedStrategy_WeightedBalanced(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	assignments := map[string]*Assignment{
		"a": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"b": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
	}

	// Total weight = 10, ideal = 5. Each backend has 5 → balanced.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) != 0 {
		t.Errorf("expected 0 moves for balanced weighted state, got %d", len(moves))
	}
}

func TestLeastLoadedStrategy_MixedWeightsStopsAtExcess(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2", "b3"}
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"u2": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"u3": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 5},
		"u4": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
		"u5": {Backend: "b3", AssignedAt: time.Now(), Source: "assignment", Weight: 1},
	}

	// Total weight = 17, ideal = 5 (17/3).
	// b1 weight = 15, excess = 15 - 5 - 1 = 9.
	// Moving users from b1 should stop once moved weight >= excess.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	totalMoved := 0
	for _, m := range moves {
		if m.FromBackend != "b1" {
			t.Errorf("expected moves from b1, got %s", m.FromBackend)
		}
		totalMoved += assignments[m.RoutingKey].EffectiveWeight()
	}

	if totalMoved < 9 {
		t.Errorf("expected to move at least weight 9, moved %d", totalMoved)
	}
}

func TestLeastLoadedStrategy_DefaultWeightOne(t *testing.T) {
	t.Parallel()

	backends := []string{"b1", "b2"}
	// Weight=0 should be treated as 1 by EffectiveWeight.
	assignments := map[string]*Assignment{
		"u1": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u2": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u3": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u4": {Backend: "b1", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
		"u5": {Backend: "b2", AssignedAt: time.Now(), Source: "assignment", Weight: 0},
	}

	// Total effective weight = 5, ideal = 2.
	// b1 effective weight = 4, excess = 4 - 2 - 1 = 1.
	s := &LeastLoadedStrategy{}
	moves := s.ComputeMoves(assignments, backends)

	if len(moves) != 1 {
		t.Errorf("expected 1 move, got %d", len(moves))
	}
}

func TestRebalancer_PreservesWeightsInReassignment(t *testing.T) {
	t.Parallel()

	store := newFakeStore([]string{"b1", "b2"})
	store.assignments["heavy"] = &Assignment{Backend: "b1", Weight: 10, Source: "assignment", AssignedAt: time.Now()}
	store.assignments["light"] = &Assignment{Backend: "b1", Weight: 1, Source: "assignment", AssignedAt: time.Now()}
	store.assignments["other"] = &Assignment{Backend: "b2", Weight: 1, Source: "assignment", AssignedAt: time.Now()}

	cache := NewUserCache(time.Minute)
	t.Cleanup(cache.Stop)

	rb := NewRebalancer(&LeastLoadedStrategy{}, store, nil, cache, nil, nil, nil, &TransitionLock{}, true)
	rb.rebalance(context.Background(), []string{"b1", "b2"})

	store.mu.Lock()
	defer store.mu.Unlock()

	// Verify that BulkAssign was called with correct weights.
	if len(store.bulkCalls) == 0 {
		t.Fatal("expected at least 1 BulkAssign call")
	}

	lastCall := store.bulkCalls[len(store.bulkCalls)-1]
	for key, entry := range lastCall {
		switch key {
		case "heavy":
			if entry.Weight != 10 {
				t.Errorf("heavy reassigned with weight %d, want 10", entry.Weight)
			}
		case "light":
			if entry.Weight != 1 {
				t.Errorf("light reassigned with weight %d, want 1", entry.Weight)
			}
		}
	}
}

// forceMoveStrategy always moves user-1 from its current backend to the other.
type forceMoveStrategy struct{}

func (s *forceMoveStrategy) ComputeMoves(assignments map[string]*Assignment, activeBackends []string) []Move {
	a, ok := assignments["user-1"]
	if !ok || len(activeBackends) < 2 {
		return nil
	}
	target := activeBackends[0]
	if target == a.Backend {
		target = activeBackends[1]
	}
	return []Move{{RoutingKey: "user-1", FromBackend: a.Backend, ToBackend: target}}
}

// rebalancerWSTestSetup creates a ConnTracker with a running WSBridge
// (client <-> echoBackend1) and a fakeStore. echoBackend2 is the swap target.
func rebalancerWSTestSetup(t *testing.T) (
	store *fakeStore, ct *ConnTracker,
	client *websocket.Conn, echoBackend2 *httptest.Server,
) {
	t.Helper()

	echoBackend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				break
			}
			_ = c.WriteMessage(mt, []byte("b1:"+string(data)))
		}
		_ = c.Close()
	}))
	t.Cleanup(echoBackend1.Close)

	echoBackend2 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				break
			}
			_ = c.WriteMessage(mt, []byte("b2:"+string(data)))
		}
		_ = c.Close()
	}))
	t.Cleanup(echoBackend2.Close)

	// Dial backend1 as the initial backend connection.
	wsURL := "ws" + strings.TrimPrefix(echoBackend1.URL, "http")
	backendConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Set up a client pair through a bridge server.
	bridgeCh := make(chan *WSBridge, 1)
	bridgeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		serverSide, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		bridge := newWSBridge(serverSide, backendConn, "user-1", nil, "/", "")
		bridgeCh <- bridge
		bridge.run()
	}))
	t.Cleanup(bridgeSrv.Close)

	clientURL := "ws" + strings.TrimPrefix(bridgeSrv.URL, "http")
	client, resp2, err := websocket.DefaultDialer.Dial(clientURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	t.Cleanup(func() { _ = client.Close() })

	bridge := <-bridgeCh

	// Verify the bridge works before handing it off.
	if err := client.WriteMessage(websocket.TextMessage, []byte("init")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "b1:init" {
		t.Fatalf("setup: expected %q, got %q", "b1:init", string(data))
	}

	ct = NewConnTracker()
	ct.Add("user-1", bridge)

	// Store: only user-1 on b1 so the forced strategy always moves it.
	store = newFakeStore([]string{echoBackend1.URL, echoBackend2.URL})
	store.assignments["user-1"] = &Assignment{
		Backend: echoBackend1.URL, Weight: 1,
		Source: "assignment", AssignedAt: time.Now(),
	}

	return store, ct, client, echoBackend2
}

func TestRebalancer_WSSwapOnRebalanceTrue(t *testing.T) {
	t.Parallel()

	store, ct, client, _ := rebalancerWSTestSetup(t)

	cache := NewUserCache(time.Minute)
	t.Cleanup(cache.Stop)

	rb := NewRebalancer(&forceMoveStrategy{}, store, nil, cache, ct, nil, nil, &TransitionLock{}, true)
	rb.rebalance(context.Background(), store.backends)

	// Give the swap time to dial the new backend.
	time.Sleep(200 * time.Millisecond)

	// Client connection should still be open and routed through b2.
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := client.WriteMessage(websocket.TextMessage, []byte("after")); err != nil {
		t.Fatalf("write after swap: %v", err)
	}
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read after swap: %v", err)
	}
	if got := string(data); got != "b2:after" {
		t.Fatalf("expected %q, got %q", "b2:after", got)
	}
}

func TestRebalancer_WSSwapOnRebalanceFalse(t *testing.T) {
	t.Parallel()

	store, ct, client, _ := rebalancerWSTestSetup(t)

	cache := NewUserCache(time.Minute)
	t.Cleanup(cache.Stop)

	rb := NewRebalancer(&forceMoveStrategy{}, store, nil, cache, ct, nil, nil, &TransitionLock{}, false)
	rb.rebalance(context.Background(), store.backends)

	// Give the close time to propagate.
	time.Sleep(200 * time.Millisecond)

	// Client connection should be closed (GoingAway frame sent).
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := client.ReadMessage()
	if err == nil {
		t.Fatal("expected read error on closed connection, got nil")
	}
}
