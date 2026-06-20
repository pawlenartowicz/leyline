package stage

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// WALEntry is a single record in the WAL: the client that issued the op and
// the op itself. CBOR keyasint mirrors the style used throughout leyline-protocol.
type WALEntry struct {
	ClientID ClientID    `cbor:"1,keyasint"`
	Op       protocol.Op `cbor:"2,keyasint"`
}

// ReplayError is returned (alongside any entries decoded before the problem)
// when Replay encounters a CRC mismatch or a truncated frame. It is non-fatal:
// the caller receives the entries that were intact.
type ReplayError struct {
	Reason string
	Offset int64
}

func (e *ReplayError) Error() string {
	return fmt.Sprintf("wal: replay corruption at offset %d: %s", e.Offset, e.Reason)
}

// WAL is an append-only, fsync'd write-ahead log stored at
// filepath.Join(dir, vaultID+".wal"). All methods are safe for concurrent use.
type WAL struct {
	mu      sync.Mutex
	path    string // absolute path to the .wal file
	dir     string // parent directory (for directory fsync)
	vaultID string
	f       *os.File
}

// OpenWAL opens (or creates) the WAL file for vaultID inside dir.
// Returns an error if vaultID contains path separators (defense-in-depth
// validation; upstream already checks this).
// After creating/opening the file the parent directory is fsync'd so the
// directory entry survives a crash even if the file data hasn't been written yet.
func OpenWAL(dir, vaultID string) (*WAL, error) {
	if strings.ContainsAny(vaultID, "/\\") {
		return nil, fmt.Errorf("wal: vaultID must not contain path separators: %q", vaultID)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("wal: mkdir %s: %w", dir, err)
	}

	path := filepath.Join(dir, vaultID+".wal")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}

	if err := fsyncDir(dir); err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: fsync dir %s: %w", dir, err)
	}

	return &WAL{
		path:    path,
		dir:     dir,
		vaultID: vaultID,
		f:       f,
	}, nil
}

// Append encodes entry as a length-prefixed CRC'd CBOR frame and writes it to
// the WAL, fsyncing before returning. The caller must not modify op after
// passing it.
func (w *WAL) Append(clientID ClientID, op protocol.Op) error {
	entry := WALEntry{ClientID: clientID, Op: op}
	payload, err := protocol.Encode(entry)
	if err != nil {
		return fmt.Errorf("wal: encode: %w", err)
	}

	frame := makeFrame(payload)

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.f.Write(frame); err != nil {
		return fmt.Errorf("wal: write: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("wal: fsync: %w", err)
	}
	return nil
}

// Replay reads all entries from the WAL in append order. If a frame is
// truncated or has a CRC mismatch, Replay returns the entries decoded so far
// together with a *ReplayError describing the corruption. A clean EOF returns
// (entries, nil).
func (w *WAL) Replay() ([]WALEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Seek to the start without reopening — safe because we hold the mutex.
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("wal: seek: %w", err)
	}

	return replayFrom(w.f)
}

// TruncateClient rewrites the WAL dropping all entries for clientID.
// It writes to a compact temp file, fsyncs it, atomically renames it over the
// original, then fsyncs the parent directory. The WAL's internal file handle is
// refreshed to point at the new file.
func (w *WAL) TruncateClient(clientID ClientID) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Read all existing entries.
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("wal: seek for truncate: %w", err)
	}
	entries, _ := replayFrom(w.f) // ignore replay error; keep whatever was intact

	// Filter out entries for clientID.
	kept := entries[:0]
	for _, e := range entries {
		if e.ClientID != clientID {
			kept = append(kept, e)
		}
	}

	compactPath := w.path + ".compact"
	cf, err := os.OpenFile(compactPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("wal: open compact file: %w", err)
	}

	for _, e := range kept {
		payload, err := protocol.Encode(e)
		if err != nil {
			cf.Close()
			os.Remove(compactPath)
			return fmt.Errorf("wal: encode during compact: %w", err)
		}
		frame := makeFrame(payload)
		if _, err := cf.Write(frame); err != nil {
			cf.Close()
			os.Remove(compactPath)
			return fmt.Errorf("wal: write compact: %w", err)
		}
	}

	if err := cf.Sync(); err != nil {
		cf.Close()
		os.Remove(compactPath)
		return fmt.Errorf("wal: fsync compact: %w", err)
	}
	cf.Close()

	// Atomic rename over the original.
	if err := os.Rename(compactPath, w.path); err != nil {
		os.Remove(compactPath)
		return fmt.Errorf("wal: rename compact: %w", err)
	}

	// Fsync the parent directory so the rename survives a crash.
	if err := fsyncDir(w.dir); err != nil {
		return fmt.Errorf("wal: fsync dir after truncate: %w", err)
	}

	// Refresh the internal file handle — the old fd now points at the
	// unlinked inode (on Linux, rename replaces the target in-place but the
	// old fd still refers to the old file until closed).
	w.f.Close()
	nf, err := os.OpenFile(w.path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("wal: reopen after truncate: %w", err)
	}
	w.f = nf
	return nil
}

