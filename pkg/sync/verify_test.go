package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/pkg/stage"
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func newVerifyFixture(t *testing.T) (*stage.BaseStore, *stage.Manifest, *MemFileIO, *Filter, string) {
	t.Helper()
	dir := t.TempDir()
	bsDir := filepath.Join(dir, "base")
	if err := os.MkdirAll(bsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bs := stage.NewBaseStore(bsDir)
	m, err := stage.OpenManifest(filepath.Join(dir, "manifest.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	disk := NewMemFileIO()
	flt, err := NewFilter(strings.NewReader(""), FilterOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return bs, m, disk, flt, bsDir
}

func TestVerify_CleanMatch(t *testing.T) {
	bs, m, disk, flt, _ := newVerifyFixture(t)
	data := []byte("a")
	if err := bs.Write("a.md", data); err != nil {
		t.Fatal(err)
	}
	if err := m.Put("a.md", stage.ManifestEntry{Path: "a.md", Hash: protocol.HashBytes(data)}); err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected clean match")
	}
}

// A single base/ file rotted while the live working tree and manifest still
// agree: verify must repair base/ from disk in place and report no
// re-bootstrap (ok=true). The repaired base bytes must equal the manifest hash.
func TestVerify_RepairsFromDiskWhenLiveMatches(t *testing.T) {
	bs, m, disk, flt, _ := newVerifyFixture(t)
	good := []byte("the-true-base-content")
	goodHash := protocol.HashBytes(good)
	// base/ holds rotted bytes; live + manifest agree on the true content.
	if err := bs.Write("a.md", []byte("ROTTED")); err != nil {
		t.Fatal(err)
	}
	if err := disk.WriteFile("a.md", good); err != nil {
		t.Fatal(err)
	}
	if err := m.Put("a.md", stage.ManifestEntry{Path: "a.md", Hash: goodHash}); err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("single corrupt file with intact live+manifest must repair, not re-bootstrap")
	}
	repaired, err := bs.Read("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if protocol.HashBytes(repaired) != goodHash {
		t.Error("base/ was not repaired to the manifest hash")
	}
}

// base/ file missing entirely (not just byte-rotted) but live+manifest agree:
// same repair path.
func TestVerify_RepairsMissingFromBaseWhenLiveMatches(t *testing.T) {
	bs, m, disk, flt, _ := newVerifyFixture(t)
	good := []byte("content")
	goodHash := protocol.HashBytes(good)
	if err := disk.WriteFile("a.md", good); err != nil {
		t.Fatal(err)
	}
	if err := m.Put("a.md", stage.ManifestEntry{Path: "a.md", Hash: goodHash}); err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing base/ with intact live+manifest must repair, not re-bootstrap")
	}
	repaired, err := bs.Read("a.md")
	if err != nil {
		t.Fatal(err)
	}
	if protocol.HashBytes(repaired) != goodHash {
		t.Error("base/ was not repaired to the manifest hash")
	}
}

// Residual case 1: base/ corrupt AND the live file diverged from the manifest
// (a pending offline edit). The true base bytes are lost locally → re-bootstrap.
func TestVerify_ResidualLiveDiverged(t *testing.T) {
	bs, m, disk, flt, _ := newVerifyFixture(t)
	if err := bs.Write("a.md", []byte("ROTTED")); err != nil {
		t.Fatal(err)
	}
	if err := disk.WriteFile("a.md", []byte("a-pending-edit")); err != nil {
		t.Fatal(err)
	}
	// Manifest records the base hash, which neither base/ nor live now match.
	if err := m.Put("a.md", stage.ManifestEntry{Path: "a.md", Hash: protocol.HashBytes([]byte("the-base"))}); err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("base corrupt + live diverged must report re-bootstrap (false)")
	}
}

// Residual case 2: base/ missing AND the live file is gone too.
func TestVerify_ResidualLiveMissing(t *testing.T) {
	bs, m, disk, flt, _ := newVerifyFixture(t)
	// Neither base/ nor live has the path; manifest still references it.
	if err := m.Put("a.md", stage.ManifestEntry{Path: "a.md", Hash: protocol.HashBytes([]byte("a"))}); err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("base missing + live missing must report re-bootstrap (false)")
	}
}

// A repairable path and a residual path in the same manifest: any residual
// path forces re-bootstrap regardless of how many others repaired cleanly.
func TestVerify_MixedResidualForcesReBootstrap(t *testing.T) {
	bs, m, disk, flt, _ := newVerifyFixture(t)
	// Repairable path.
	good := []byte("good")
	if err := bs.Write("ok.md", []byte("rot")); err != nil {
		t.Fatal(err)
	}
	if err := disk.WriteFile("ok.md", good); err != nil {
		t.Fatal(err)
	}
	if err := m.Put("ok.md", stage.ManifestEntry{Path: "ok.md", Hash: protocol.HashBytes(good)}); err != nil {
		t.Fatal(err)
	}
	// Residual path.
	if err := bs.Write("bad.md", []byte("rot")); err != nil {
		t.Fatal(err)
	}
	if err := disk.WriteFile("bad.md", []byte("diverged")); err != nil {
		t.Fatal(err)
	}
	if err := m.Put("bad.md", stage.ManifestEntry{Path: "bad.md", Hash: protocol.HashBytes([]byte("base"))}); err != nil {
		t.Fatal(err)
	}

	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("a residual path anywhere in the manifest must force re-bootstrap")
	}
}

func TestVerify_FilterExcludedSkipped(t *testing.T) {
	bs, m, disk, _, _ := newVerifyFixture(t)
	// Non-admin filter: .leyline/* is excluded.
	flt, err := NewFilter(strings.NewReader(""), FilterOpts{AllowControlPlane: false})
	if err != nil {
		t.Fatal(err)
	}
	// Manifest has a stale .leyline/ entry that doesn't exist in base/ or
	// live. Verify must skip it because filter.Excluded == true.
	if err := m.Put(".leyline/vaultconfig/web.yaml", stage.ManifestEntry{Path: ".leyline/vaultconfig/web.yaml", Hash: protocol.HashBytes([]byte("y"))}); err != nil {
		t.Fatal(err)
	}
	// Add a clean in-scope entry.
	data := []byte("a")
	if err := bs.Write("a.md", data); err != nil {
		t.Fatal(err)
	}
	if err := m.Put("a.md", stage.ManifestEntry{Path: "a.md", Hash: protocol.HashBytes(data)}); err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyBaseSnapshot(bs, m, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("excluded path's drift must not fail verification")
	}
}

func TestVerify_NilManifestIsClean(t *testing.T) {
	bs, _, disk, flt, _ := newVerifyFixture(t)
	ok, err := VerifyBaseSnapshot(bs, nil, disk, flt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("nil manifest is trivially clean")
	}
}
