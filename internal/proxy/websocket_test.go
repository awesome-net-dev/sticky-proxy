package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newEchoServer starts a WebSocket echo server that prefixes responses.
func newEchoServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_ = c.WriteMessage(mt, []byte(prefix+string(data)))
		}
		_ = c.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWSBridge_NormalRelay(t *testing.T) {
	t.Parallel()

	backend := newEchoServer(t, "b1:")

	// Dial backend.
	wsURL := "ws" + strings.TrimPrefix(backend.URL, "http")
	backendConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Create a client pair via a pipe-like server.
	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		serverSide, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		bridge := newWSBridge(serverSide, backendConn, "user-1", nil, "/", "")
		bridge.run()
	}))
	defer clientSrv.Close()

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	client, resp2, err := websocket.DefaultDialer.Dial(clientURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	defer func() { _ = client.Close() }()

	// Send a message and verify it's echoed through the bridge.
	if err := client.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "b1:hello" {
		t.Fatalf("expected %q, got %q", "b1:hello", string(data))
	}
}

func TestWSBridge_SwapBackend(t *testing.T) {
	t.Parallel()

	backend1 := newEchoServer(t, "b1:")
	backend2 := newEchoServer(t, "b2:")

	// Dial backend1.
	wsURL1 := "ws" + strings.TrimPrefix(backend1.URL, "http")
	backendConn, resp, err := websocket.DefaultDialer.Dial(wsURL1, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	bridgeCh := make(chan *WSBridge, 1)

	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		serverSide, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		bridge := newWSBridge(serverSide, backendConn, "user-1", nil, "/", "")
		bridgeCh <- bridge
		bridge.run()
	}))
	defer clientSrv.Close()

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	client, resp2, err := websocket.DefaultDialer.Dial(clientURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	defer func() { _ = client.Close() }()

	bridge := <-bridgeCh

	// Verify relay through backend1.
	if err := client.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "b1:ping" {
		t.Fatalf("before swap: expected %q, got %q", "b1:ping", string(data))
	}

	// Swap to backend2.
	bridge.SwapBackend(backend2.URL)

	// Give the bridge time to complete the swap.
	time.Sleep(100 * time.Millisecond)

	// Verify relay now goes through backend2.
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := client.WriteMessage(websocket.TextMessage, []byte("pong")); err != nil {
		t.Fatal(err)
	}
	_, data, err = client.ReadMessage()
	if err != nil {
		t.Fatalf("after swap: read error: %v", err)
	}
	if string(data) != "b2:pong" {
		t.Fatalf("after swap: expected %q, got %q", "b2:pong", string(data))
	}
}

func TestWSBridge_SwapPreservesClientConnection(t *testing.T) {
	t.Parallel()

	backend1 := newEchoServer(t, "a:")
	backend2 := newEchoServer(t, "b:")

	wsURL1 := "ws" + strings.TrimPrefix(backend1.URL, "http")
	backendConn, resp, err := websocket.DefaultDialer.Dial(wsURL1, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	bridgeCh := make(chan *WSBridge, 1)

	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		serverSide, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		bridge := newWSBridge(serverSide, backendConn, "user-1", nil, "/", "")
		bridgeCh <- bridge
		bridge.run()
	}))
	defer clientSrv.Close()

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	client, resp2, err := websocket.DefaultDialer.Dial(clientURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	defer func() { _ = client.Close() }()

	bridge := <-bridgeCh

	// Send multiple messages, swap in between, verify the client connection
	// stays open the entire time.
	for i := 0; i < 3; i++ {
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := client.WriteMessage(websocket.TextMessage, []byte("msg")); err != nil {
			t.Fatalf("iteration %d: write error: %v", i, err)
		}
		_, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("iteration %d: read error: %v", i, err)
		}
		if i == 0 {
			if string(data) != "a:msg" {
				t.Fatalf("iteration %d: expected %q, got %q", i, "a:msg", string(data))
			}
		}

		if i == 0 {
			bridge.SwapBackend(backend2.URL)
			time.Sleep(100 * time.Millisecond)
		}
	}

	// After swap, messages should go through backend2.
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := client.WriteMessage(websocket.TextMessage, []byte("final")); err != nil {
		t.Fatal(err)
	}
	_, data, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "b:final" {
		t.Fatalf("expected %q, got %q", "b:final", string(data))
	}
}

func TestWSBridge_CloseTerminatesRelay(t *testing.T) {
	t.Parallel()

	backend := newEchoServer(t, "")

	wsURL := "ws" + strings.TrimPrefix(backend.URL, "http")
	backendConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	bridgeCh := make(chan *WSBridge, 1)
	doneCh := make(chan struct{})

	clientSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		serverSide, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		bridge := newWSBridge(serverSide, backendConn, "user-1", nil, "/", "")
		bridgeCh <- bridge
		bridge.run()
		close(doneCh)
	}))
	defer clientSrv.Close()

	clientURL := "ws" + strings.TrimPrefix(clientSrv.URL, "http")
	client, resp2, err := websocket.DefaultDialer.Dial(clientURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	defer func() { _ = client.Close() }()

	bridge := <-bridgeCh

	// Close the bridge (simulating drain).
	bridge.Close()

	// The bridge's run() should exit.
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge.run() did not exit after Close")
	}
}
