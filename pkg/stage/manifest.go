package stage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// ManifestEntry is one path's state at the client's current base.
type ManifestEntry struct {
	Path    string        `json:"path"`
	Hash    protocol.Hash `json:"hash,omitempty"`
	Binary  bool          `json:"binary,omitempty"`
	Deleted bool          `json:"deleted,omitempty"`
}

// Manifest is an append-only on-disk log of ManifestEntry rows (one JSON
// object per line). Latest entry per path wins; a Deleted=true row is a
// tombstone (Get returns ok=false). In memory we keep the live set in
// `live` and the total row count in `rows` so Compact can fire at 2× live.
//
// The manifest is the watcher's source of truth for "what hash does the
// daemon think this path has right now?" Engine.applyDecision writes the
// manifest entry before the disk write so the watcher's subsequent fsnotify
// event finds a matching hash and suppresses the bootstrap-echo op — without
// this ordering, every catchup-applied write echoes back to the server.
type Manifest struct {
	mu   sync.Mutex
	path string
	live map[string]ManifestEntry
	rows int
	w    *os.File
	bufw *bufio.Writer
}

func OpenManifest(path string) (*Manifest, error) {
	m := &Manifest{path: path, live: make(map[string]ManifestEntry)}
	// Replay existing log.
	if f, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(f)
		// Allow long lines for large hash + path entries.
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			var e ManifestEntry
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				f.Close()
				return nil, fmt.Errorf("parse manifest: %w", err)
			}
			m.rows++
			if e.Deleted {
				delete(m.live, e.Path)
			} else {
				m.live[e.Path] = e
			}
		}
		// A row over the scanner cap makes Scan() return false without
		// erroring; fail loud rather than silently truncating the replay.
		if err := scanner.Err(); err != nil {
			f.Close()
			return nil, fmt.Errorf("read manifest: %w", err)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Open for append.
	w, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	m.w = w
	m.bufw = bufio.NewWriter(w)
	return m, nil
}

func (m *Manifest) Get(path string) (ManifestEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live[path]
	return e, ok
}

// IsEmpty distinguishes a first-run manifest (no log file or empty file) from
// a known-empty vault (log file exists after compaction wiped a fully-tombstoned
// set). The bootstrap-scan gate in daemon.Run and oneshot.runOneShotSession
// uses this to decide whether to fold pre-existing files into the staged log
// before Hello — without it, applyBootstrap clobbers offline edits.
func (m *Manifest) IsEmpty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.live) == 0 && m.rows == 0
}

func (m *Manifest) Put(path string, e ManifestEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.Path = path
	e.Deleted = false
	return m.append(e)
}

func (m *Manifest) Delete(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.append(ManifestEntry{Path: path, Deleted: true})
}

// Range iterates the live set in unspecified order.
func (m *Manifest) Range(f func(path string, e ManifestEntry) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for p, e := range m.live {
		if !f(p, e) {
			return
		}
	}
}

func (m *Manifest) append(e ManifestEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := m.bufw.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := m.bufw.Flush(); err != nil {
		return err
	}
	// Fsync to match StagedLog/AckedLog's append-durability contract —
	// a power loss must not leave the manifest behind the on-disk state
	// it describes (reconcile would re-emit already-applied files with
	// stale PreHashes).
	if err := m.w.Sync(); err != nil {
		return err
	}
	m.rows++
	if e.Deleted {
		delete(m.live, e.Path)
	} else {
		m.live[e.Path] = e
	}
	// Opportunistic compaction at 2× live.
	if m.rows > 2*len(m.live) && len(m.live) > 0 {
		return m.compactLocked()
	}
	return nil
}

func (m *Manifest) Compact() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.compactLocked()
}

func (m *Manifest) compactLocked() error {
	if m.bufw != nil {
		_ = m.bufw.Flush()
	}
	if m.w != nil {
		_ = m.w.Close()
		m.w = nil
		m.bufw = nil
	}
	var buf bytes.Buffer
	for _, e := range m.live {
		data, _ := json.Marshal(e)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := fileutil.AtomicWrite(m.path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	// Reopen for append.
	w, err := os.OpenFile(m.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	m.w = w
	m.bufw = bufio.NewWriter(w)
	m.rows = len(m.live)
	return nil
}

func (m *Manifest) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bufw != nil {
		_ = m.bufw.Flush()
	}
	if m.w != nil {
		err := m.w.Close()
		m.w = nil
		return err
	}
	return nil
}
