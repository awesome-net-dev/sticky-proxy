package proxy

import (
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var wsDialer = &websocket.Dialer{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// proxyWebSocket upgrades the client connection and dials the backend,
// then relays messages bidirectionally until either side closes.
// If a ConnTracker is provided, the client connection is registered so
// that drain/rebalance can close it to force a reconnect to the new backend.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, backend, routingKey string, ct *ConnTracker) {
	backendURL, err := url.Parse(backend)
	if err != nil {
		http.Error(w, "bad backend address", http.StatusBadGateway)
		return
	}

	switch backendURL.Scheme {
	case "https":
		backendURL.Scheme = "wss"
	default:
		backendURL.Scheme = "ws"
	}
	backendURL.Path = r.URL.Path
	backendURL.RawQuery = r.URL.RawQuery

	// Forward relevant headers to the backend.
	reqHeader := http.Header{}
	if auth := r.Header.Get("Authorization"); auth != "" {
		reqHeader.Set("Authorization", auth)
	}
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		reqHeader.Set("Cookie", cookie)
	}
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		reqHeader.Set("Sec-WebSocket-Protocol", proto)
	}

	// Dial the backend WebSocket endpoint.
	backendConn, resp, err := wsDialer.Dial(backendURL.String(), reqHeader)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		log.Printf("ws: backend dial error: %v", err)
		http.Error(w, "websocket backend unavailable", http.StatusBadGateway)
		return
	}
	defer func() { _ = backendConn.Close() }()

	// Upgrade the client connection.
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: client upgrade error: %v", err)
		return
	}
	defer func() { _ = clientConn.Close() }()

	if ct != nil {
		ct.Add(routingKey, clientConn)
		defer ct.Remove(routingKey, clientConn)
	}

	// Use a single channel to detect when one direction finishes.
	done := make(chan struct{}, 1)

	// client → backend in a separate goroutine.
	go func() {
		relay(backendConn, clientConn)
		done <- struct{}{}
	}()

	// backend → client runs in the current goroutine (saves one goroutine).
	relay(clientConn, backendConn)

	// Wait for the other direction to notice the closed connection.
	<-done
}

// relay reads messages from src and writes them to dst until an error occurs.
func relay(dst, src *websocket.Conn) {
	for {
		msgType, msg, err := src.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
			) {
				_ = dst.WriteMessage(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				)
			}
			if err != io.EOF {
				log.Printf("ws: relay error: %v", err)
			}
			return
		}

		if err := dst.WriteMessage(msgType, msg); err != nil {
			return
		}
	}
}
