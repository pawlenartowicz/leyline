package main

import (
	"bytes"
	"strings"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestLoadConfig_HappyPath(t *testing.T) {
	t.Setenv("LEYLINE_SMOKE_URL", "ws://example.test:21348/_leyline/sync/test")
	t.Setenv("LEYLINE_SMOKE_API_KEY", "ley_abcdefghij1234567890")
	t.Setenv("LEYLINE_SMOKE_URL_V6", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.URL != "ws://example.test:21348/_leyline/sync/test" {
		t.Errorf("URL = %q", cfg.URL)
	}
	if cfg.APIKey != "ley_abcdefghij1234567890" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
}

func TestLoadConfig_MissingURL(t *testing.T) {
	t.Setenv("LEYLINE_SMOKE_URL", "")
	t.Setenv("LEYLINE_SMOKE_API_KEY", "ley_abcdefghij1234567890")
	t.Setenv("LEYLINE_SMOKE_URL_V6", "")

	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "LEYLINE_SMOKE_URL") {
		t.Fatalf("expected LEYLINE_SMOKE_URL error, got %v", err)
	}
}

func TestLoadConfig_MissingKey(t *testing.T) {
	t.Setenv("LEYLINE_SMOKE_URL", "ws://example.test:21348/_leyline/sync/test")
	t.Setenv("LEYLINE_SMOKE_API_KEY", "")
	t.Setenv("LEYLINE_SMOKE_URL_V6", "")

	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "LEYLINE_SMOKE_API_KEY") {
		t.Fatalf("expected LEYLINE_SMOKE_API_KEY error, got %v", err)
	}
}

func TestLoadConfig_KeyShape(t *testing.T) {
	t.Setenv("LEYLINE_SMOKE_URL", "ws://example.test:21348/_leyline/sync/test")
	t.Setenv("LEYLINE_SMOKE_API_KEY", "not-a-leyline-key")
	t.Setenv("LEYLINE_SMOKE_URL_V6", "")

	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "ley_") {
		t.Fatalf("expected key shape error, got %v", err)
	}
}

