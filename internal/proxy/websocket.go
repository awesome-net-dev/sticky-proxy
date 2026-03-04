package proxy

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// isWebSocketUpgrade checks whether the incoming request is a WebSocket
// upgrade request by inspecting the Connection and Upgrade headers.
func isWebSocketUpgrade(r *http.Request) bool {
	connHeader := strings.ToLower(r.Header.Get("Connection"))
	upgradeHeader := strings.ToLower(r.Header.Get("Upgrade"))

	return strings.Contains(connHeader, "upgrade") && upgradeHeader == "websocket"
}

// proxyWebSocket upgrades the client connection and dials the backend,
// then relays messages bidirectionally until either side closes.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, backend string) {
	// Build the backend WebSocket URL. The backend address is an HTTP URL
	// like "http://backend1:8080", so we convert scheme and append the
	// original request path.
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

	// Forward relevant headers to the backend (Authorization, cookies, etc.)
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
	backendConn, resp, err := websocket.DefaultDialer.Dial(backendURL.String(), reqHeader)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		log.Printf("ws: backend dial error: %v", err)
		http.Error(w, "websocket backend unavailable", http.StatusBadGateway)
		return
	}
	defer backendConn.Close()

	// Upgrade the client connection.
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: client upgrade error: %v", err)
		// Upgrade already wrote the HTTP error response.
		return
	}
	defer clientConn.Close()

	// errc is used to signal that one direction has finished.
	errc := make(chan error, 2)

	// client -> backend
	go relayCopy(errc, backendConn, clientConn)

	// backend -> client
	go relayCopy(errc, clientConn, backendConn)

	// Wait for either direction to finish, then let deferred closes
	// clean up both connections.
	<-errc
}

// relayCopy reads messages from src and writes them to dst.
// When src sends a close message or an error occurs the function
// returns the error on errc.
func relayCopy(errc chan<- error, dst, src *websocket.Conn) {
	for {
		msgType, msg, err := src.ReadMessage()
		if err != nil {
			// If we received a normal close, forward it to the other side.
			if websocket.IsCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway,
			) {
				dst.WriteMessage(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				)
			}
			// io.EOF is also expected on clean shutdown.
			if err != io.EOF {
				errc <- err
			} else {
				errc <- nil
			}
			return
		}

		if err := dst.WriteMessage(msgType, msg); err != nil {
			errc <- err
			return
		}
	}
}
