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

	// Use a real WebSocket pair so the conn is non-nil and closeable.
	c1, cleanup1 := testWSConn(t)
	defer cleanup1()
	c2, cleanup2 := testWSConn(t)
	defer cleanup2()

	ct.Add("user1", c1)
	if ct.Count("user1") != 1 {
		t.Fatalf("expected 1, got %d", ct.Count("user1"))
	}

	ct.Add("user1", c2)
	if ct.Count("user1") != 2 {
		t.Fatalf("expected 2, got %d", ct.Count("user1"))
	}

	ct.Remove("user1", c1)
	if ct.Count("user1") != 1 {
		t.Fatalf("expected 1 after remove, got %d", ct.Count("user1"))
	}

	ct.Remove("user1", c2)
	if ct.Count("user1") != 0 {
		t.Fatalf("expected 0 after remove all, got %d", ct.Count("user1"))
	}
}

func TestConnTracker_RemoveUnknownNoPanic(t *testing.T) {
	t.Parallel()
	ct := NewConnTracker()
	c, cleanup := testWSConn(t)
	defer cleanup()
	ct.Remove("nobody", c) // should not panic
}

func TestConnTracker_CloseConns(t *testing.T) {
	t.Parallel()

	ct := NewConnTracker()
	client, cleanup := testWSConn(t)
	defer cleanup()

	ct.Add("user1", client)

	// CloseConns should remove the entry and close the connection.
	ct.CloseConns("user1")

	if ct.Count("user1") != 0 {
		t.Fatal("expected 0 after CloseConns")
	}

	// Reading from a closed connection should fail.
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := client.ReadMessage()
	if err == nil {
		t.Fatal("expected read error on closed connection")
	}
}

func TestConnTracker_CloseConnsUnknownNoPanic(t *testing.T) {
	t.Parallel()
	ct := NewConnTracker()
	ct.CloseConns("nobody") // should not panic
}

// testWSConn creates a real client WebSocket connection backed by a minimal
// echo server. Returns the client conn and a cleanup function.
func testWSConn(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Hold the connection open until closed by the other side.
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				break
			}
		}
		_ = c.Close()
	}))

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	_ = resp.Body.Close()

	return conn, func() {
		_ = conn.Close()
		srv.Close()
	}
}
