package meta

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMeta_MissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "no-such-file"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("missing file should yield nil config, got %+v", cfg)
	}
}

func TestMeta_RejectsUnknownTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta")
	if err := os.WriteFile(path, []byte("garbage_field: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error on unknown top-level key")
	}
}

func TestMeta_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta")
	big := bytes.Repeat([]byte("a"), MaxFileSize+1)
	if err := os.WriteFile(path, big, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected size-limit error")
	}
}
