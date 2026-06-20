package search

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// ---- Analyzer / trigram tests ----

func TestTokenize_Basic(t *testing.T) {
	words := tokenize("hello world")
	if len(words) != 2 {
		t.Fatalf("want 2 words, got %d: %v", len(words), words)
	}
	if words[0] != "hello" || words[1] != "world" {
		t.Errorf("unexpected words: %v", words)
	}
}

func TestTokenize_NonAlpha(t *testing.T) {
	words := tokenize("hello-world foo.bar")
	for _, w := range words {
		if w == "hello-world" {
			t.Errorf("should split on dash, got word %q", w)
		}
	}
	if len(words) != 4 {
		t.Fatalf("want 4 words (hello, world, foo, bar), got %d: %v", len(words), words)
	}
}

func TestTrigramWord_Short(t *testing.T) {
	// word "ab" is < 3 runes → produces single short gram " ab"
	grams := trigramWord("ab")
	if len(grams) != 1 {
		t.Fatalf("want 1 gram for 'ab', got %d: %v", len(grams), grams)
	}
	if grams[0] != " ab" {
		t.Errorf("gram = %q, want \" ab\"", grams[0])
	}
}

func TestTrigramWord_Normal(t *testing.T) {
	// "cat" → " cat " → grams: " ca", "cat", "at "
	grams := trigramWord("cat")
	want := []string{" ca", "cat", "at "}
	if len(grams) != len(want) {
		t.Fatalf("want %v, got %v", want, grams)
	}
	for i, g := range grams {
		if g != want[i] {
			t.Errorf("gram[%d] = %q, want %q", i, g, want[i])
		}
	}
}

func TestAnalyzer_PolishDiacritics(t *testing.T) {
	v := newVocab()
	a := NewTrigramAnalyzer(v)

	// "łączność" should be indexed without folding diacritics.
	ids := a.Analyze("łączność")
	if len(ids) == 0 {
		t.Fatal("expected non-empty gram IDs for łączność")
	}

	// Query for substring "łączn" should share grams with "łączność".
	qIDs := a.AnalyzeQuery("łączn")
	if len(qIDs) == 0 {
		t.Fatal("expected non-empty gram IDs for query łączn")
	}

	// At least one gram must intersect.
	if intersectLen(ids, qIDs) == 0 {
		t.Error("łączn should share trigrams with łączność")
	}
}

func TestAnalyzer_Paths(t *testing.T) {
	v := newVocab()
	a := NewTrigramAnalyzer(v)

	// "notes/sieci.md" splits on "/" and "." so "notes", "sieci", "md" are
	// separate words — each gets its own trigrams.
	ids := a.Analyze("notes/sieci.md")
	if len(ids) == 0 {
		t.Fatal("expected grams for path string")
	}
}

func TestAnalyzer_Code(t *testing.T) {
	v := newVocab()
	a := NewTrigramAnalyzer(v)

	// Identifiers and symbols in code should tokenize without crashing.
	ids := a.Analyze("func main() { fmt.Println(\"hello\") }")
	if len(ids) == 0 {
		t.Fatal("expected grams for code string")
	}
}

func TestAnalyzer_SortedAndDeduped(t *testing.T) {
	v := newVocab()
	a := NewTrigramAnalyzer(v)

	ids := a.Analyze("the quick brown fox jumps over the lazy dog")
	for i := 1; i < len(ids); i++ {
		if ids[i] < ids[i-1] {
			t.Errorf("ids not sorted at index %d: %d < %d", i, ids[i], ids[i-1])
		}
		if ids[i] == ids[i-1] {
			t.Errorf("duplicate id %d at index %d", ids[i], i)
		}
	}
}

func TestAnalyzer_EmptyString(t *testing.T) {
	v := newVocab()
	a := NewTrigramAnalyzer(v)
	ids := a.Analyze("")
	if len(ids) != 0 {
		t.Errorf("expected empty grams for empty string, got %v", ids)
	}
}

// ---- ExtractText tests ----

func TestExtractText_MarkdownFrontmatter(t *testing.T) {
	data := []byte("---\ntitle: Łączność\ntags:\n  - sieci\n---\n\n# Body\n\nsome text")
	ex := ExtractText("note.md", data, webignore.ModeMarkdown)

	if ex.Title == "" {
		t.Error("expected non-empty title from frontmatter")
	}
	if ex.Tags == "" {
		t.Error("expected non-empty tags from frontmatter")
	}
	if ex.Body == "" {
		t.Error("expected non-empty body")
	}
}

func TestExtractText_MarkdownNoFrontmatter(t *testing.T) {
	data := []byte("# Heading\n\nJust a paragraph.")
	ex := ExtractText("note.md", data, webignore.ModeMarkdown)

	if ex.Body == "" {
		t.Error("expected non-empty body for markdown without frontmatter")
	}
}

