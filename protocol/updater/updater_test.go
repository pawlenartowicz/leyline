package updater

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwapAndRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin")
	src := filepath.Join(dir, "new")
	write(t, target, "OLD")
	write(t, src, "NEW")

	backup, err := Swap(target, src)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, target) != "NEW" {
		t.Errorf("target = %q, want NEW", read(t, target))
	}
	if read(t, backup) != "OLD" {
		t.Errorf("backup = %q, want OLD", read(t, backup))
	}
	if backup != target+"~" {
		t.Errorf("backup path = %q, want %q", backup, target+"~")
	}

	if err := Rollback(target, backup); err != nil {
		t.Fatal(err)
	}
	if read(t, target) != "OLD" {
		t.Errorf("after rollback target = %q, want OLD", read(t, target))
	}
}

type fakeRestarter struct {
	err   error
	calls int
}

func (f *fakeRestarter) Restart() error { f.calls++; return f.err }

type fakeHealth struct{ err error }

func (f fakeHealth) Healthy() error { return f.err }

func TestApply_CleanPath(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	write(t, a, "OLD")
	srcA := filepath.Join(dir, "a.new")
	write(t, srcA, "NEW")

	r := &fakeRestarter{}
	err := Apply([]SwapPair{{Target: a, NewBinary: srcA}}, r, fakeHealth{})
	if err != nil {
		t.Fatal(err)
	}
	if read(t, a) != "NEW" {
		t.Errorf("a = %q, want NEW", read(t, a))
	}
	if r.calls != 1 {
		t.Errorf("restart calls = %d, want 1", r.calls)
	}
}

func TestApply_HealthFailRollsBackAndRestartsOld(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	write(t, a, "OLDA")
	write(t, b, "OLDB")
	srcA := filepath.Join(dir, "a.new")
	srcB := filepath.Join(dir, "b.new")
	write(t, srcA, "NEWA")
	write(t, srcB, "NEWB")

	r := &fakeRestarter{}
	err := Apply(
		[]SwapPair{{Target: a, NewBinary: srcA}, {Target: b, NewBinary: srcB}},
		r, fakeHealth{err: errors.New("unhealthy")},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if read(t, a) != "OLDA" || read(t, b) != "OLDB" {
		t.Errorf("both pairs should be rolled back; got a=%q b=%q", read(t, a), read(t, b))
	}
	if r.calls != 2 {
		t.Errorf("restart calls = %d, want 2 (new then old)", r.calls)
	}
}

func TestApply_NilRestarterAndHealth(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	write(t, a, "OLD")
	srcA := filepath.Join(dir, "a.new")
	write(t, srcA, "NEW")

	if err := Apply([]SwapPair{{Target: a, NewBinary: srcA}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if read(t, a) != "NEW" {
		t.Errorf("a = %q, want NEW", read(t, a))
	}
}
