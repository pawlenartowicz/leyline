package roles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/caps"
)

func TestParse_GoldenPath(t *testing.T) {
	body := `
# comment
researcher  sync.pull,sync.push
viewer      sync.pull
`
	out, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := len(out), 2; got != want {
		t.Fatalf("len(out) = %d, want %d", got, want)
	}
	if !out["researcher"].Has(caps.SyncPush) || !out["researcher"].Has(caps.SyncPull) {
		t.Errorf("researcher should hold sync.pull + sync.push")
	}
	if !out["viewer"].Has(caps.SyncPull) || out["viewer"].Has(caps.SyncPush) {
		t.Errorf("viewer should hold sync.pull only")
	}
}

func TestParse_DropsInvalid(t *testing.T) {
	body := strings.Join([]string{
		"# comment",
		"",
		"Bad-Name  sync.pull",        // uppercase
		"admin  sync.pull,sync.push", // reserved (built-in)
		"my_guest sync.pull",         // reserved (suffix _guest)
		"researcher",                 // missing caps list
		"researcher sync.pull bonus", // extra field
		"researcher sync.bogus",      // unknown capability
		"researcher sync.pull",       // first valid → keeps
		"researcher sync.push",       // duplicate
	}, "\n")
	out, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := len(out), 1; got != want {
		t.Fatalf("len(out) = %d, want %d. out=%v", got, want, out)
	}
	if !out["researcher"].Has(caps.SyncPull) {
		t.Errorf("researcher should be the survivor with sync.pull")
	}
	if out["researcher"].Has(caps.SyncPush) {
		t.Errorf("researcher's duplicate row should not have merged sync.push")
	}
}

func TestParse_NilReader(t *testing.T) {
	out, err := Parse(nil)
	if err != nil || out == nil || len(out) != 0 {
		t.Errorf("Parse(nil) = (%v, %v), want (empty map, nil)", out, err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	out, err := Load(filepath.Join(dir, "absent"))
	if err != nil {
		t.Errorf("Load(absent): unexpected error %v", err)
	}
	if out == nil || len(out) != 0 {
		t.Errorf("Load(absent) = %v, want empty map", out)
	}
}

func TestLoad_RealFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "roles")
	if err := os.WriteFile(p, []byte("research sync.pull\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !out["research"].Has(caps.SyncPull) {
		t.Errorf("research should hold sync.pull, got %v", out)
	}
}