func TestExtractText_MarkdownWikilinks(t *testing.T) {
	data := []byte("See [[target page|alias text]] for more.")
	ex := ExtractText("note.md", data, webignore.ModeMarkdown)

	// Alias "alias text" should appear in body; "target page" should appear
	// when there's no alias.
	if ex.Body == "" {
		t.Error("expected non-empty body")
	}
}

func TestExtractText_Text(t *testing.T) {
	data := []byte("plain text content")
	ex := ExtractText("file.txt", data, webignore.ModeText)

	if ex.Body != "plain text content" {
		t.Errorf("text body = %q, want verbatim", ex.Body)
	}
}

func TestExtractText_HTML(t *testing.T) {
	data := []byte("<html><body><h1>Hello</h1><script>alert(1)</script></body></html>")
	ex := ExtractText("file.html", data, webignore.ModeHTML)

	if ex.Body == "" {
		t.Error("expected non-empty body from HTML")
	}
	// Script content should be stripped.
	if len(ex.Body) > 0 && containsSubstr(ex.Body, "<script>") {
		t.Error("HTML extraction should strip script tags")
	}
}

func TestExtractText_Asset_NotIndexed(t *testing.T) {
	data := []byte("\x89PNG\r\n\x1a\n")
	ex := ExtractText("image.png", data, webignore.ModeAsset)

	if ex.Body != "" || ex.Title != "" || ex.Tags != "" {
		t.Error("asset mode should return empty Extracted")
	}
}

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

// ---- Index build/update/remove tests ----

func makeTestIndex(t *testing.T) *Index {
	t.Helper()
	idx := NewIndex()
	idx.Build([]IndexFile{
		{Path: "a.md", Hash: [32]byte{1}, Data: []byte("# Alpha\n\nalpha content"), Mode: webignore.ModeMarkdown},
		{Path: "b.md", Hash: [32]byte{2}, Data: []byte("---\ntitle: Beta\n---\nbeta content"), Mode: webignore.ModeMarkdown},
		{Path: "c.txt", Hash: [32]byte{3}, Data: []byte("gamma plain text"), Mode: webignore.ModeText},
	})
	return idx
}

func TestIndex_Build(t *testing.T) {
	idx := makeTestIndex(t)
	if idx.DocCount() < 1 {
		t.Fatal("expected at least 1 doc after Build")
	}
}

func TestIndex_UpdateFile_New(t *testing.T) {
	idx := makeTestIndex(t)
	before := idx.DocCount()
	idx.UpdateFile("new.md", [32]byte{99}, []byte("# New file\nnew content"), webignore.ModeMarkdown)
	after := idx.DocCount()
	if after != before+1 {
		t.Errorf("DocCount after UpdateFile: got %d, want %d", after, before+1)
	}
}

func TestIndex_UpdateFile_Replace(t *testing.T) {
	idx := makeTestIndex(t)
	before := idx.DocCount()
	// Update an existing file with new content.
	idx.UpdateFile("a.md", [32]byte{11}, []byte("# Updated\nupdated content"), webignore.ModeMarkdown)
	after := idx.DocCount()
	if after != before {
		t.Errorf("DocCount after update should be unchanged: got %d, want %d", after, before)
	}
}

func TestIndex_RemoveFile(t *testing.T) {
	idx := makeTestIndex(t)
	before := idx.DocCount()
	idx.RemoveFile("a.md")
	after := idx.DocCount()
	if after != before-1 {
		t.Errorf("DocCount after RemoveFile: got %d, want %d", after, before-1)
	}
}

func TestIndex_RemoveFile_Idempotent(t *testing.T) {
	idx := makeTestIndex(t)
	before := idx.DocCount()
	idx.RemoveFile("nonexistent.md")
	if idx.DocCount() != before {
		t.Error("RemoveFile of non-existent path should be a no-op")
	}
}

// ---- Cache reconciliation tests ----

func TestCache_RoundTrip(t *testing.T) {
	idx := makeTestIndex(t)
	cacheDir := t.TempDir()
	vaultID := "test-vault"

	SaveCache(idx, cacheDir, vaultID, discardLogger())

	// Simulate a second startup with the same live files.
	liveFiles := []IndexFile{
		{Path: "a.md", Hash: [32]byte{1}, Data: []byte("# Alpha\n\nalpha content"), Mode: webignore.ModeMarkdown},
		{Path: "b.md", Hash: [32]byte{2}, Data: []byte("---\ntitle: Beta\n---\nbeta content"), Mode: webignore.ModeMarkdown},
		{Path: "c.txt", Hash: [32]byte{3}, Data: []byte("gamma plain text"), Mode: webignore.ModeText},
	}
	idx2 := LoadOrRebuild(cacheDir, vaultID, liveFiles, discardLogger())
	if idx2.DocCount() != idx.DocCount() {
		t.Errorf("loaded index has %d docs, want %d", idx2.DocCount(), idx.DocCount())
	}
}

