package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestWSAuthSuccess(t *testing.T) {
	h, _, rawKey := testHarness(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_leyline/sync/{vault}", h.ServeWS)
	server := httptest.NewServer(mux)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	authMsg := protocol.AuthMsg{
		Type:          protocol.MsgAuth,
		Key:           rawKey,
		PluginVersion: "0.1.0",
		ClientID:      "test-client-success",
	}
	data, _ := protocol.Encode(authMsg)
	conn.WriteMessage(websocket.BinaryMessage, data)

	_, resp, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	mt, msg, err := protocol.ParseServerMessage(resp)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mt != protocol.MsgAuthOK {
		t.Fatalf("expected auth_ok, got type=%d", mt)
	}
	authOK := msg.(*protocol.AuthOKMsg)
	if authOK.Name != "Alice" {
		t.Errorf("expected name Alice, got %q", authOK.Name)
	}
	if authOK.Role != "editor" {
		t.Errorf("expected role editor, got %q", authOK.Role)
	}
}

// TestWSAuthFailure was removed; auth_fail for invalid key is covered at the real-binary level by:
//   invivo/conncaps/connection_caps_test.go (any auth with bad key → auth_fail)
//   invivo/wire/wire_rejection_test.go (non-CBOR / invalid frames → close 1002)

func TestWSAuthTimeout(t *testing.T) {
	h, _, _ := testHarness(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /_leyline/sync/{vault}", h.ServeWS)
	server := httptest.NewServer(mux)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/_leyline/sync/a"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// sync-primitive-justified: waiting for the server-side auth timeout (5s) to fire and close the connection; the timeout is driven by a server goroutine timer with no client-observable channel — only ReadMessage returning an error signals closure.
	time.Sleep(6 * time.Second)
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Error("expected connection to be closed after auth timeout")
	}
}

func TestPingPong(t *testing.T) {
	_, server, key := testHarness(t)
	conn := connectClient(t, server, key)
	sendMsg(t, conn, protocol.PingMsg{Type: protocol.MsgPing})
	expectType(t, conn, protocol.MsgPong)
}
