package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

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
// then relays messages bidirectionally using a WSBridge. The bridge supports
// transparent backend swaps during rebalance — the client connection stays
// open while only the backend connection is replaced.
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
		slog.Error("ws: backend dial error", "error", err)
		http.Error(w, "websocket backend unavailable", http.StatusBadGateway)
		return
	}

	// Upgrade the client connection.
	clientConn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = backendConn.Close()
		slog.Error("ws: client upgrade error", "error", err)
		return
	}

	bridge := newWSBridge(clientConn, backendConn, routingKey, reqHeader, r.URL.Path, r.URL.RawQuery)

	if ct != nil {
		ct.Add(routingKey, bridge)
		defer ct.Remove(routingKey, bridge)
	}

	bridge.run()
}

// wsMsg holds a WebSocket message read from a connection.
type wsMsg struct {
	msgType int
	data    []byte
	err     error
}

// WSBridge relays WebSocket traffic between a client and a backend,
// supporting transparent backend swaps. When SwapBackend is called,
// the old backend connection is closed, a new one is dialed, and relay
// continues — the client connection stays open throughout.
type WSBridge struct {
	client     *websocket.Conn
	clientMu   sync.Mutex // serializes writes to client conn
	routingKey string
	reqHeader  http.Header
	path       string
	rawQuery   string

	backend   *websocket.Conn
	backendMu sync.Mutex  // serializes writes to backend conn
	swapCh    chan string // buffered(1), receives new backend URL

	// Lifecycle management for reader goroutines.
	clientCancel context.CancelFunc // cancels client reader goroutine
	readerCancel context.CancelFunc // cancels backend reader goroutine
}

func newWSBridge(client, backend *websocket.Conn, routingKey string, header http.Header, path, rawQuery string) *WSBridge {
	return &WSBridge{
		client:     client,
		routingKey: routingKey,
		reqHeader:  header,
		path:       path,
		rawQuery:   rawQuery,
		backend:    backend,
		swapCh:     make(chan string, 1),
	}
}

// SwapBackend requests a transparent backend swap. The client connection
// stays open; only the backend connection is replaced.
func (b *WSBridge) SwapBackend(newURL string) {
	select {
	case b.swapCh <- newURL:
	default:
	}
	_ = b.backend.Close()
}

// Close sends a GoingAway frame and closes the client connection.
// Used during drain when the backend is being removed entirely.
func (b *WSBridge) Close() {
	b.clientMu.Lock()
	_ = b.client.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseGoingAway, "backend reassigned"),
		time.Now().Add(5*time.Second),
	)
	b.clientMu.Unlock()
	_ = b.client.Close()
}

func (b *WSBridge) writeClient(msgType int, data []byte) error {
	b.clientMu.Lock()
	defer b.clientMu.Unlock()
	return b.client.WriteMessage(msgType, data)
}

func (b *WSBridge) writeBackend(msgType int, data []byte) error {
	b.backendMu.Lock()
	defer b.backendMu.Unlock()
	return b.backend.WriteMessage(msgType, data)
}

func (b *WSBridge) run() {
	clientCtx, clientCancel := context.WithCancel(context.Background())
	b.clientCancel = clientCancel

	defer func() {
		clientCancel()
		_ = b.client.Close()
		_ = b.backend.Close()
		if b.readerCancel != nil {
			b.readerCancel()
		}
	}()

	// Single client reader goroutine for the bridge lifetime.
	clientCh := make(chan wsMsg, 8)
	go wsReadPump(clientCtx, b.client, clientCh)

	backendCh := b.startBackendReader()

	for {
		select {
		case msg := <-clientCh:
			if msg.err != nil {
				return
			}
			if err := b.writeBackend(msg.msgType, msg.data); err != nil {
				if newURL := b.pendingSwap(); newURL != "" {
					if ch := b.doSwap(newURL, clientCh); ch != nil {
						backendCh = ch
						continue
					}
				}
				return
			}

		case msg := <-backendCh:
			if msg.err != nil {
				if newURL := b.pendingSwap(); newURL != "" {
					if ch := b.doSwap(newURL, clientCh); ch != nil {
						backendCh = ch
						continue
					}
				}
				// Normal backend close — send close frame to client.
				if websocket.IsCloseError(msg.err,
					websocket.CloseNormalClosure,
					websocket.CloseGoingAway,
				) {
					b.clientMu.Lock()
					_ = b.client.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					b.clientMu.Unlock()
				}
				return
			}
			if err := b.writeClient(msg.msgType, msg.data); err != nil {
				return
			}

		case newURL := <-b.swapCh:
			_ = b.backend.Close()
			<-backendCh // drain error from closed backend reader
			if ch := b.doSwap(newURL, clientCh); ch != nil {
				backendCh = ch
				continue
			}
			return
		}
	}
}

// doSwap dials the new backend, forwards any queued client messages,
// and starts a new backend reader. Returns the new backend channel, or nil on failure.
func (b *WSBridge) doSwap(newURL string, clientCh <-chan wsMsg) <-chan wsMsg {
	// Cancel the old backend reader goroutine.
	if b.readerCancel != nil {
		b.readerCancel()
		b.readerCancel = nil
	}

	newConn, err := b.dialBackend(newURL)
	if err != nil {
		slog.Error("ws: swap dial failed", "routingKey", b.routingKey, "error", err)
		return nil
	}
	_ = b.backend.Close() // close old (may already be closed)

	b.backendMu.Lock()
	b.backend = newConn
	b.backendMu.Unlock()

	// Forward any client messages queued during the swap.
	for {
		select {
		case msg := <-clientCh:
			if msg.err != nil {
				return nil
			}
			if err := b.writeBackend(msg.msgType, msg.data); err != nil {
				return nil
			}
		default:
			slog.Debug("ws: backend swapped", "routingKey", b.routingKey, "newBackend", newURL)
			return b.startBackendReader()
		}
	}
}

func (b *WSBridge) startBackendReader() <-chan wsMsg {
	// Cancel any previous reader goroutine.
	if b.readerCancel != nil {
		b.readerCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.readerCancel = cancel

	ch := make(chan wsMsg, 8)
	go wsReadPump(ctx, b.backend, ch)
	return ch
}

func (b *WSBridge) pendingSwap() string {
	select {
	case u := <-b.swapCh:
		return u
	default:
		return ""
	}
}

func (b *WSBridge) dialBackend(backendURL string) (*websocket.Conn, error) {
	u, err := url.Parse(backendURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = b.path
	u.RawQuery = b.rawQuery

	conn, resp, err := wsDialer.Dial(u.String(), b.reqHeader)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return conn, nil
}

// wsReadPump reads messages from conn and sends them to ch until an error
// occurs or the context is cancelled.
func wsReadPump(ctx context.Context, conn *websocket.Conn, ch chan<- wsMsg) {
	for {
		mt, data, err := conn.ReadMessage()
		select {
		case ch <- wsMsg{mt, data, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}
