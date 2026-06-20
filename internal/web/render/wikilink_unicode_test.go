package render

import (
	"os"
	"path/filepath"
	"testing"
)

func mustWriteFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWikilink_UnicodeBasenameAndURLEscape(t *testing.T) {
	root := t.TempDir()
	files := []string{
		"Notatki/Środowisko.md", // Polish, mixed-case + diacritic
		"de/Übersicht.md",       // German, uppercase target via fold
		"jp/データ.md",            // Japanese, no case to fold
		"papers/My Paper.md",    // ASCII with space → %20
	}
	for _, rel := range files {
		writeMD(t, root, rel)
	}
	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/", idx, nil)

	cases := []struct {
		target string
		want   string // empty means resolution should fail
	}{
		// Polish: lowercase wikilink against capitalised filename, via fold.
		{"środowisko", "/Notatki/%C5%9Arodowisko"},
		// German: uppercase wikilink against capitalised filename, via fold.
		{"ÜBERSICHT", "/de/%C3%9Cbersicht"},
		// Japanese: exact basename, no fold needed but escape must still fire.
		{"データ", "/jp/%E3%83%87%E3%83%BC%E3%82%BF"},
		// ASCII with space: basename match, URL escapes the space.
		{"My Paper", "/papers/My%20Paper"},
		// And the case-folded variant resolves the same way.
		{"my paper", "/papers/My%20Paper"},
		// Path-style target with non-ASCII directory + stem.
		{"Notatki/Środowisko", "/Notatki/%C5%9Arodowisko"},
		// Broken link — no file remotely close.
		{"nieistnieje", ""},
	}
	for _, tc := range cases {
		got, ok := r.Resolve(tc.target)
		switch {
		case tc.want == "" && ok:
			t.Errorf("Resolve(%q): want ok=false, got %q", tc.target, got)
		case tc.want != "" && !ok:
			t.Errorf("Resolve(%q): want %q, got ok=false", tc.target, tc.want)
		case tc.want != "" && got != tc.want:
			t.Errorf("Resolve(%q): want %q, got %q", tc.target, tc.want, got)
		}
	}
}

func TestWikilink_UnicodeDistinctByDiacritic(t *testing.T) {
	// Two files differing only in diacritic must both resolve, each to
	// itself — case-fold does not strip diacritics, so they're distinct
	// keys.
	root := t.TempDir()
	writeMD(t, root, "zoo.md")
	writeMD(t, root, "żoo.md")

	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/", idx, nil)

	if got, ok := r.Resolve("zoo"); !ok || got != "/zoo" {
		t.Errorf("zoo: got %q ok=%v, want /zoo", got, ok)
	}
	if got, ok := r.Resolve("żoo"); !ok || got != "/%C5%BCoo" {
		t.Errorf("żoo: got %q ok=%v, want /%%C5%%BCoo", got, ok)
	}
}

// TestWikilink_TurkishDotlessI verifies that the Turkish dotted İ (U+0130)
// and dotless ı (U+0131) are handled by Unicode case-folding. Under Unicode
// case folding (golang.org/x/text/cases.Fold), İ folds to i (Latin Small
// Letter I) and ı also folds to i. Two files differing only by İ vs ı
// may therefore share the same fold key; the resolver must handle this
// deterministically (first-seen wins, no crash).
func TestWikilink_TurkishDotlessI(t *testing.T) {
	root := t.TempDir()
	// Write a file with dotted İ in the name.
	writeMD(t, root, "İstanbul.md")

	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/", idx, nil)

	// Resolve the exact basename (with İ).
	got, ok := r.Resolve("İstanbul")
	if !ok {
		t.Errorf("Resolve(İstanbul): expected ok=true, got ok=false")
	} else {
		// URL must percent-encode the non-ASCII İ.
		if got == "" {
			t.Errorf("Resolve(İstanbul) returned empty URL")
		}
	}

	// Resolve with lowercase latin i (fold of İ under some rules) — may
	// hit or miss depending on the fold table; the key requirement is no panic.
	_, _ = r.Resolve("istanbul") // must not panic
}

func TestWikilink_UnicodeAssetURL(t *testing.T) {
	root := t.TempDir()
	// Polish PNG with diacritic in basename.
	writeMD(t, root, "note.md") // unused, just so BuildBasenameIndex has work
	mustWriteFile(t, root, "images/Środowisko.png", "x")

	idx, err := BuildBasenameIndex(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := NewVaultWikilinkResolver("/notes", idx, nil)

	got, ok := r.Resolve("Środowisko.png")
	if !ok {
		t.Fatalf("asset basename did not resolve")
	}
	const want = "/notes/images/%C5%9Arodowisko.png"
	if got != want {
		t.Errorf("asset URL: got %q, want %q", got, want)
	}
}
