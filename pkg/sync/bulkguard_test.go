package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestBulkDeleteThreshold(t *testing.T) {
	cases := []struct {
		name string
		c    ReconcileCounts
		want bool
	}{
		// Exact threshold: 25% / 10 files.
		{"exact-threshold-10/40", ReconcileCounts{Deletes: 10, ManifestSize: 40}, true},
		// Super-threshold: 100% deletes.
		{"super-threshold-100/100", ReconcileCounts{Deletes: 100, ManifestSize: 100}, true},
		// Near-miss-fraction: 24% (9.6/40 rounds down to 9 deletes... use 9/40 = 22.5%).
		{"near-miss-fraction-9/40", ReconcileCounts{Deletes: 9, ManifestSize: 40}, false},
		// Near-miss-floor: 9 deletes regardless of fraction.
		{"near-miss-floor-9/9", ReconcileCounts{Deletes: 9, ManifestSize: 9}, false},
		// Empty manifest: never fires (no fraction).
		{"empty-manifest", ReconcileCounts{Deletes: 50, ManifestSize: 0}, false},
		// Zero deletes: never fires.
		{"zero-deletes", ReconcileCounts{Deletes: 0, ManifestSize: 100}, false},
		// Just-above floor with high fraction.
		{"floor-just-met-10/30", ReconcileCounts{Deletes: 10, ManifestSize: 30}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BulkDeleteThreshold(tc.c); got != tc.want {
				t.Errorf("BulkDeleteThreshold(%+v) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

func TestWriteConfirmMarker_TemplateContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "LEYLINE_CONFIRM_NEEDED.txt")
	ops := []protocol.Op{
		{Type: protocol.OpDelete, Path: "notes/a.md"},
		{Type: protocol.OpDelete, Path: "notes/b.md"},
		{Type: protocol.OpDelete, Path: "z.md"},
		// A non-delete should NOT appear in the sample list.
		{Type: protocol.OpWrite, Path: "k.md"},
	}
	counts := ReconcileCounts{Adds: 1, Modifies: 0, Deletes: 3, ManifestSize: 4}
	if err := WriteConfirmMarker(path, counts, ops); err != nil {
		t.Fatalf("WriteConfirmMarker: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"Leyline detected a bulk deletion",
		"Adds:       1",
		"Deletes:    3",
		"Manifest:   4",
		"notes/a.md",
		"notes/b.md",
		"z.md",
		"leyline confirm",
		"leyline restore-local",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marker missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "k.md") {
		t.Error("marker should not list non-delete paths")
	}
}

func TestConfirmMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "LEYLINE_CONFIRM_NEEDED.txt")
	if ConfirmMarkerPresent(path) {
		t.Error("marker should not be present before write")
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !ConfirmMarkerPresent(path) {
		t.Error("marker should be present after write")
	}
}
