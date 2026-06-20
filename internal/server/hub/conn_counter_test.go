package hub

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/config"
)

// TestPerKeyConnCounter_RegisterUnregister checks that the per-IP counter
// in h.connCount is incremented on connect and decremented on disconnect.
// This is a unit-level check driven via the WS path (the counter lives in
// ServeWS / hub.Run and has no separate injectable interface).
func TestPerKeyConnCounter_RegisterUnregister(t *testing.T) {
	h, server, key := testHarness(t)

	ip := "127.0.0.1" // httptest server uses loopback

	// Before connect: counter either absent or 0.
	before := int64(0)
	if v, ok := h.connCount.Load(ip); ok {
		before = v.(*atomic.Int64).Load()
	}

	conn := connectClient(t, server, key)
	// Drive a ping to confirm we're fully registered.
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)

	// Counter must have risen by at least 1.
	afterConnect := int64(0)
	if v, ok := h.connCount.Load(ip); ok {
		afterConnect = v.(*atomic.Int64).Load()
	}
	if afterConnect <= before {
		t.Errorf("counter after connect = %d, want > %d", afterConnect, before)
	}

	conn.Close()
	// Wait for unregister to run on the hub goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cur := int64(0)
		if v, ok := h.connCount.Load(ip); ok {
			cur = v.(*atomic.Int64).Load()
		}
		if cur < afterConnect {
			return // success: counter decremented
		}
		// sync-primitive-justified: polling hub.connCount after Close to observe the unregister path; unregister runs on the hub goroutine with no done-channel exposed to callers.
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("counter did not decrement after disconnect")
}

// TestPerKeyConnCounter_RaceConcurrentConnects verifies that concurrent
// connect/disconnect cycles do not cause counter drift under -race.
func TestPerKeyConnCounter_RaceConcurrentConnects(t *testing.T) {
	h, server, key := testHarness(t)

	const N = 5
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
			conn, _, err := websocket.DefaultDialer.Dial(url, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			sendMsg(t, conn, protocol.AuthMsg{
				Type: protocol.MsgAuth, Key: key,
				PluginVersion: "0.1.0", ClientID: "race-client",
			})
			// Discard auth response.
			conn.SetReadDeadline(time.Now().Add(time.Second))
			conn.ReadMessage()
		}(i)
	}
	wg.Wait()

	// After all goroutines exit and connections close, the counter must
	// eventually settle to zero (or the baseline). Give the hub goroutine
	// time to process unregistrations.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.ConnectedClientCount() == 0 {
			return
		}
		// sync-primitive-justified: polling ConnectedClientCount to let hub goroutine drain unregister events; no done-channel available.
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("connected client count did not reach 0: got %d", h.ConnectedClientCount())
}

// TestMaxConnectionsPerKey_Enforced verifies that a second connection from
// the same key is rejected once MaxConnectionsPerKey=1.
func TestMaxConnectionsPerKey_Enforced(t *testing.T) {
	_, server, key := testHarnessWithConfig(t, func(c *config.Config) {
		c.Sync.MaxConnectionsPerKey = 1
	})

	// First connection: succeeds.
	conn1 := connectClient(t, server, key)
	// Confirm it's up.
	sendMsg(t, conn1, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn1, protocol.MsgPong)

	// Second connection: same key, same vault → must get auth_fail session_limit_exceeded.
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn2, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial second conn: %v", err)
	}
	defer conn2.Close()

	sendMsg(t, conn2, protocol.AuthMsg{
		Type: protocol.MsgAuth, Key: key,
		PluginVersion: "0.1.0", ClientID: "second-client",
	})
	conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn2.ReadMessage()
	conn2.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("reading auth response on second conn: %v", err)
	}

	mt, msg, err := protocol.ParseServerMessage(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if mt != protocol.MsgAuthFail {
		t.Fatalf("expected auth_fail, got type %d", mt)
	}
	fail := msg.(*protocol.AuthFailMsg)
	if fail.Reason != "session_limit_exceeded" {
		t.Errorf("reason = %q, want session_limit_exceeded", fail.Reason)
	}
}
