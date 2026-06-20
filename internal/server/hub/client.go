package hub

import (
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/internal/server/ratelimit"
	"github.com/pawlenartowicz/leyline/internal/server/stage"
)

// Client represents one authenticated WebSocket session. Fields are written
// once during ServeWS (before writePump/readPump start) and are read-only
// after that, except for mu/closed which guard the send-channel lifecycle.
type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	// send is the outbound frame queue. Capacity 64 provides enough buffer for
	// a bootstrap stream chunk + concurrent broadcasts. SendMsg drops the client
	// (closes send) when the channel is full — backpressure eviction.
	send     chan []byte
	vaultID  string
	label    string
	clientID stage.ClientID
	keyname  string
	role     string // identity-change signal + auth_ok payload; not used for authorization
	caps      caps.Set
	expiresAt time.Time
	// authHash is the 24-hex-char SHA256 prefix of the API key. Used by
	// ReevaluateClients to detect revocation or role changes without holding
	// the raw key material past the auth handshake.
	authHash  string
	ip        string

	// failedPushLimiter is a per-client circuit breaker for validation failures
	// (bad ops, pre-hash mismatches). Separate from the per-keyname push-rate
	// limiter so a single misbehaving connection can't exhaust the shared budget.
	failedPushLimiter *ratelimit.Limiter

	mu     sync.Mutex
	closed bool
}

func newClient(hub *Hub, conn *websocket.Conn, failedLimit int) *Client {
	return &Client{
		hub:               hub,
		conn:              conn,
		send:              make(chan []byte, 64),
		failedPushLimiter: ratelimit.New(failedLimit, time.Minute),
	}
}

func (c *Client) sendError(code, message, path string) {
	c.SendMsg(protocol.ErrorMsg{Type: protocol.MsgError, Code: code, Message: message, Path: path})
}

// SendMsg CBOR-encodes msg and enqueues it on the send channel. The writePump
// pulls from that channel and writes each frame as a single WS binary message.
func (c *Client) SendMsg(msg any) error {
	data, err := protocol.Encode(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	select {
	case c.send <- data:
	default:
		c.close()
	}
	return nil
}

// close is the internal (unlocked) closer. Caller must hold c.mu.
func (c *Client) close() {
	if !c.closed {
		c.closed = true
		if c.send != nil {
			close(c.send)
		}
	}
}

func (c *Client) Close() {
	c.CloseWithReason("")
}

func (c *Client) CloseWithReason(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason != "" && c.conn != nil {
		msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
		c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
	}
	c.close()
	if c.conn != nil {
		c.conn.Close()
	}
}

// closeWithProtocolMismatch sends a WS close frame with code 1002 and the
// canonical protocol-mismatch reason, then closes the underlying connection.
// Used by handleMessage when a frame fails to decode as a v1 CBOR envelope
// or carries an unknown MsgType.
func (c *Client) closeWithProtocolMismatch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		msg := websocket.FormatCloseMessage(websocket.CloseProtocolError, protocol.CloseReasonProtocolMismatch)
		_ = c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
	}
	c.close()
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

const writeWait = 10 * time.Second

func (c *Client) writePump() {
	defer c.hub.pumpWG.Done()
	defer c.conn.Close()
	for msg := range c.send {
		c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		if err := c.conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			slog.Warn("client write", "client", c.label, "error", err)
			return
		}
	}
	// Drain: emit a close frame for the backpressure-eviction path
	// (SendMsg's default branch closes c.send but leaves c.conn open).
	// Normal shutdown still goes through CloseWithReason.
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	c.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func (c *Client) readPump() {
	defer c.hub.pumpWG.Done()
	defer func() {
		select {
		case c.hub.unregister <- c:
		case <-c.hub.done:
		}
		c.conn.Close()
	}()

	// Read deadline = 2 × PingInterval — one full interval for the ping to
	// travel, another for the pong to arrive before we give up.
	pingTimeout := time.Duration(c.hub.cfg.Sync.PingInterval*2) * time.Second
	c.conn.SetReadDeadline(time.Now().Add(pingTimeout))

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Error("client read", "client", c.label, "error", err)
			}
			return
		}
		c.hub.handleMessage(c, data)
		c.conn.SetReadDeadline(time.Now().Add(pingTimeout))
	}
}
