package sync

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/vaultaddr"
)

// dialURLFromAddress converts a canonical "host/vaultID" address to the
// WebSocket URL (wss://host/_leyline/sync/vaultID). Thin alias kept for in-package
// readability; new callers should use vaultaddr directly.
func dialURLFromAddress(s string) (string, error) {
	return vaultaddr.DialURL(s)
}

// NormalizeVaultAddress is the legacy CLI entry point — it now delegates to
// leyline-protocol/vaultaddr.Normalize. Kept as a wrapper because many CLI
// commands import this name; new code should prefer vaultaddr directly.
func NormalizeVaultAddress(s string) (string, error) {
	return vaultaddr.Normalize(s)
}

// ServerMessage is a typed delivery from the read loop.
type ServerMessage struct {
	Type    protocol.MsgType
	Payload any // a *protocol.<Foo>Msg
}

// DialOpts configures one WebSocket connection attempt.
type DialOpts struct {
	URL           string
	Key           string
	PluginVersion string
	// ClientID is the per-installation UUID sent in AuthMsg.ClientID. The
	// server keys its per-client stage and idempotency cache on this value.
	ClientID string
	// ServerProtocolMajorOK is called with AuthOKMsg.ServerVersion. If nil,
	// the client trusts the server. Returning false aborts the connection
	// after auth_ok with a version-mismatch error.
	ServerProtocolMajorOK func(serverVersion string) bool
	// Dialer overrides the websocket dialer used for the connection.
	Dialer *websocket.Dialer
}

// Client is a single-connection WebSocket client speaking the leyline v1
// CBOR sync protocol. Not safe for concurrent Sends from multiple goroutines.
type Client struct {
	conn *websocket.Conn
	recv chan ServerMessage

	closeOnce sync.Once
	closed    chan struct{}
}

// NewClient returns a fresh, unconnected Client.
func NewClient() *Client {
	return &Client{
		recv:   make(chan ServerMessage, 32),
		closed: make(chan struct{}),
	}
}

// Dial opens the WebSocket, sends auth, waits for auth_ok/auth_fail.
// On success, the read loop is running and messages flow through Recv().
func (c *Client) Dial(ctx context.Context, opts DialOpts) (*protocol.AuthOKMsg, error) {
	dialURL, err := dialURLFromAddress(opts.URL)
	if err != nil {
		return nil, err
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	conn, _, err := dialer.DialContext(ctx, dialURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", dialURL, err)
	}
	c.conn = conn

	auth := protocol.AuthMsg{
		Type:          protocol.MsgAuth,
		Key:           opts.Key,
		PluginVersion: opts.PluginVersion,
		ClientID:      opts.ClientID,
	}
	if err := writeMessage(conn, auth); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send auth: %w", err)
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		// A pre-auth close with code 1002 means the server rejected the
		// wire format. Surface that distinctly so the user gets actionable
		// guidance instead of a generic read error.
		if websocket.IsCloseError(err, websocket.CloseProtocolError) {
			return nil, fmt.Errorf("incompatible server, update client: %w", err)
		}
		return nil, fmt.Errorf("read auth response: %w", err)
	}
	mt, msg, err := protocol.ParseServerMessage(raw)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse auth response: %w", err)
	}
	switch mt {
	case protocol.MsgAuthOK:
		ok := msg.(*protocol.AuthOKMsg)
		if opts.ServerProtocolMajorOK != nil && !opts.ServerProtocolMajorOK(ok.ServerVersion) {
			conn.Close()
			return nil, fmt.Errorf("incompatible server version %s", ok.ServerVersion)
		}
		go c.readLoop()
		return ok, nil
	case protocol.MsgAuthFail:
		fail := msg.(*protocol.AuthFailMsg)
		conn.Close()
		return nil, fmt.Errorf("auth failed: %s", fail.Reason)
	default:
		conn.Close()
		return nil, fmt.Errorf("unexpected response type %d", mt)
	}
}

// Recv returns the channel of incoming server messages. Closed on disconnect.
func (c *Client) Recv() <-chan ServerMessage {
	return c.recv
}

// RecvSync blocks until the next ServerMessage arrives, the read loop
// closes, or ctx fires. Returns the message on success; an error
// wrapping ctx.Err() on cancel; io.EOF on disconnect. Used by the
// engine state machine for Hello/Catchup/PushAck/FlushAck reads where
// the next frame must be awaited synchronously.
func (c *Client) RecvSync(ctx context.Context) (ServerMessage, error) {
	select {
	case msg, ok := <-c.recv:
		if !ok {
			return ServerMessage{}, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return ServerMessage{}, ctx.Err()
	case <-c.closed:
		return ServerMessage{}, io.EOF
	}
}

// Send writes a message struct as a CBOR binary frame. Caller is responsible
// for using one goroutine per Client for writes (or wrapping calls in a mutex).
func (c *Client) Send(v any) error {
	if c.conn == nil {
		return fmt.Errorf("client not connected")
	}
	return writeMessage(c.conn, v)
}

// writeMessage CBOR-encodes v and writes it as a single binary WS frame.
func writeMessage(conn *websocket.Conn, v any) error {
	data, err := protocol.Encode(v)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, data)
}

// Close terminates the connection.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.conn != nil {
			err = c.conn.Close()
		}
	})
	return err
}

// readLoop reads raw frames from the WebSocket, parses them, and fans them
// into c.recv. Unknown message types from newer minor versions are skipped;
// any other parse error closes the loop and the channel.
func (c *Client) readLoop() {
	defer close(c.recv)
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		mtype, msg, perr := protocol.ParseServerMessage(raw)
		if perr != nil {
			// Skip unknown types; they may be from a newer minor version.
			if !strings.Contains(perr.Error(), "unknown") {
				return
			}
			continue
		}
		select {
		case c.recv <- ServerMessage{Type: mtype, Payload: msg}:
		case <-c.closed:
			return
		}
	}
}

// Encode wraps the package-level CBOR encoder for tests and helpers.
func Encode(v any) ([]byte, error) {
	return protocol.Encode(v)
}

// BackoffOpts controls reconnect behavior.
type BackoffOpts struct {
	Base   time.Duration
	Max    time.Duration
	Jitter float64
}

// RunWithReconnect dials, runs body, and retries with exponential backoff
// until ctx is cancelled or body returns a non-connection error. body
// returning nil exits immediately (one-shot callers should never return nil
// from the daemon loop; only reconnect-appropriate errors propagate).
func RunWithReconnect(ctx context.Context, dial DialOpts, b BackoffOpts, body func(*Client, *protocol.AuthOKMsg) error) error {
	if b.Base == 0 {
		b.Base = time.Second
	}
	if b.Max == 0 {
		b.Max = 60 * time.Second
	}
	delay := b.Base
	for {
		cli := NewClient()
		ok, err := cli.Dial(ctx, dial)
		if err == nil {
			delay = b.Base // reset on success
			err = body(cli, ok)
			cli.Close()
			if err != nil {
				return err
			}
		}
		sleep := delay
		if b.Jitter > 0 {
			sleep += time.Duration(rand.Float64() * b.Jitter * float64(delay))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		delay *= 2
		if delay > b.Max {
			delay = b.Max
		}
	}
}
