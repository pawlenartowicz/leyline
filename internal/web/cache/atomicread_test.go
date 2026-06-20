package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestEpoch_BumpsMonotonically(t *testing.T) {
	var e Epoch
	v0 := e.Get()
	v1 := e.Bump()
	if v1 <= v0 {
		t.Errorf("Bump did not increase: %d → %d", v0, v1)
	}
	if e.Get() != v1 {
		t.Errorf("Get after Bump = %d, want %d", e.Get(), v1)
	}
}

func TestEpoch_ConcurrentBumps(t *testing.T) {
	var e Epoch
	const goroutines = 50
	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() { e.Bump(); done <- struct{}{} }()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
	if e.Get() != goroutines {
		t.Errorf("after %d Bumps Get = %d", goroutines, e.Get())
	}
}

func TestReadAndHash_Stable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.md")
	body := []byte("hello world")
	if err := os.WriteFile(p, body, 0644); err != nil {
		t.Fatal(err)
	}
	got, hash, err := ReadAndHash(p)
	if err != nil {
		t.Fatalf("ReadAndHash: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("contents = %q", got)
	}
	want := sha256.Sum256(body)
	if hash != hex.EncodeToString(want[:]) {
		t.Errorf("hash mismatch")
	}
}

func TestReadAndHash_MissingFile(t *testing.T) {
	if _, _, err := ReadAndHash("/no/such"); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestReadAndHash_RetriesOnSizeFlip(t *testing.T) {
	statCalls := 0
	statFn := func(path string) (int64, error) {
		statCalls++
		if statCalls == 1 {
			return 100, nil
		}
		return 50, nil
	}
	readFn := func(path string) ([]byte, error) {
		return make([]byte, 50), nil
	}
	got, _, err := readAndHashCustom("/fake", statFn, readFn)
	if err != nil {
		t.Fatalf("readAndHashCustom: %v", err)
	}
	if len(got) != 50 {
		t.Errorf("len(got) = %d, want 50", len(got))
	}
	if statCalls != 2 {
		t.Errorf("expected exactly 2 stat calls (initial mismatch, then retry); got %d", statCalls)
	}
}

func TestReadAndHash_NoRetryWhenStable(t *testing.T) {
	statCalls := 0
	statFn := func(path string) (int64, error) {
		statCalls++
		return 50, nil
	}
	readFn := func(path string) ([]byte, error) {
		return make([]byte, 50), nil
	}
	if _, _, err := readAndHashCustom("/fake", statFn, readFn); err != nil {
		t.Fatalf("readAndHashCustom: %v", err)
	}
	if statCalls != 1 {
		t.Errorf("expected exactly 1 stat call (no retry); got %d", statCalls)
	}
}
