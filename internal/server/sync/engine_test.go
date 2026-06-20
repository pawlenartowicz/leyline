package sync

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

func TestResolveHello_Bootstrap_NilBase(t *testing.T) {
	// clientBase == nil → bootstrap, ops for all files at head.

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	commitFile(t, g, dir, "a.md", []byte("hello"))

	allow := allowAll(t)
	res, err := ResolveHello(g, allow, nil, nil, true)
	if err != nil {
		t.Fatalf("ResolveHello: %v", err)
	}

	if res.State != protocol.HelloStateBootstrap {
		t.Errorf("state = %q, want %q", res.State, protocol.HelloStateBootstrap)
	}
	if res.Head.IsZero() {
		t.Error("head should not be zero")
	}
	// Bootstrap is streamed by the caller (handleHello); ResolveHello no
	// longer materializes the op slice here.
	if res.Ops != nil {
		t.Errorf("expected nil ops for streaming bootstrap, got %d", len(res.Ops))
	}
}

func TestResolveHello_UpToDate(t *testing.T) {
	// clientBase == head → up_to_date, no ops.

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	head := commitFile(t, g, dir, "a.md", []byte("hello"))

	allow := allowAll(t)
	res, err := ResolveHello(g, allow, &head, nil, true)
	if err != nil {
		t.Fatalf("ResolveHello: %v", err)
	}

	if res.State != protocol.HelloStateUpToDate {
		t.Errorf("state = %q, want %q", res.State, protocol.HelloStateUpToDate)
	}
	if res.Head != head {
		t.Errorf("head mismatch")
	}
	if len(res.Ops) != 0 {
		t.Errorf("expected no ops for up_to_date, got %d", len(res.Ops))
	}
}

func TestResolveHello_Catchup(t *testing.T) {
	// clientBase is behind head but reachable → catchup, ops carry the delta.

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	base := commitFile(t, g, dir, "a.md", []byte("a"))
	commitFile(t, g, dir, "b.md", []byte("b"))

	allow := allowAll(t)
	res, err := ResolveHello(g, allow, &base, nil, true)
	if err != nil {
		t.Fatalf("ResolveHello: %v", err)
	}

	if res.State != protocol.HelloStateCatchup {
		t.Errorf("state = %q, want %q", res.State, protocol.HelloStateCatchup)
	}
	if res.Head.IsZero() {
		t.Error("head should not be zero")
	}
	if len(res.Ops) == 0 {
		t.Error("expected ops for catchup")
	}
	// The op should be a write for b.md.
	found := false
	for _, op := range res.Ops {
		if op.Path == "b.md" && op.Type == protocol.OpWrite {
			found = true
		}
	}
	if !found {
		t.Errorf("expected write(b.md) in catchup ops, got: %v", res.Ops)
	}
}

func TestResolveHello_BaseLost(t *testing.T) {
	// clientBase is not in the ancestor chain of head → base_lost, no ops.

	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	commitFile(t, g, dir, "a.md", []byte("a"))

	// A completely fabricated hash that is definitely not in history.
	var fake protocol.Hash
	for i := range fake {
		fake[i] = byte(i + 1)
	}

	allow := allowAll(t)
	res, err := ResolveHello(g, allow, &fake, nil, true)
	if err != nil {
		t.Fatalf("ResolveHello: %v", err)
	}

	if res.State != protocol.HelloStateBaseLost {
		t.Errorf("state = %q, want %q", res.State, protocol.HelloStateBaseLost)
	}
	if len(res.Ops) != 0 {
		t.Errorf("expected no ops for base_lost, got %d", len(res.Ops))
	}
}

// TestResolveHello_UpToDate_MatchingDigest verifies that when base matches
// HEAD and the client manifest digest matches the server's computed digest
// at HEAD, the result is up_to_date (unchanged).
func TestResolveHello_UpToDate_MatchingDigest(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	head := commitFile(t, g, dir, "a.md", []byte("hello"))
	allow := allowAll(t)

	// Compute the server's digest at HEAD using the same function used
	// by ResolveHello internally, then feed it back as the client's
	// digest — a faithful in-sync client.
	serverDigest, err := ServerManifestDigestAtHead(g, allow, head, true)
	if err != nil {
		t.Fatalf("ServerManifestDigestAtHead: %v", err)
	}

	res, err := ResolveHello(g, allow, &head, &serverDigest, true)
	if err != nil {
		t.Fatalf("ResolveHello: %v", err)
	}
	if res.State != protocol.HelloStateUpToDate {
		t.Errorf("state = %q, want %q (digest matched)", res.State, protocol.HelloStateUpToDate)
	}
}

// TestResolveHello_UpToDate_MismatchedDigest_ReturnsCatchupEmpty verifies
// that when base matches HEAD but the client's manifest digest disagrees
// with the server's view, the result is catchup with an empty op list so
// the client re-runs its session-start reconcile.
func TestResolveHello_UpToDate_MismatchedDigest_ReturnsCatchupEmpty(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}

	head := commitFile(t, g, dir, "a.md", []byte("hello"))
	allow := allowAll(t)

	// Hand-corrupted digest (not the one the server would compute).
	var bogus protocol.Hash
	for i := range bogus {
		bogus[i] = 0xFF
	}

	res, err := ResolveHello(g, allow, &head, &bogus, true)
	if err != nil {
		t.Fatalf("ResolveHello: %v", err)
	}
	if res.State != protocol.HelloStateCatchup {
		t.Errorf("state = %q, want %q (digest mismatch must produce catchup)", res.State, protocol.HelloStateCatchup)
	}
	if len(res.Ops) != 0 {
		t.Errorf("expected empty ops on digest-mismatch catchup, got %d: %v", len(res.Ops), res.Ops)
	}
	if res.Head != head {
		t.Errorf("head mismatch: got %x, want %x", res.Head, head)
	}
}

// TestServerManifestDigestAtHead_MatchesClientFormula verifies the
// server-side digest computation produces the same value as feeding
// the equivalent (path, hash) entries through protocol.ManifestDigest —
// confirming server and client agree on the formula at the fixture
// boundary.
func TestServerManifestDigestAtHead_MatchesClientFormula(t *testing.T) {
	dir := t.TempDir()
	g, err := storage.OpenOrInitGit(dir)
	if err != nil {
		t.Fatalf("OpenOrInitGit: %v", err)
	}
	// Commit two files at HEAD.
	commitFile(t, g, dir, "a.md", []byte("alpha"))
	head := commitFile(t, g, dir, "b.md", []byte("beta"))

	allow := allowAll(t)
	got, err := ServerManifestDigestAtHead(g, allow, head, true)
	if err != nil {
		t.Fatalf("ServerManifestDigestAtHead: %v", err)
	}

	// Client-formula equivalent: sha256 over both files at the same
	// content-hash function (protocol.HashBytes = sha256).
	want := protocol.ManifestDigest([]protocol.ManifestEntry{
		{Path: "a.md", Hash: protocol.HashBytes([]byte("alpha"))},
		{Path: "b.md", Hash: protocol.HashBytes([]byte("beta"))},
	})
	if got != want {
		t.Errorf("digest mismatch:\n got=%x\nwant=%x", got, want)
	}
}

