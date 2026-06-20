//go:build stress

package hub

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pawlenartowicz/leyline/protocol"
)

// dial connects to the test server and authenticates without using the
// shared *testing.T helpers (which call t.Fatalf and aren't safe from
// goroutines).
func dialAuth(server, key string) (*websocket.Conn, error) {
	url := "ws" + strings.TrimPrefix(server, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	auth, _ := protocol.Encode(protocol.AuthMsg{
		Type: protocol.MsgAuth, Key: key, PluginVersion: "0.1.0",
	})
	if err := conn.WriteMessage(websocket.BinaryMessage, auth); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Time{})
	return conn, nil
}

// sendJSONIgnore writes msg to conn, returning the resulting error if any.
func sendJSONIgnore(conn *websocket.Conn, msg any) error {
	data, _ := protocol.Encode(msg)
	return conn.WriteMessage(websocket.BinaryMessage, data)
}

// drainOne reads one message with a deadline and discards the result.
func drainOne(conn *websocket.Conn) error {
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	return err
}

// TestStressVaultMutex hammers a single vault from N concurrent clients
// for ~30 seconds with a mix of push/pull/rename/delete operations. The
// per-vault fileMu serializes mutations; we assert the system does not
// deadlock or panic, and that the in-memory FileMeta map agrees with disk
// at the end.
func TestStressVaultMutex(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })

	h, server, key := testHarness(t)
	t.Cleanup(func() {
		// Disconnect all clients and wait until every read/writePump
		// goroutine has exited before letting TempDir cleanup run.
		// Otherwise go-git can race RemoveAll and emit "directory not
		// empty" warnings.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.WaitForDrain(ctx); err != nil {
			t.Logf("WaitForDrain: %v", err)
		}
	})

	const clients = 8
	const duration = 30 * time.Second

	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	var ops atomic.Int64
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			conn, err := dialAuth(server.URL, key)
			if err != nil {
				return
			}
			defer conn.Close()
			r := rand.New(rand.NewSource(int64(id)))
			for time.Now().Before(deadline) {
				path := fmt.Sprintf("notes/c%d/file%d.md", id, r.Intn(20))
				switch r.Intn(4) {
				case 0:
					sendJSONIgnore(conn, protocol.PushMsg{
						Type: protocol.MsgPush, Path: path,
						Data: []byte(fmt.Sprintf("from %d at %d\n", id, time.Now().UnixNano())),
					})
				case 1:
					sendJSONIgnore(conn, protocol.PullMsg{Type: protocol.MsgPull, Paths: []string{path}})
				case 2:
					sendJSONIgnore(conn, protocol.RenameMsg{
						Type: protocol.MsgRename, From: path,
						To: fmt.Sprintf("notes/c%d/renamed%d.md", id, r.Intn(20)),
					})
				case 3:
					sendJSONIgnore(conn, protocol.DeleteMsg{Type: protocol.MsgDelete, Path: path})
				}
				_ = drainOne(conn) // best-effort drain reply/broadcast
				ops.Add(1)
			}
		}(i)
	}
	wg.Wait()

	t.Logf("stress: %d ops across %d clients", ops.Load(), clients)

	// Force-disconnect stragglers and wait for handlers to release fileMu
	// before snapshotting disk and meta.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := h.WaitForDrain(drainCtx); err != nil {
		t.Fatalf("WaitForDrain after stress: %v", err)
	}
	drainCancel()

	// Snapshot meta and disk; tracked entries must agree.
	vs := h.GetVaultState("a")
	meta := vs.meta.Snapshot()
	files, err := vs.disk.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles after stress: %v", err)
	}
	for path := range meta {
		if _, ok := files[path]; !ok {
			t.Errorf("meta has %q but disk does not", path)
		}
	}
	for path := range files {
		// Hidden control-plane files (.leyline/*) and .gitignore live on
		// disk but are not tracked by the FileMetaMap.
		if isControlPlanePath(path) || path == ".gitignore" {
			continue
		}
		if _, ok := meta[path]; !ok {
			t.Errorf("disk has %q but meta does not", path)
		}
	}
}
