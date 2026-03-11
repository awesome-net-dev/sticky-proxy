package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var wsDialer = &websocket.Dialer{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// wsMsgBufPool provides reusable byte slices for reading WebSocket messages.
// The initial capacity (4KB) covers most broker messages without growing.
var wsMsgBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
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
	data    []byte  // message payload (subslice of pooled buffer)
	pool    *[]byte // pooled buffer to return after relay; nil for error-only messages
	err     error
}

// release returns the message buffer to the pool. Must be called after
// the message data is no longer needed (i.e. after write completes).
func (m *wsMsg) release() {
	if m.pool != nil {
		*m.pool = (*m.pool)[:0]
		wsMsgBufPool.Put(m.pool)
		m.pool = nil
		m.data = nil
	}
}

// WSBridge relays WebSocket traffic between a client and a backend,
// supporting transparent backend swaps. When SwapBackend is called,
// the old backend connection is closed, a new one is dialed, and relay
// continues — the client connection stays open throughout.
//
// Writes are coalesced through dedicated write-pump goroutines: messages
// are sent to a channel, and the pump drains as many as available before
// acquiring the connection mutex once and flushing the batch.
type WSBridge struct {
	client     *websocket.Conn
	clientMu   sync.Mutex // serializes writes to client conn
	routingKey string
	reqHeader  http.Header
	path       string
	rawQuery   string

	// Client write pump (lives for the entire bridge lifetime).
	clientWriteCh   chan wsMsg
	clientWriteErr  chan error
	clientWriteDone chan struct{}

	backend   *websocket.Conn
	backendMu sync.Mutex  // serializes writes to backend conn
	swapCh    chan string // buffered(1), receives new backend URL

	// Backend write pump (restarted on each swap).
	backendWriteCh   chan wsMsg
	backendWriteErr  chan error
	backendWriteDone chan struct{}

	// Lifecycle management for reader goroutines.
	clientCancel context.CancelFunc // cancels client reader goroutine
	readerCancel context.CancelFunc // cancels backend reader goroutine
}