func TestCache_HashChange_Reextract(t *testing.T) {
	idx := makeTestIndex(t)
	cacheDir := t.TempDir()
	vaultID := "test-vault"

	SaveCache(idx, cacheDir, vaultID, discardLogger())

	// a.md has a changed hash → must be re-extracted.
	liveFiles := []IndexFile{
		{Path: "a.md", Hash: [32]byte{10}, Data: []byte("# Changed\nchanged content"), Mode: webignore.ModeMarkdown},
		{Path: "b.md", Hash: [32]byte{2}, Data: []byte("---\ntitle: Beta\n---\nbeta content"), Mode: webignore.ModeMarkdown},
		{Path: "c.txt", Hash: [32]byte{3}, Data: []byte("gamma plain text"), Mode: webignore.ModeText},
	}
	idx2 := LoadOrRebuild(cacheDir, vaultID, liveFiles, discardLogger())
	if idx2.DocCount() < 1 {
		t.Fatal("expected at least 1 doc after reconcile")
	}
}

func TestCache_Deletion(t *testing.T) {
	idx := makeTestIndex(t)
	cacheDir := t.TempDir()
	vaultID := "test-vault"

	SaveCache(idx, cacheDir, vaultID, discardLogger())

	// a.md is gone from live files → must be dropped.
	liveFiles := []IndexFile{
		{Path: "b.md", Hash: [32]byte{2}, Data: []byte("---\ntitle: Beta\n---\nbeta content"), Mode: webignore.ModeMarkdown},
		{Path: "c.txt", Hash: [32]byte{3}, Data: []byte("gamma plain text"), Mode: webignore.ModeText},
	}
	idx2 := LoadOrRebuild(cacheDir, vaultID, liveFiles, discardLogger())
	// a.md should not be present.
	idx2.mu.RLock()
	_, aPresent := idx2.docs["a.md"]
	idx2.mu.RUnlock()
	if aPresent {
		t.Error("deleted file a.md should not be in reconciled index")
	}
}

func TestCache_MissingFile_RebuildFromScratch(t *testing.T) {
	cacheDir := t.TempDir()
	vaultID := "no-cache-vault"

	liveFiles := []IndexFile{
		{Path: "a.md", Hash: [32]byte{1}, Data: []byte("# Alpha\n\nalpha content"), Mode: webignore.ModeMarkdown},
	}
	idx := LoadOrRebuild(cacheDir, vaultID, liveFiles, discardLogger())
	if idx.DocCount() == 0 {
		t.Error("expected docs even when no cache exists")
	}
}

// ---- Query coverage / boosts / snippet tests ----

