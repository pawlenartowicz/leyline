package stage

import (
	"os"
	"path/filepath"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestBaseStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base.json")

	h := protocol.HashBytes([]byte("commit"))
	in := BaseState{Base: &h, LastSync: 1715800000, NextSeq: 42}
	if err := WriteBase(path, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := ReadBase(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out.Base == nil || *out.Base != h {
		t.Errorf("base mismatch: %+v", out.Base)
	}
	if out.NextSeq != 42 {
		t.Errorf("next_seq = %d", out.NextSeq)
	}
}

func TestBaseStateNilBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base.json")
	if err := WriteBase(path, BaseState{Base: nil, NextSeq: 1}); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := ReadBase(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out.Base != nil {
		t.Errorf("expected nil base, got %+v", out.Base)
	}
}

func TestBaseStateMissing(t *testing.T) {
	_, err := ReadBase(filepath.Join(t.TempDir(), "missing.json"))
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %v", err)
	}
}

func TestResetBase_ClearsAllThree(t *testing.T) {
	dir := t.TempDir()
	baseFile := filepath.Join(dir, "base.json")
	manifestFile := filepath.Join(dir, "manifest.jsonl")
	baseDir := filepath.Join(dir, "base")

	// Populate all three.
	h := protocol.HashBytes([]byte("c"))
	if err := WriteBase(baseFile, BaseState{Base: &h, NextSeq: 5}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestFile, []byte(`{"path":"a.md"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "sub", "b.md"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ResetBase(baseFile, manifestFile, baseDir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(baseFile); !os.IsNotExist(err) {
		t.Errorf("base.json still present: %v", err)
	}
	if _, err := os.Stat(manifestFile); !os.IsNotExist(err) {
		t.Errorf("manifest still present: %v", err)
	}
	if _, err := os.Stat(baseDir); !os.IsNotExist(err) {
		t.Errorf("base/ still present: %v", err)
	}
}

func TestResetBase_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// Call on a fully-missing layout — must not error.
	if err := ResetBase(
		filepath.Join(dir, "base.json"),
		filepath.Join(dir, "manifest.jsonl"),
		filepath.Join(dir, "base"),
	); err != nil {
		t.Errorf("ResetBase on missing files: %v", err)
	}
}

func TestBaseStateAtomicWrite(t *testing.T) {
	// Writing must use tmp + rename so a crash mid-write doesn't truncate.
	// We verify by inspecting the directory after write: only base.json
	// (no leftover .tmp).
	dir := t.TempDir()
	path := filepath.Join(dir, "base.json")
	if err := WriteBase(path, BaseState{NextSeq: 1}); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "base.json" {
			t.Errorf("leftover file %q after write", e.Name())
		}
	}
}