func TestLoadConfig_V6Optional(t *testing.T) {
	t.Setenv("LEYLINE_SMOKE_URL", "ws://example.test:21348/_leyline/sync/test")
	t.Setenv("LEYLINE_SMOKE_API_KEY", "ley_abcdefghij1234567890")
	t.Setenv("LEYLINE_SMOKE_URL_V6", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.URLV6 != "" {
		t.Errorf("URLV6 should be empty, got %q", cfg.URLV6)
	}
}

func TestBuildAuth(t *testing.T) {
	got := buildAuth("ley_abc", "client-uuid-1")
	_, msg, err := protocol.ParseClientMessage(got)
	if err != nil {
		t.Fatal(err)
	}
	auth := msg.(*protocol.AuthMsg)
	if auth.Type != protocol.MsgAuth {
		t.Errorf("type = %d, want %d", auth.Type, protocol.MsgAuth)
	}
	if auth.Key != "ley_abc" {
		t.Errorf("key = %q", auth.Key)
	}
	if auth.ClientID != "client-uuid-1" {
		t.Errorf("client_id = %q", auth.ClientID)
	}
	// Lock to numeric semver — server's compareVersions silently treats
	// non-numeric segments as 0 (see internal/hub/hub.go), so any
	// label-prefixed version would coincidentally pass today but break the
	// moment MinPluginVersion bumps past 0.x.
	if auth.PluginVersion != "0.1.0" {
		t.Errorf("plugin_version = %q, want 0.1.0", auth.PluginVersion)
	}
}

func TestBuildHello_RoundTrips(t *testing.T) {
	frame := buildHello(nil, nil)
	_, msg, err := protocol.ParseClientMessage(frame)
	if err != nil {
		t.Fatalf("ParseClientMessage: %v", err)
	}
	h, ok := msg.(*protocol.HelloMsg)
	if !ok {
		t.Fatalf("got %T, want *protocol.HelloMsg", msg)
	}
	if h.Type != protocol.MsgHello {
		t.Errorf("type = %d, want %d", h.Type, protocol.MsgHello)
	}
	if h.Base != nil {
		t.Fatalf("Base = %v, want nil", h.Base)
	}
	if h.ManifestDigest != nil {
		t.Fatalf("ManifestDigest = %v, want nil", h.ManifestDigest)
	}
}

func TestBuildHello_WithBase(t *testing.T) {
	base := protocol.HashBytes([]byte("base"))
	digest := protocol.HashBytes([]byte("digest"))
	frame := buildHello(&base, &digest)
	_, msg, err := protocol.ParseClientMessage(frame)
	if err != nil {
		t.Fatalf("ParseClientMessage: %v", err)
	}
	h := msg.(*protocol.HelloMsg)
	if h.Base == nil || *h.Base != base {
		t.Fatalf("Base = %v, want %v", h.Base, base)
	}
	if h.ManifestDigest == nil || *h.ManifestDigest != digest {
		t.Fatalf("ManifestDigest = %v, want %v", h.ManifestDigest, digest)
	}
}

func TestBuildPushBatch_OneWriteOp(t *testing.T) {
	base := protocol.HashBytes([]byte("base-marker"))
	op := protocol.Op{
		Seq: 1, Type: protocol.OpWrite, Path: "notes/x.md",
		Data: []byte("hello"), PreHash: nil, TS: 1715782991000,
	}
	frame := buildPushBatch(42, base, []protocol.Op{op})
	_, msg, err := protocol.ParseClientMessage(frame)
	if err != nil {
		t.Fatalf("ParseClientMessage: %v", err)
	}
	pb, ok := msg.(*protocol.PushBatchMsg)
	if !ok {
		t.Fatalf("got %T, want *protocol.PushBatchMsg", msg)
	}
	if pb.BatchID != 42 {
		t.Fatalf("BatchID = %d", pb.BatchID)
	}
	if pb.Base != base {
		t.Fatalf("Base = %x", pb.Base)
	}
	if len(pb.Ops) != 1 || !bytes.Equal(pb.Ops[0].Data, []byte("hello")) {
		t.Fatalf("Ops = %+v", pb.Ops)
	}
}

func TestBuildFlush(t *testing.T) {
	frame := buildFlush(7)
	_, msg, err := protocol.ParseClientMessage(frame)
	if err != nil {
		t.Fatalf("ParseClientMessage: %v", err)
	}
	f, ok := msg.(*protocol.FlushMsg)
	if !ok {
		t.Fatalf("got %T, want *protocol.FlushMsg", msg)
	}
	if f.FlushID != 7 {
		t.Errorf("FlushID = %d, want 7", f.FlushID)
	}
}

func TestBuildersEncodeDeterministically(t *testing.T) {
	frames := map[string][]byte{
		"hello":     buildHello(nil, nil),
		"pushBatch": buildPushBatch(1, protocol.Hash{}, nil),
		"flush":     buildFlush(7),
	}
	for name, frame := range frames {
		if _, _, err := protocol.ParseClientMessage(frame); err != nil {
			t.Fatalf("%s does not decode: %v", name, err)
		}
	}
}

func TestOpHelpers(t *testing.T) {
	w := opWrite(1, "a.md", []byte("body"), nil)
	if w.Type != protocol.OpWrite || w.Path != "a.md" || !bytes.Equal(w.Data, []byte("body")) || w.PreHash != nil {
		t.Errorf("opWrite payload wrong: %+v", w)
	}
	if w.TS == 0 {
		t.Errorf("opWrite TS should be set")
	}
	preHash := protocol.HashBytes([]byte("body"))
	d := opDelete(2, "a.md", preHash)
	if d.Type != protocol.OpDelete || d.Path != "a.md" || d.PreHash == nil || *d.PreHash != preHash {
		t.Errorf("opDelete payload wrong: %+v", d)
	}
	r := opRename(3, "a.md", "b.md", preHash)
	if r.Type != protocol.OpRename || r.From != "a.md" || r.To != "b.md" || r.PreHash == nil || *r.PreHash != preHash {
		t.Errorf("opRename payload wrong: %+v", r)
	}
	if r.Path != "" {
		t.Errorf("opRename must not set Path, got %q", r.Path)
	}
}

func TestParseTestArg(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"all", []string{"crud", "rename", "traversal", "allowed", "multi", "reader_push_denied", "ipv6", "auth_fail"}},
		{"crud", []string{"crud"}},
		{"auth_ratelimit", []string{"auth_ratelimit"}},
		{"push_rate_limit_strict", []string{"push_rate_limit_strict"}},
	}
	for _, tc := range cases {
		got, err := parseTestArg(tc.name)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s[%d]: got %q want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestParseTestArg_Unknown(t *testing.T) {
	if _, err := parseTestArg("nonsense"); err == nil {
		t.Error("expected error for unknown subtest")
	}
}

func TestParseTestArg_OldNameDropped(t *testing.T) {
	if _, err := parseTestArg("rate_limit_strict"); err == nil {
		t.Error("expected error for old name rate_limit_strict — should be push_rate_limit_strict now")
	}
}