func TestQuery_BasicMatch(t *testing.T) {
	idx := makeTestIndex(t)
	vaultRoot := t.TempDir()
	writeTmpFile(t, vaultRoot, "a.md", "# Alpha\n\nalpha content")
	writeTmpFile(t, vaultRoot, "b.md", "---\ntitle: Beta\n---\nbeta content")
	writeTmpFile(t, vaultRoot, "c.txt", "gamma plain text")

	results, _, err := idx.Query(context.Background(), "alpha", QueryOptions{VaultRoot: vaultRoot, VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'alpha'")
	}
	// a.md should be first since it contains "alpha" in title and body.
	if results[0].Path != "a.md" {
		t.Errorf("expected a.md as top result, got %q", results[0].Path)
	}
}

func TestQuery_NoMatch(t *testing.T) {
	idx := makeTestIndex(t)
	vaultRoot := t.TempDir()
	writeTmpFile(t, vaultRoot, "a.md", "# Alpha\n\nalpha content")

	results, _, err := idx.Query(context.Background(), "xyzzy", QueryOptions{VaultRoot: vaultRoot, VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 'xyzzy', got %d", len(results))
	}
}

func TestQuery_TitleBoost(t *testing.T) {
	// "Beta" appears only in b.md's title; "beta" appears in b.md's body too.
	// b.md should score higher than c.txt.
	idx := makeTestIndex(t)
	vaultRoot := t.TempDir()
	writeTmpFile(t, vaultRoot, "b.md", "---\ntitle: Beta\n---\nbeta content")
	writeTmpFile(t, vaultRoot, "c.txt", "gamma plain text")

	results, _, err := idx.Query(context.Background(), "beta", QueryOptions{VaultRoot: vaultRoot, VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'beta'")
	}
	if results[0].Path != "b.md" {
		t.Errorf("b.md (title match) should rank first, got %q", results[0].Path)
	}
}

func TestQuery_CoverageFloor(t *testing.T) {
	// A long query where almost no grams match should return no results.
	idx := NewIndex()
	idx.Build([]IndexFile{
		{Path: "x.md", Hash: [32]byte{1}, Data: []byte("completely different content here"), Mode: webignore.ModeMarkdown},
	})
	vaultRoot := t.TempDir()
	writeTmpFile(t, vaultRoot, "x.md", "completely different content here")

	// Query for something wholly absent.
	results, _, err := idx.Query(context.Background(), "postgresql database", QueryOptions{VaultRoot: vaultRoot, VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results below coverage floor, got %d", len(results))
	}
}

func TestQuery_SnippetNotEmpty(t *testing.T) {
	idx := makeTestIndex(t)
	vaultRoot := t.TempDir()
	writeTmpFile(t, vaultRoot, "a.md", "# Alpha\n\nalpha content here for snippet")

	results, _, err := idx.Query(context.Background(), "alpha", QueryOptions{VaultRoot: vaultRoot, VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// Snippet may be empty if file read fails (it's best-effort), but the
	// file exists so we expect a non-empty snippet.
	if results[0].Snippet == "" {
		t.Error("expected non-empty snippet when vault file exists")
	}
}

func TestQuery_HighlightRuneOffsets(t *testing.T) {
	// "łączność " precedes the match, so byte offsets and rune offsets diverge.
	// Highlights must be rune (code-point) offsets for a code-point slice to
	// land on the query text — this guards the JS client's Array.from slicing.
	content := "łączność sieci network topology"
	idx := NewIndex()
	idx.Build([]IndexFile{
		{Path: "p.md", Hash: [32]byte{2}, Data: []byte(content), Mode: webignore.ModeMarkdown},
	})
	vaultRoot := t.TempDir()
	writeTmpFile(t, vaultRoot, "p.md", content)

	results, _, err := idx.Query(context.Background(), "network", QueryOptions{VaultRoot: vaultRoot, VaultPrefix: "/"})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) == 0 || len(results[0].Highlights) == 0 {
		t.Fatal("expected a result with at least one highlight")
	}
	runes := []rune(results[0].Snippet)
	h := results[0].Highlights[0]
	if h[0] < 0 || h[1] > len(runes) || h[0] >= h[1] {
		t.Fatalf("highlight %v out of range for %d-rune snippet %q", h, len(runes), results[0].Snippet)
	}
	if got := string(runes[h[0]:h[1]]); got != "network" {
		t.Errorf("highlight rune slice = %q, want \"network\" (snippet %q)", got, results[0].Snippet)
	}
}

func TestQuery_TopK(t *testing.T) {
	idx := NewIndex()
	files := make([]IndexFile, 30)
	for i := range files {
		files[i] = IndexFile{
			Path: filepath.Join("dir", "file.md"),
			Hash: [32]byte{byte(i)},
			Data: []byte("alpha common word repeated here many times"),
			Mode: webignore.ModeMarkdown,
		}
		files[i].Path = filepath.Join("files", filepath.Base(files[i].Path))
		// Make unique paths.
		files[i].Path = filepath.Join("f", string(rune('a'+i%26))+".md")
	}
	idx.Build(files)
	vaultRoot := t.TempDir()
	for _, f := range files {
		writeTmpFile(t, vaultRoot, f.Path, string(f.Data))
	}

	results, truncated, err := idx.Query(context.Background(), "alpha", QueryOptions{
		VaultRoot:   vaultRoot,
		VaultPrefix: "/",
		TopK:        5,
	})
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(results) > 5 {
		t.Errorf("expected at most 5 results, got %d", len(results))
	}
	_ = truncated
}

// ---- Footprint guard test ----

func TestVaultSearch_FootprintGuard(t *testing.T) {
	vaultRoot := t.TempDir()
	// Write enough content to exceed a very small max_index_bytes.
	content := make([]byte, 100)
	for i := range content {
		content[i] = 'x'
	}
	writeTmpFile(t, vaultRoot, "big.txt", string(content))

	// Set a very small limit so the vault triggers the guard.
	vs := NewVaultSearch(
		vaultRoot, "guard-vault", t.TempDir(),
		VaultConfig{Enabled: true, MaxIndexBytes: 10},
		webignore.NewDispatch([]string{".txt"}),
		nil,
		discardLogger(),
	)

	err := vs.EnsureBuilt(context.Background())
	if err == nil {
		t.Fatal("expected ErrSearchDisabled from footprint guard")
	}
	if err != ErrSearchDisabled {
		t.Errorf("expected ErrSearchDisabled, got %v", err)
	}
}

// ---- helper functions ----

func writeTmpFile(t *testing.T, base, rel, content string) {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nopWriter{}, nil))
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
