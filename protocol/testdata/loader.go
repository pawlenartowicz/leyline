// Package testdata provides loaders for the shared wire + merge + access fixture corpus.
// Tests import this package to get deterministic, canonical byte sequences that are
// the single source of truth for Go and TypeScript implementations.
package testdata

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// corpusDir returns the absolute path to this package's directory, regardless
// of the working directory the test was invoked from.
func corpusDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// LoadWireGolden reads testdata/wire/<name>.cbor and returns the raw bytes.
// t.Fatalf is called on any error. name should not include the .cbor extension.
func LoadWireGolden(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(corpusDir(), "wire", name+".cbor")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LoadWireGolden(%q): %v", name, err)
	}
	return data
}

// MergeInput is the JSON envelope stored in <formatter>/<case>.in files.
// For callout/comment/gitmarker/threeway:
//   Base, Server, Client hold the merge inputs;
//   Hint carries format-specific parameters (unparsed string).
// For sidecar:
//   Base = originalPath, Server = timestamp, Client = "" (unused).
type MergeInput struct {
	Base   string `json:"base"`
	Server string `json:"server"`
	Client string `json:"client"`
	Hint   string `json:"hint,omitempty"`
}

// LoadMergeGoldenInput reads testdata/merge/<formatter>/<name>.in, parses
// the JSON envelope, and returns the MergeInput.
func LoadMergeGoldenInput(t *testing.T, formatter, name string) MergeInput {
	t.Helper()
	path := filepath.Join(corpusDir(), "merge", formatter, name+".in")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LoadMergeGoldenInput(%q, %q): %v", formatter, name, err)
	}
	var in MergeInput
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatalf("LoadMergeGoldenInput(%q, %q) parse: %v", formatter, name, err)
	}
	return in
}

// LoadMergeGoldenWant reads testdata/merge/<formatter>/<name>.want and
// returns the expected output bytes.
func LoadMergeGoldenWant(t *testing.T, formatter, name string) []byte {
	t.Helper()
	path := filepath.Join(corpusDir(), "merge", formatter, name+".want")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LoadMergeGoldenWant(%q, %q): %v", formatter, name, err)
	}
	return data
}

// --- classify corpus ---
//
// The classify corpus pins the routing table of pkg/merge.Classify (Go)
// and src/merge/classify.ts (TS): given a (catchup, staged?) pair and
// a context, both implementations must agree on the (LogKind, LogFormat,
// DiskAction) triple. Disk-content and replacement-staged bytes are
// covered by the per-formatter corpora (callout/comment/gitmarker/sidecar)
// and are intentionally NOT checked here.

// ClassifyOpJSON mirrors the on-disk JSON shape of a fixture op. The
// loader returns it as-is; callers convert to their own Op type. Use
// DataBytes() to resolve the data payload (data_b64 wins over data).
type ClassifyOpJSON struct {
	Seq     uint64 `json:"seq"`
	Type    string `json:"type"`
	Path    string `json:"path,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Data    string `json:"data,omitempty"`     // UTF-8 text payload
	DataB64 string `json:"data_b64,omitempty"` // base64 raw bytes; wins over Data when present
	Binary  bool   `json:"binary,omitempty"`
	TS      int64  `json:"ts"`
}

// DataBytes returns the resolved data payload, decoding data_b64 when
// present, otherwise treating data as a UTF-8 string. Returns nil when
// neither field is set (the "no payload" case).
func (o ClassifyOpJSON) DataBytes() []byte {
	if o.DataB64 != "" {
		b, err := base64.StdEncoding.DecodeString(o.DataB64)
		if err != nil {
			return nil
		}
		return b
	}
	if o.Data == "" {
		return nil
	}
	return []byte(o.Data)
}

// ClassifyCtxJSON mirrors the merge.Context fields the classifier reads.
type ClassifyCtxJSON struct {
	Base          string `json:"base"`
	DiffMode      string `json:"diff_mode"`
	ServerKeyname string `json:"server_keyname"`
	ClientKeyname string `json:"client_keyname"`
	TS            string `json:"ts"`
}

// ClassifyInput is the parsed <case>.in envelope.
type ClassifyInput struct {
	Catchup ClassifyOpJSON  `json:"catchup"`
	Staged  *ClassifyOpJSON `json:"staged,omitempty"`
	Ctx     ClassifyCtxJSON `json:"ctx"`
}

// ClassifyWant is the parsed <case>.want.json envelope. Kind/Format are
// the LogKind/LogFormat string values in snake_case (e.g. "auto_merge",
// "callout"). Action is the DiskAction in snake_case: apply, auto_merge,
// write_conflict, write_sidecar, apply_rename, apply_delete, noop.
type ClassifyWant struct {
	Kind   string `json:"kind"`
	Format string `json:"format"`
	Action string `json:"action"`
}

// LoadClassifyCase reads testdata/merge/classify/<name>.in and the
// matching <name>.want.json.
func LoadClassifyCase(t *testing.T, name string) (ClassifyInput, ClassifyWant) {
	t.Helper()
	inPath := filepath.Join(corpusDir(), "merge", "classify", name+".in")
	wantPath := filepath.Join(corpusDir(), "merge", "classify", name+".want.json")
	inRaw, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("LoadClassifyCase(%q) in: %v", name, err)
	}
	wantRaw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("LoadClassifyCase(%q) want: %v", name, err)
	}
	var in ClassifyInput
	if err := json.Unmarshal(inRaw, &in); err != nil {
		t.Fatalf("LoadClassifyCase(%q) parse in: %v", name, err)
	}
	var want ClassifyWant
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatalf("LoadClassifyCase(%q) parse want: %v", name, err)
	}
	return in, want
}

// ListClassifyCases enumerates the case names (without extension) found
// under testdata/merge/classify/, in sorted order.
func ListClassifyCases(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(corpusDir(), "merge", "classify")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ListClassifyCases: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".in") {
			out = append(out, strings.TrimSuffix(name, ".in"))
		}
	}
	sort.Strings(out)
	return out
}

// LoadAccessGolden reads the access file and its matching resolved-caps JSON
// for the named fixture case.
// Returns (accessBytes, capsJSON).
func LoadAccessGolden(t *testing.T, name string) (accessBytes, capsJSON []byte) {
	t.Helper()
	aPath := filepath.Join(corpusDir(), "access", name+".access")
	cPath := filepath.Join(corpusDir(), "access", name+".caps.json")

	ab, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("LoadAccessGolden(%q) access: %v", name, err)
	}
	cj, err := os.ReadFile(cPath)
	if err != nil {
		t.Fatalf("LoadAccessGolden(%q) caps: %v", name, err)
	}
	return ab, cj
}
