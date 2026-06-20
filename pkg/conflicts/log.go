package conflicts

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one row in conflicts.log.
type Entry struct {
	TS      string `json:"ts"`
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Format  string `json:"format,omitempty"`
	Sidecar string `json:"sidecar,omitempty"`
	Origin  string `json:"origin"`
}

const rotateThreshold = 1 * 1024 * 1024 // rotate log at 1 MB
const maxRotations = 2                  // retain last two rotations

// Log is an append-only JSONL conflict event log at .leyline/backend/conflicts.log.
// Rotated at rotateThreshold; at most maxRotations old files are kept.
type Log struct {
	mu   sync.Mutex
	path string
	w    *os.File
	bw   *bufio.Writer
	size int64
}

func OpenLog(path string) (*Log, error) {
	l := &Log{path: path}
	info, err := os.Stat(path)
	if err == nil {
		l.size = info.Size()
	}
	w, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l.w = w
	l.bw = bufio.NewWriter(w)
	return l, nil
}

func (l *Log) Append(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := l.bw.Write(data); err != nil {
		return err
	}
	if err := l.bw.Flush(); err != nil {
		return err
	}
	l.size += int64(len(data))
	if l.size >= rotateThreshold {
		return l.rotateLocked()
	}
	return nil
}

// rotateLocked renames the current log to a timestamped backup, trims old
// rotations to maxRotations, and reopens for append. Callers must hold l.mu.
func (l *Log) rotateLocked() error {
	if l.bw != nil {
		_ = l.bw.Flush()
	}
	if l.w != nil {
		_ = l.w.Close()
	}
	rotated := l.path + "." + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(l.path, rotated); err != nil {
		return err
	}
	// Trim old rotations.
	dir := filepath.Dir(l.path)
	entries, err := os.ReadDir(dir)
	if err == nil {
		var rotations []string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "conflicts.log.") {
				rotations = append(rotations, e.Name())
			}
		}
		sort.Strings(rotations)
		for i := 0; i < len(rotations)-maxRotations; i++ {
			os.Remove(fmt.Sprintf("%s/%s", dir, rotations[i]))
		}
	}
	// Reopen.
	w, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.w = w
	l.bw = bufio.NewWriter(w)
	l.size = 0
	return nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bw != nil {
		_ = l.bw.Flush()
	}
	if l.w != nil {
		err := l.w.Close()
		l.w = nil
		return err
	}
	return nil
}