// Close closes the underlying file. After Close, all other methods return errors.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// --- internal helpers ---

const (
	frameLenSize = 4 // uint32 BE payload length
	frameCRCSize = 4 // uint32 CRC32-IEEE of payload
	frameHdrSize = frameLenSize + frameCRCSize

	// maxWALPayloadLen caps the per-frame alloc in replayFrom. The bound is
	// the WebSocket read limit (15 MiB, set in hub.go) — a WAL frame that
	// exceeds the wire ceiling cannot have been written by a legitimate client
	// and is therefore corrupt.
	maxWALPayloadLen = 15 << 20
)

// makeFrame encodes payload as [length u32 BE][crc u32 BE][payload].
func makeFrame(payload []byte) []byte {
	frame := make([]byte, frameHdrSize+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(payload))
	copy(frame[8:], payload)
	return frame
}

// replayFrom reads WAL frames from r until EOF, returning decoded entries.
// On CRC mismatch or short read it returns the entries decoded so far plus
// a *ReplayError. The caller must hold w.mu.
func replayFrom(r io.ReadSeeker) ([]WALEntry, error) {
	var entries []WALEntry
	hdr := make([]byte, frameHdrSize)

	for {
		offset, _ := r.(io.Seeker).Seek(0, io.SeekCurrent)

		_, err := io.ReadFull(r, hdr)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if err == io.ErrUnexpectedEOF {
				return entries, &ReplayError{
					Reason: "truncated frame header",
					Offset: offset,
				}
			}
			// Clean EOF — done.
			return entries, nil
		}
		if err != nil {
			return entries, &ReplayError{
				Reason: fmt.Sprintf("read header: %v", err),
				Offset: offset,
			}
		}

		payloadLen := binary.BigEndian.Uint32(hdr[0:4])
		expectedCRC := binary.BigEndian.Uint32(hdr[4:8])

		if payloadLen > maxWALPayloadLen {
			return entries, &ReplayError{
				Reason: fmt.Sprintf("payload length %d exceeds maximum %d (corrupt frame)", payloadLen, maxWALPayloadLen),
				Offset: offset,
			}
		}

		payload := make([]byte, payloadLen)
		_, err = io.ReadFull(r, payload)
		if err != nil {
			return entries, &ReplayError{
				Reason: fmt.Sprintf("truncated payload (want %d bytes): %v", payloadLen, err),
				Offset: offset,
			}
		}

		actualCRC := crc32.ChecksumIEEE(payload)
		if actualCRC != expectedCRC {
			return entries, &ReplayError{
				Reason: fmt.Sprintf("CRC mismatch (want %08x got %08x)", expectedCRC, actualCRC),
				Offset: offset,
			}
		}

		var entry WALEntry
		if err := protocol.Decode(payload, &entry); err != nil {
			return entries, &ReplayError{
				Reason: fmt.Sprintf("CBOR decode: %v", err),
				Offset: offset,
			}
		}

		entries = append(entries, entry)
	}
}

// fsyncDir delegates to fileutil.SyncDir — the WAL keeps an open file handle
// across many writes (unlike AtomicWrite's one-shot replace), so it still
// needs a way to durably commit a freshly-created directory entry.
func fsyncDir(dir string) error {
	return fileutil.SyncDir(dir)
}
