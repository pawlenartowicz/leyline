package urlx

import (
	"os"
	"path/filepath"
	"testing"
)

func makeVault(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestResolvePretty_ServesNoteWithoutExtension(t *testing.T) {
	root := makeVault(t, map[string]string{"note.md": "x"})
	d, err := ResolvePretty(root, "note")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionServe {
		t.Errorf("Action = %v, want Serve", d.Action)
	}
	if d.RelPath != "note.md" {
		t.Errorf("RelPath = %q, want note.md", d.RelPath)
	}
}

func TestResolvePretty_RedirectsExtensionToCanonical(t *testing.T) {
	root := makeVault(t, map[string]string{"note.md": "x"})
	d, err := ResolvePretty(root, "note.md")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionRedirect {
		t.Errorf("Action = %v, want Redirect", d.Action)
	}
	if d.Redirect != "note" {
		t.Errorf("Redirect = %q, want note", d.Redirect)
	}
}

func TestResolvePretty_DirectoryIndex(t *testing.T) {
	root := makeVault(t, map[string]string{"dir/index.md": "x"})
	d, err := ResolvePretty(root, "dir/")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.RelPath != "dir/index.md" {
		t.Errorf("RelPath = %q, want dir/index.md", d.RelPath)
	}
}

func TestResolvePretty_DirectoryReadmeFallback(t *testing.T) {
	root := makeVault(t, map[string]string{"dir/README.md": "x"})
	d, err := ResolvePretty(root, "dir/")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.RelPath != "dir/README.md" {
		t.Errorf("RelPath = %q, want dir/README.md", d.RelPath)
	}
}

func TestResolvePretty_DirectoryNoIndex404(t *testing.T) {
	root := makeVault(t, map[string]string{"dir/x.md": "x"})
	d, err := ResolvePretty(root, "dir/")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionNotFound {
		t.Errorf("Action = %v, want NotFound (no auto listing)", d.Action)
	}
}

func TestResolvePretty_TrailingSlashRedirectForDirectory(t *testing.T) {
	root := makeVault(t, map[string]string{"dir/index.md": "x"})
	d, err := ResolvePretty(root, "dir")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionRedirect {
		t.Errorf("Action = %v, want Redirect", d.Action)
	}
	if d.Redirect != "dir/" {
		t.Errorf("Redirect = %q, want dir/", d.Redirect)
	}
}

func TestResolvePretty_AssetKeepsExtension(t *testing.T) {
	root := makeVault(t, map[string]string{"diagram.png": "x"})
	d, err := ResolvePretty(root, "diagram.png")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionServe || d.RelPath != "diagram.png" {
		t.Errorf("got %+v, want serve diagram.png", d)
	}
}

func TestResolvePretty_RootDirectory(t *testing.T) {
	root := makeVault(t, map[string]string{"index.md": "x"})
	d, err := ResolvePretty(root, "")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionServe || d.RelPath != "index.md" {
		t.Errorf("got %+v, want serve index.md", d)
	}
}

func TestResolvePretty_NotFound(t *testing.T) {
	root := makeVault(t, map[string]string{})
	d, err := ResolvePretty(root, "no/such")
	if err != nil {
		t.Fatalf("ResolvePretty: %v", err)
	}
	if d.Action != ActionNotFound {
		t.Errorf("Action = %v, want NotFound", d.Action)
	}
}
