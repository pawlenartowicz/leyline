package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// TestWritePump_NormalSend covers the happy path: SendMsg on a peer that
// is actively reading produces no deadline-related error and the payload
// arrives intact.
func TestWritePump_NormalSend(t *testing.T) {
	h, server, key := testHarness(t)
	client, conn := silentPeer(t, h, server, key, "Alice")

	if err := client.SendMsg(protocol.PongMsg{Type: protocol.MsgPong}); err != nil {
		t.Fatalf("SendMsg: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	mt, _, err := protocol.ParseServerMessage(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mt != protocol.MsgPong {
		t.Fatalf("got type %d, want %d", mt, protocol.MsgPong)
	}
}

// TestWritePump_ClosedSendEmitsCloseFrame covers the backpressure-eviction
// drain path: when c.send is closed but the conn is still open, writePump
// drains, emits a CloseNormalClosure frame, then exits.
func TestWritePump_ClosedSendEmitsCloseFrame(t *testing.T) {
	h, server, key := testHarness(t)
	client, conn := silentPeer(t, h, server, key, "Alice")

	client.mu.Lock()
	client.close()
	client.mu.Unlock()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected close frame, got nil error")
	}
	if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		t.Fatalf("expected CloseNormalClosure, got %v", err)
	}
}

// TestWritePump_StuckPeer_DeadlineExits covers the write-deadline guard:
// a peer that stops draining causes writePump to exit with an i/o timeout
// inside writeWait + slack rather than hanging forever.
func TestWritePump_StuckPeer_DeadlineExits(t *testing.T) {
	if testing.Short() {
		t.Skip("uses writeWait deadline (~10s); skipped in -short mode")
	}
	h, server, key := testHarness(t)
	client, _ := silentPeer(t, h, server, key, "Alice")

	big := strings.Repeat("x", 8*1024*1024)
	// Use a frame the receiver would otherwise accept; the content is
	// arbitrary — we only need a payload large enough to back up the kernel
	// write buffer.
	if err := client.SendMsg(protocol.BroadcastMsg{
		Type: protocol.MsgBroadcast,
		Ops:  []protocol.Op{{Type: protocol.OpWrite, Path: "p", Data: []byte(big)}},
	}); err != nil {
		t.Fatalf("SendMsg: %v", err)
	}

	deadline := time.Now().Add(writeWait + 5*time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		closed := client.closed
		client.mu.Unlock()
		if closed {
			return
		}
		// sync-primitive-justified: polling client.closed which is set by the write-pump goroutine after a send failure; the closed flag has no channel export and the write-pump owns the connection lifecycle.
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("client not closed within %v", writeWait+5*time.Second)
}
