package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestDigest_Empty(t *testing.T) {
	got := ManifestDigest(nil)
	want := sha256.Sum256(nil)
	if got != want {
		t.Errorf("ManifestDigest(nil) = %x, want %x (sha256 of empty)", got, want)
	}
}

func TestManifestDigest_FormulaShape(t *testing.T) {
	a := hashFromHex(t, "0101010101010101010101010101010101010101010101010101010101010101")
	b := hashFromHex(t, "0202020202020202020202020202020202020202020202020202020202020202")

	// Direct: SHA-256 of "a.md\t01...01\nb.md\t02...02\n".
	expected := sha256.Sum256([]byte(
		"a.md\t0101010101010101010101010101010101010101010101010101010101010101\n" +
			"b.md\t0202020202020202020202020202020202020202020202020202020202020202\n",
	))
	got := ManifestDigest([]ManifestEntry{
		{Path: "a.md", Hash: a},
		{Path: "b.md", Hash: b},
	})
	if got != expected {
		t.Errorf("ManifestDigest(a,b): got %x, want %x", got, expected)
	}
}

func TestManifestDigest_SortsInternally(t *testing.T) {
	a := hashFromHex(t, "0101010101010101010101010101010101010101010101010101010101010101")
	b := hashFromHex(t, "0202020202020202020202020202020202020202020202020202020202020202")
	sorted := ManifestDigest([]ManifestEntry{{Path: "a.md", Hash: a}, {Path: "b.md", Hash: b}})
	reversed := ManifestDigest([]ManifestEntry{{Path: "b.md", Hash: b}, {Path: "a.md", Hash: a}})
	if sorted != reversed {
		t.Errorf("ManifestDigest should sort internally; sorted=%x, reversed=%x", sorted, reversed)
	}
}

func TestManifestDigest_DoesNotMutateInput(t *testing.T) {
	a := hashFromHex(t, "0101010101010101010101010101010101010101010101010101010101010101")
	b := hashFromHex(t, "0202020202020202020202020202020202020202020202020202020202020202")
	in := []ManifestEntry{{Path: "b.md", Hash: b}, {Path: "a.md", Hash: a}}
	_ = ManifestDigest(in)
	if in[0].Path != "b.md" || in[1].Path != "a.md" {
		t.Errorf("ManifestDigest mutated input: got %+v", in)
	}
}

// TestManifestDigest_Corpus loads testdata/manifest_digest/cases.json and
// checks that the digest matches what we recompute live. The corpus exists
// to give the plugin a TS-side reference to test against; the want_digest
// values are sanity-checked here against a fresh in-process computation.
func TestManifestDigest_Corpus(t *testing.T) {
	blob, err := os.ReadFile(filepath.Join("testdata", "manifest_digest", "cases.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var cases []manifestDigestCase
	if err := json.Unmarshal(blob, &cases); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			entries := make([]ManifestEntry, len(tc.Entries))
			for i, e := range tc.Entries {
				entries[i] = ManifestEntry{Path: e.Path, Hash: hashFromHex(t, e.Hash)}
			}
			got := ManifestDigest(entries)
			gotHex := hex.EncodeToString(got[:])
			if gotHex != tc.WantDigest {
				t.Errorf("ManifestDigest(%s): got %s, want %s", tc.Name, gotHex, tc.WantDigest)
			}
		})
	}
}

type manifestDigestCase struct {
	Name    string `json:"name"`
	Entries []struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
	} `json:"entries"`
	WantDigest string `json:"want_digest"`
}

func hashFromHex(t *testing.T, s string) Hash {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q): %v", s, err)
	}
	if len(b) != 32 {
		t.Fatalf("hash hex must decode to 32 bytes, got %d", len(b))
	}
	var h Hash
	copy(h[:], b)
	return h
}
