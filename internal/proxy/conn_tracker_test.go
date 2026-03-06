package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnTracker_AddRemoveCount(t *testing.T) {
	t.Parallel()

	ct := NewConnTracker()

	if ct.Count("user1") != 0 {
		t.Fatal("expected 0 connections for unknown user")
	}

	b1, cleanup1 := testWSBridge(t)
	defer cleanup1()
	b2, cleanup2 := testWSBridge(t)
	defer cleanup2()

	ct.Add("user1", b1)
	if ct.Count("user1") != 1 {
		t.Fatalf("expected 1, got %d", ct.Count("user1"))
	}

	ct.Add("user1", b2)
	if ct.Count("user1") != 2 {
		t.Fatalf("expected 2, got %d", ct.Count("user1"))
	}

	ct.Remove("user1", b1)
	if ct.Count("user1") != 1 {
		t.Fatalf("expected 1 after remove, got %d", ct.Count("user1"))
	}

	ct.Remove("user1", b2)
	if ct.Count("user1") != 0 {
		t.Fatalf("expected 0 after remove all, got %d", ct.Count("user1"))
	}
}

func TestConnTracker_RemoveUnknownNoPanic(t *testing.T) {
	t.Parallel()
	ct := NewConnTracker()
	b, cleanup := testWSBridge(t)
	defer cleanup()
	ct.Remove("nobody", b) // should not panic
}

func TestConnTracker_CloseConns(t *testing.T) {
	t.Parallel()

	ct := NewConnTracker()
	bridge, cleanup := testWSBridge(t)
	defer cleanup()

	ct.Add("user1", bridge)

	// CloseConns should remove the entry and close the client connection.
	ct.CloseConns("user1")

	if ct.Count("user1") != 0 {
		t.Fatal("expected 0 after CloseConns")
	}

	// Reading from a closed client connection should fail.
	_ = bridge.client.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := bridge.client.ReadMessage()
	if err == nil {
		t.Fatal("expected read error on closed connection")
	}
}

func TestConnTracker_CloseConnsUnknownNoPanic(t *testing.T) {
	t.Parallel()
	ct := NewConnTracker()
	ct.CloseConns("nobody") // should not panic
}

func TestConnTracker_SwapConns(t *testing.T) {
	t.Parallel()

	// Set up an echo backend to swap to.
	echoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				break
			}
			_ = c.WriteMessage(mt, data)
		}
		_ = c.Close()
	}))
	defer echoSrv.Close()

	ct := NewConnTracker()
	bridge, cleanup := testWSBridge(t)
	defer cleanup()

	ct.Add("user1", bridge)

	// Swap to the echo server.
	newURL := "http" + strings.TrimPrefix(echoSrv.URL, "http")
	ct.SwapConns("user1", newURL)

	// Bridge still tracked (swap doesn't remove).
	if ct.Count("user1") != 1 {
		t.Fatalf("expected 1 after swap, got %d", ct.Count("user1"))
	}
}

func TestConnTracker_SwapConnsUnknownNoPanic(t *testing.T) {
	t.Parallel()
	ct := NewConnTracker()
	ct.SwapConns("nobody", "http://localhost:1234") // should not panic
}

// testWSBridge creates a WSBridge with a real client+backend WebSocket pair.
// The backend is a minimal hold-open server. Returns the bridge and a cleanup function.
func testWSBridge(t *testing.T) (*WSBridge, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
		_ = c.Close()
	}))

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		srv.Close()
		t.Fatalf("client dial: %v", err)
	}
	_ = resp.Body.Close()

	backendConn, resp2, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp2 != nil {
			_ = resp2.Body.Close()
		}
		_ = clientConn.Close()
		srv.Close()
		t.Fatalf("backend dial: %v", err)
	}
	_ = resp2.Body.Close()

	bridge := newWSBridge(clientConn, backendConn, "test-user", nil, "/", "")

	return bridge, func() {
		_ = clientConn.Close()
		_ = backendConn.Close()
		srv.Close()
	}
}
