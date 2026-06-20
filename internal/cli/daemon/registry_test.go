package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestRegister_AddsAndDedups(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	v1 := t.TempDir()
	v2 := t.TempDir()

	for _, p := range []string{v1, v2, v1} {
		if err := Register(p); err != nil {
			t.Fatalf("Register(%s): %v", p, err)
		}
	}
	got, err := ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	abs1, _ := filepath.Abs(v1)
	abs2, _ := filepath.Abs(v2)
	want := dedupSorted([]string{abs1, abs2})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRegister_ConcurrentSafe(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const N = 16
	roots := make([]string, N)
	for i := range roots {
		roots[i] = t.TempDir()
	}
	var wg sync.WaitGroup
	for _, r := range roots {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			if err := Register(r); err != nil {
				t.Errorf("Register: %v", err)
			}
		}(r)
	}
	wg.Wait()
	got, err := ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != N {
		t.Errorf("got %d entries, want %d (%v)", len(got), N, got)
	}
}

func TestReadRegistry_MissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got, err := ReadRegistry()
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestPruneRegistry_RewritesContents(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	for _, p := range []string{a, b, c} {
		if err := Register(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneRegistry([]string{a, c}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	absA, _ := filepath.Abs(a)
	absC, _ := filepath.Abs(c)
	want := dedupSorted([]string{absA, absC})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRegistryPath_UsesXDGConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	got, err := RegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "leyline", "vaults")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	// Ensure parent dir wasn't pre-created by RegistryPath itself.
	if _, err := os.Stat(filepath.Dir(got)); !os.IsNotExist(err) {
		t.Errorf("dir should not exist yet: %v", err)
	}
}
