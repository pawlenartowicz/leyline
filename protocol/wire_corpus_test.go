package protocol_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// TestWireCorpus_RoundTrip loads every *.cbor file under testdata/wire/,
// decodes it as both a client and a server message (whichever applies),
// re-encodes it, and asserts the bytes are identical to the golden blob.
// This catches encoder drift: if a field tag changes or the CBOR mode
// changes, the round-trip will diverge and the test fails.
func TestWireCorpus_RoundTrip(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	wireDir := filepath.Join(filepath.Dir(thisFile), "testdata", "wire")

	entries, err := os.ReadDir(wireDir)
	if err != nil {
		t.Fatalf("open wire dir: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cbor") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".cbor")
		t.Run(name, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join(wireDir, entry.Name()))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}

			// Try client parse first, then server parse; one must succeed.
			_, msg, clientErr := protocol.ParseClientMessage(golden)
			if clientErr != nil {
				_, msg, err = protocol.ParseServerMessage(golden)
				if err != nil {
					// Neither side could parse — record both errors and fail.
					t.Fatalf("ParseClientMessage: %v\nParseServerMessage: %v", clientErr, err)
				}
			}

			// Re-encode and compare.
			got, err := protocol.Encode(msg)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			if !bytes.Equal(got, golden) {
				t.Errorf("round-trip mismatch for %s:\n  golden: %x\n  got:    %x", name, golden, got)
			}
		})
	}
}