func newWSBridge(client, backend *websocket.Conn, routingKey string, header http.Header, path, rawQuery string) *WSBridge {
	return &WSBridge{
		client:          client,
		routingKey:      routingKey,
		reqHeader:       header,
		path:            path,
		rawQuery:        rawQuery,
		clientWriteCh:   make(chan wsMsg, 16),
		clientWriteErr:  make(chan error, 1),
		clientWriteDone: make(chan struct{}),
		backend:         backend,
		swapCh:          make(chan string, 1),
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

// startBackendWritePump creates channels and launches the backend write pump goroutine.
func (b *WSBridge) startBackendWritePump() {
	b.backendWriteCh = make(chan wsMsg, 16)
	b.backendWriteErr = make(chan error, 1)
	b.backendWriteDone = make(chan struct{})
	go wsWritePump(b.backend, &b.backendMu, b.backendWriteCh, b.backendWriteErr, b.backendWriteDone)
}

// stopBackendWritePump signals the backend write pump to stop and drains
// any buffered messages. Safe to call multiple times.
func (b *WSBridge) stopBackendWritePump() {
	if b.backendWriteDone == nil {
		return
	}
	close(b.backendWriteDone)
	// Drain remaining messages so their pooled buffers are released.
	for {
		select {
		case msg := <-b.backendWriteCh:
			msg.release()
		default:
			b.backendWriteDone = nil
			return
		}
	}
}

// wsWritePump is a dedicated writer goroutine that coalesces multiple pending
// messages into a single batch, acquiring the connection mutex once per batch.
// This reduces mutex contention and syscall overhead under high throughput.
func wsWritePump(conn *websocket.Conn, mu *sync.Mutex, ch <-chan wsMsg, errCh chan<- error, done <-chan struct{}) {
	batch := make([]wsMsg, 0, 16)
	for {
		// Block until at least one message arrives or we're told to stop.
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			batch = append(batch, msg)
		case <-done:
			return
		}

		// Non-blocking drain: grab all additional pending messages.
	drain:
		for len(batch) < cap(batch) {
			select {
			case msg, ok := <-ch:
				if !ok {
					break drain
				}
				batch = append(batch, msg)
			default:
				break drain
			}
		}

		// Write the entire batch under a single mutex acquisition.
		mu.Lock()
		var writeErr error
		for i := range batch {
			if writeErr == nil {
				writeErr = conn.WriteMessage(batch[i].msgType, batch[i].data)
			}
			batch[i].release()
		}
		mu.Unlock()
		batch = batch[:0]

		if writeErr != nil {
			select {
			case errCh <- writeErr:
			default:
			}
			return
		}
	}
}

func (b *WSBridge) run() {
	clientCtx, clientCancel := context.WithCancel(context.Background())
	b.clientCancel = clientCancel

	defer func() {
		clientCancel()
		close(b.clientWriteDone)
		b.stopBackendWritePump()
		_ = b.client.Close()
		_ = b.backend.Close()
		if b.readerCancel != nil {
			b.readerCancel()
		}
	}()

	// Start write pumps.
	go wsWritePump(b.client, &b.clientMu, b.clientWriteCh, b.clientWriteErr, b.clientWriteDone)
	b.startBackendWritePump()

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
			// Send to backend write pump (ownership of pooled buffer transfers).
			select {
			case b.backendWriteCh <- msg:
			case err := <-b.backendWriteErr:
				msg.release()
				_ = err
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
			// Send to client write pump (ownership of pooled buffer transfers).
			select {
			case b.clientWriteCh <- msg:
			case err := <-b.clientWriteErr:
				msg.release()
				_ = err
				return
			}

		case <-b.backendWriteErr:
			if newURL := b.pendingSwap(); newURL != "" {
				if ch := b.doSwap(newURL, clientCh); ch != nil {
					backendCh = ch
					continue
				}
			}
			return

		case <-b.clientWriteErr:
			return

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
// and starts a new backend reader and write pump. Returns the new backend
// channel, or nil on failure.
func (b *WSBridge) doSwap(newURL string, clientCh <-chan wsMsg) <-chan wsMsg {
	// Cancel the old backend reader goroutine.
	if b.readerCancel != nil {
		b.readerCancel()
		b.readerCancel = nil
	}

	// Stop old backend write pump.
	b.stopBackendWritePump()

	newConn, err := b.dialBackend(newURL)
	if err != nil {
		slog.Error("ws: swap dial failed", "routingKey", b.routingKey, "error", err)
		return nil
	}
	_ = b.backend.Close() // close old (may already be closed)

	b.backendMu.Lock()
	b.backend = newConn
	b.backendMu.Unlock()

	// Start new backend write pump for the new connection.
	b.startBackendWritePump()

	// Forward any client messages queued during the swap.
	for {
		select {
		case msg := <-clientCh:
			if msg.err != nil {
				return nil
			}
			select {
			case b.backendWriteCh <- msg:
			default:
				msg.release()
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

// wsReadPump reads messages from conn using pooled buffers and sends them
// to ch until an error occurs or the context is cancelled.
func wsReadPump(ctx context.Context, conn *websocket.Conn, ch chan<- wsMsg) {
	for {
		mt, bp, err := wsReadPooled(conn)
		if err != nil {
			select {
			case ch <- wsMsg{msgType: mt, err: err}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ch <- wsMsg{msgType: mt, data: *bp, pool: bp}:
		case <-ctx.Done():
			*bp = (*bp)[:0]
			wsMsgBufPool.Put(bp)
			return
		}
	}
}

// wsReadPooled reads a single WebSocket message into a pooled buffer.
// For messages up to 4KB (typical broker messages), this is zero-alloc.
// Larger messages cause the buffer to grow; the grown buffer is returned
// to the pool so subsequent reads on the same connection also benefit.
func wsReadPooled(conn *websocket.Conn) (int, *[]byte, error) {
	mt, reader, err := conn.NextReader()
	if err != nil {
		return 0, nil, err
	}

	bp := wsMsgBufPool.Get().(*[]byte)
	if err := readAllPooled(reader, bp); err != nil {
		*bp = (*bp)[:0]
		wsMsgBufPool.Put(bp)
		return 0, nil, err
	}
	return mt, bp, nil
}

// readAllPooled reads all data from r into the pooled buffer, growing it
// as needed. On the hot path (message fits in existing capacity), no
// allocation occurs.
func readAllPooled(r io.Reader, bp *[]byte) error {
	b := (*bp)[:0]
	for {
		if len(b) == cap(b) {
			grown := make([]byte, len(b), cap(b)*2+4096)
			copy(grown, b)
			b = grown
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err == io.EOF {
			*bp = b
			return nil
		}
		if err != nil {
			*bp = b
			return err
		}
	}
}
