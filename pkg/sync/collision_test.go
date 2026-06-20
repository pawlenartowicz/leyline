package sync

import "testing"

func TestRenameForCollision_SimpleRename(t *testing.T) {
	got := RenameForCollision("notes/idea.md", "laptop", func(string) bool { return false })
	want := "notes/idea.md"
	if got == want {
		t.Fatalf("expected rename, got original path %q", got)
	}
	if got != "notes/idea.laptop.md" {
		t.Errorf("got %q, want notes/idea.laptop.md", got)
	}
}

func TestRenameForCollision_RootDir(t *testing.T) {
	got := RenameForCollision("idea.md", "laptop", func(string) bool { return false })
	if got != "idea.laptop.md" {
		t.Errorf("got %q, want idea.laptop.md", got)
	}
}

func TestRenameForCollision_NoExtension(t *testing.T) {
	// Extension-less files: the keyname suffix lands at the end with
	// no trailing dot. (The "." is included in the candidate construction
	// only when ext is non-empty.)
	got := RenameForCollision("Makefile", "laptop", func(string) bool { return false })
	if got != "Makefile.laptop" {
		t.Errorf("got %q, want Makefile.laptop", got)
	}
}

func TestRenameForCollision_DoubleCollisionUses_1(t *testing.T) {
	existing := map[string]bool{
		"notes/idea.laptop.md": true,
	}
	got := RenameForCollision("notes/idea.md", "laptop", func(p string) bool { return existing[p] })
	if got != "notes/idea.laptop.1.md" {
		t.Errorf("got %q, want notes/idea.laptop.1.md", got)
	}
}

func TestRenameForCollision_TripleCollisionUses_2(t *testing.T) {
	existing := map[string]bool{
		"notes/idea.laptop.md":   true,
		"notes/idea.laptop.1.md": true,
	}
	got := RenameForCollision("notes/idea.md", "laptop", func(p string) bool { return existing[p] })
	if got != "notes/idea.laptop.2.md" {
		t.Errorf("got %q, want notes/idea.laptop.2.md", got)
	}
}

func TestRenameForCollision_NoExtensionDoubleCollision(t *testing.T) {
	existing := map[string]bool{
		"Makefile.laptop": true,
	}
	got := RenameForCollision("Makefile", "laptop", func(p string) bool { return existing[p] })
	if got != "Makefile.laptop.1" {
		t.Errorf("got %q, want Makefile.laptop.1", got)
	}
}

func TestRenameForCollision_NestedDir(t *testing.T) {
	got := RenameForCollision("a/b/c/file.txt", "host", func(string) bool { return false })
	if got != "a/b/c/file.host.txt" {
		t.Errorf("got %q, want a/b/c/file.host.txt", got)
	}
}

// TestRenameForCollision_SlashInKeyname verifies that a keyname containing '/'
// does not inject a subdirectory into the collision path. The slash must be
// replaced with '-' so the result stays within the original directory.
func TestRenameForCollision_SlashInKeyname(t *testing.T) {
	got := RenameForCollision("notes/idea.md", "foo/bar", func(string) bool { return false })
	// The keyname slash must be sanitized; the result must stay in "notes/".
	want := "notes/idea.foo-bar.md"
	if got != want {
		t.Errorf("got %q, want %q (slash in keyname must become '-')", got, want)
	}
}
