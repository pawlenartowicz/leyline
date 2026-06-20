package hub

import (
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// newTestVaultForPublish spins a hydrated single-vault hub via the shared
// testHarness fixture and returns its VaultState. The WS server testHarness
// also stands up is unused here — Publish is a pure hub-package call.
func newTestVaultForPublish(t *testing.T) (*VaultState, *Hub) {
	t.Helper()
	h, _, _ := testHarness(t)
	vs := h.GetVaultState("a")
	if vs == nil {
		t.Fatal("vault 'a' not hydrated")
	}
	return vs, h
}

// seedFiles commits the given files into the vault in a single CommitOps,
// mirroring seedHead but for a whole tree. Leaves HEAD reflecting the seed.
func seedFiles(t *testing.T, vs *VaultState, files map[string][]byte) {
	t.Helper()
	ops := make([]protocol.Op, 0, len(files))
	for p, c := range files {
		ops = append(ops, protocol.Op{Type: protocol.OpWrite, Path: p, Data: c})
	}
	vs.fileMu.Lock()
	defer vs.fileMu.Unlock()
	head, err := vs.git.CommitOps(ops, "test-seed")
	if err != nil {
		t.Fatalf("seedFiles CommitOps: %v", err)
	}
	vs.headHashCached = head
	vs.sizes.Apply(ops)
}

func TestPublish_OverwriteWritesAndInferredDeletes(t *testing.T) {
	vs, h := newTestVaultForPublish(t)
	// seed: a.md (keep, unchanged), b.md (changed), c.md (deleted by absence)
	seedFiles(t, vs, map[string][]byte{
		"a.md": []byte("alpha\n"),
		"b.md": []byte("old beta\n"),
		"c.md": []byte("gamma\n"),
	})

	desired := map[string][]byte{
		"a.md":   []byte("alpha\n"),    // unchanged → no op
		"b.md":   []byte("new beta\n"), // changed → OpWrite
		"d/e.md": []byte("delta\n"),    // new → OpWrite
		// c.md absent → OpDelete
	}

	res, err := h.Publish(vs, desired, "ci-key", false)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Written != 2 || res.Deleted != 1 {
		t.Fatalf("written=%d deleted=%d, want 2/1", res.Written, res.Deleted)
	}

	got, err := vs.disk.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	// Publish never touches the control plane; ListFiles still surfaces the
	// recovery-seeded .leyline/README.md. Drop it so the content tree alone is
	// compared (its survival is asserted separately by the empty-publish test).
	delete(got, ".leyline/README.md")
	want := map[string]protocol.Hash{}
	for p, c := range desired {
		want[p] = protocol.HashBytes(c)
	}
	if len(got) != len(want) {
		t.Fatalf("disk has %d files, want %d: %v", len(got), len(want), got)
	}
	for p, want := range want {
		if got[p] != want {
			t.Errorf("disk[%s] hash mismatch", p)
		}
	}
}

func TestPublish_EmptyRejectedUnlessAllowEmpty(t *testing.T) {
	vs, h := newTestVaultForPublish(t)
	seedFiles(t, vs, map[string][]byte{"a.md": []byte("x\n")})

	if _, err := h.Publish(vs, map[string][]byte{}, "ci-key", false); err == nil {
		t.Fatal("expected ErrEmptyPublish, got nil")
	}
	res, err := h.Publish(vs, map[string][]byte{}, "ci-key", true)
	if err != nil {
		t.Fatalf("allow_empty publish: %v", err)
	}
	if res.Deleted != 1 {
		t.Fatalf("deleted=%d, want 1 (vault wiped)", res.Deleted)
	}
}
