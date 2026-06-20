package render

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runParseCustomNav(t *testing.T, body string, vaultPrefix string, resolver WikilinkResolver, idMap map[string]string) ([]*NavNode, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nav")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write nav: %v", err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nodes, err := ParseCustomNavFile(path, vaultPrefix, resolver, idMap, logger)
	if err != nil {
		t.Fatalf("ParseCustomNavFile: %v", err)
	}
	return nodes, buf.String()
}

func TestCustomNav_HappyPath(t *testing.T) {
	body := `
# Hand-curated nav
"About"       : "about/"
"Quick start" : "@static-notes/"
"Repositories"
   - "leyline-server" : "https://github.com/example/leyline-server"
   - "leyline-cli"    : "https://github.com/example/leyline-cli"
`
	idMap := map[string]string{"static-notes": "/notes"}
	nodes, logs := runParseCustomNav(t, body, "/", nil, idMap)
	if len(nodes) != 3 {
		t.Fatalf("nodes = %d, want 3 (%+v)", len(nodes), nodes)
	}
	if nodes[0].Title != "About" || nodes[0].URL != "/about/" {
		t.Errorf("nodes[0] = %+v", nodes[0])
	}
	if nodes[1].URL != "/notes/" {
		t.Errorf("cross-vault: nodes[1].URL = %q, want /notes/", nodes[1].URL)
	}
	if !nodes[2].ParentOnly || len(nodes[2].Children) != 2 {
		t.Errorf("Repositories: ParentOnly=%v, children=%d", nodes[2].ParentOnly, len(nodes[2].Children))
	}
	if nodes[2].Children[0].URL != "https://github.com/example/leyline-server" {
		t.Errorf("child[0].URL = %q", nodes[2].Children[0].URL)
	}
	if logs != "" {
		t.Errorf("unexpected warnings: %s", logs)
	}
}

func TestCustomNav_HashInsideQuotesIsLiteral(t *testing.T) {
	body := `"Title with # symbol" : "target#section"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || nodes[0].Title != "Title with # symbol" {
		t.Errorf("nodes = %+v", nodes)
	}
	if nodes[0].URL != "/target#section" {
		t.Errorf("URL = %q, want /target#section", nodes[0].URL)
	}
	if logs != "" {
		t.Errorf("unexpected warnings: %s", logs)
	}
}

func TestCustomNav_TopLevelWithoutLinkIsParentOnly(t *testing.T) {
	body := `"Group"
   - "Child" : "child/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || !nodes[0].ParentOnly {
		t.Fatalf("nodes = %+v", nodes)
	}
	if nodes[0].URL != "" {
		t.Errorf("URL = %q, want \"\"", nodes[0].URL)
	}
	if len(nodes[0].Children) != 1 || nodes[0].Children[0].URL != "/child/" {
		t.Errorf("child = %+v", nodes[0].Children)
	}
	if logs != "" {
		t.Errorf("unexpected warnings: %s", logs)
	}
}

func TestCustomNav_MissingColonSkipped(t *testing.T) {
	body := `"missing colon" "target/"
"good" : "valid/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || nodes[0].Title != "good" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !strings.Contains(logs, "expected ':' after title") {
		t.Errorf("expected colon-warn in logs:\n%s", logs)
	}
}

func TestCustomNav_OrphanChildBeforeAnyTopLevel(t *testing.T) {
	body := `- "orphan" : "x/"
"first top" : "top/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || nodes[0].Title != "first top" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !strings.Contains(logs, "child line before any top-level item") {
		t.Errorf("expected orphan-warn in logs:\n%s", logs)
	}
}

func TestCustomNav_UnterminatedQuoteSkipped(t *testing.T) {
	// A literal unterminated quote — no second `"` anywhere on the line.
	body := `"unterminated
"good" : "valid/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || nodes[0].Title != "good" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !strings.Contains(logs, "unterminated quoted token") {
		t.Errorf("expected unterminated-warn in logs:\n%s", logs)
	}
}

func TestCustomNav_EmbeddedQuoteSkipped(t *testing.T) {
	// `"foo "x" bar" : "y/"` — takeQuoted closes title at the first inner
	// `"`, then sees `x" bar" : "y/"`, the next non-space char is `x` (not
	// `:`), so the line is rejected.
	body := `"foo "x" bar" : "y/"`
	_, logs := runParseCustomNav(t, body, "/", nil, nil)
	if !strings.Contains(logs, "expected ':' after title") {
		t.Errorf("expected embedded-quote warn in logs:\n%s", logs)
	}
}

func TestCustomNav_ChildRequiresLink(t *testing.T) {
	body := `"parent" : "p/"
   - "no-link"
   - "ok" : "ok/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || len(nodes[0].Children) != 1 {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !strings.Contains(logs, "child requires a link target") {
		t.Errorf("expected child-link warn in logs:\n%s", logs)
	}
}

func TestCustomNav_TrailingCharactersRejected(t *testing.T) {
	body := `"title" : "target/" garbage
"ok" : "ok/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if len(nodes) != 1 || nodes[0].Title != "ok" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if !strings.Contains(logs, "trailing characters after link") {
		t.Errorf("expected trailing-chars warn in logs:\n%s", logs)
	}
}

func TestCustomNav_AliasInWikilinkResolved(t *testing.T) {
	resolver := stubResolver{m: map[string]string{"note": "/notes/note"}}
	body := `"Note" : "[[note]]"
"Anchor" : "[[note#section]]"`
	nodes, logs := runParseCustomNav(t, body, "/notes", resolver, nil)
	if logs != "" {
		t.Errorf("unexpected warnings: %s", logs)
	}
	if nodes[0].URL != "/notes/note" {
		t.Errorf("wikilink URL = %q", nodes[0].URL)
	}
	if nodes[1].URL != "/notes/note#section" {
		t.Errorf("wikilink+anchor URL = %q", nodes[1].URL)
	}
}

func TestCustomNav_CrossVaultUnknownDegradesQuietly(t *testing.T) {
	body := `"Other" : "@missing/path"`
	nodes, _ := runParseCustomNav(t, body, "/", nil, map[string]string{})
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	if nodes[0].URL != "" {
		t.Errorf("unknown cross-vault URL = %q, want \"\"", nodes[0].URL)
	}
}

func TestCustomNav_DepthOneOnly(t *testing.T) {
	// A `-` line after another child still attaches to the most recent
	// top-level item — grandchildren are flattened because there is no
	// nested-child syntax. The grammar accepts it; the runtime guarantees
	// depth-1.
	body := `"Top" : "top/"
- "child a" : "a/"
- "child b" : "b/"`
	nodes, logs := runParseCustomNav(t, body, "/", nil, nil)
	if logs != "" {
		t.Errorf("unexpected warnings: %s", logs)
	}
	if len(nodes) != 1 || len(nodes[0].Children) != 2 {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestCustomNav_FileMissingIsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := ParseCustomNavFile(filepath.Join(t.TempDir(), "ghost"), "/", nil, nil, logger)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

type stubResolver struct {
	m map[string]string
}

func (s stubResolver) Resolve(target string) (string, bool) {
	url, ok := s.m[target]
	return url, ok
}
