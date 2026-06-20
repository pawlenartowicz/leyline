package allowed

import (
	"os"
	"path/filepath"
	"testing"
)

const testAllowed = `# test config
[sync]
*.md
*.txt
*.png

[history]
*.md
*.txt

[limits]
sync = 10mb
history = 1mb
`

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"10mb", 10 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
		{"500kb", 500 * 1024},
		{"1gb", 1024 * 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got, err := ParseSize(tt.input)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestParseSize_Invalid(t *testing.T) {
	for _, input := range []string{"", "10", "10tb", "abc", "-5mb"} {
		if _, err := ParseSize(input); err == nil {
			t.Errorf("ParseSize(%q) should fail", input)
		}
	}
}

func TestRules_CanSync(t *testing.T) {
	path := writeTestFile(t, testAllowed)
	rules, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		size int64
		ok   bool
	}{
		{"notes/hello.md", 100, true},
		{"docs/readme.txt", 100, true},
		{"images/photo.png", 100, true},
		{"script.py", 100, false},                   // not in [sync]
		{"notes/hello.md", 11 * 1024 * 1024, false}, // over sync limit
	}
	for _, tt := range tests {
		ok, _ := rules.CanSync(tt.path, tt.size)
		if ok != tt.ok {
			t.Errorf("CanSync(%q, %d) = %v, want %v", tt.path, tt.size, ok, tt.ok)
		}
	}
}

func TestRules_HasHistory(t *testing.T) {
	path := writeTestFile(t, testAllowed)
	rules, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		size int64
		want bool
	}{
		{"notes/hello.md", 100, true},
		{"docs/readme.txt", 100, true},
		{"images/photo.png", 100, false},         // in sync, not in history
		{"notes/big.md", 2 * 1024 * 1024, false}, // over history limit
	}
	for _, tt := range tests {
		got := rules.HasHistory(tt.path, tt.size)
		if got != tt.want {
			t.Errorf("HasHistory(%q, %d) = %v, want %v", tt.path, tt.size, got, tt.want)
		}
	}
}

func TestRules_SyncPatterns(t *testing.T) {
	path := writeTestFile(t, testAllowed)
	rules, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	patterns := rules.SyncPatterns()
	if len(patterns) != 3 {
		t.Errorf("expected 3 sync patterns, got %d", len(patterns))
	}
}

func TestRules_HistoryPatterns(t *testing.T) {
	path := writeTestFile(t, testAllowed)
	rules, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	patterns := rules.HistoryPatterns()
	if len(patterns) != 2 {
		t.Errorf("expected 2 history patterns, got %d", len(patterns))
	}
}

func TestRules_SyncLimit(t *testing.T) {
	path := writeTestFile(t, testAllowed)
	rules, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := rules.SyncLimit(); got != 10*1024*1024 {
		t.Errorf("SyncLimit = %d, want 10mb", got)
	}
}

// TestLoad_MissingFile: Load returns an error when the file does not exist.
func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load("/no/such/path/allowed"); err == nil {
		t.Error("expected error loading missing file")
	}
}

// TestLoad_BadLimit: a malformed limit value (unparseable size) must
// surface a wrapped ParseSize error rather than silently default.
func TestLoad_BadLimit(t *testing.T) {
	bad := "[limits]\nsync = potato\n"
	path := writeTestFile(t, bad)
	if _, err := Load(path); err == nil {
		t.Error("expected error loading bad limit value")
	}
}

func TestMatchHistoryPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed")
	body := "[history]\n*.md\n*.txt\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r.MatchHistoryPattern("notes/foo.md") {
		t.Error("foo.md should match")
	}
	if r.MatchHistoryPattern("notes/foo.png") {
		t.Error("foo.png should not match")
	}
}

func TestReload_SwapOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed")
	if err := os.WriteFile(path, []byte("[history]\n*.md\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, _ := Load(path)
	if err := os.WriteFile(path, []byte("[history]\n*.md\n*.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if !r.MatchHistoryPattern("a.txt") {
		t.Fatal("reload didn't pick up new pattern")
	}
}

func TestReload_KeepOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "allowed")
	if err := os.WriteFile(path, []byte("[history]\n*.md\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, _ := Load(path)
	if err := os.WriteFile(path, []byte("[limits]\nsync=not-a-number\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(); err == nil {
		t.Fatal("expected parse error")
	}
	if !r.MatchHistoryPattern("a.md") {
		t.Fatal("previous rules must survive Reload failure")
	}
}
