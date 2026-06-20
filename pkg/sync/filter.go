package sync

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/pawlenartowicz/leyline/protocol/pathutil"
)

// Filter is the sole admission test for both walk and event paths.
// The watcher (event path) and ReconcileWorkingTree (walk path) both
// consult Excluded — no other hardcoded `.leyline/` or hidden-file rules
// live outside this file.
//
// Four hardcoded carve-outs always fire, regardless of AllowControlPlane or
// any leylineignore pattern, because each is an OS-level / correctness
// concern, not a policy decision:
//
//   - ".leyline-tmp-*" — leyline's own atomic-write artifacts (fileutil.AtomicWrite).
//   - ".git/"          — would collide with the server-side git repo.
//   - "LEYLINE_CONFIRM_NEEDED.txt" — bulk-change safety marker written before
//     a large destructive operation; must never be synced or it would propagate
//     the warning to other clients.
//   - ".leyline/trash/" — inbound-delete staging area; never synced so that
//     deleted files can be recovered locally before permanent removal.

// FilterOpts configures Filter behavior. The caller (the daemon) supplies the
// callbacks because pkg/sync cannot touch the filesystem.
type FilterOpts struct {
	// AllowControlPlane lets paths under ".leyline/" through (admins only).
	//
	// This is a UX optimization, NOT a security boundary. The server is the
	// authoritative admin gate on both directions: it rejects a non-admin's
	// vaultconfig push with permission-denied and withholds the vaultconfig
	// subset from non-admin recipients on catchup/bootstrap/broadcast. Because
	// the daemon flips this flag on exactly for vault.admin holders (after
	// AuthOK), it keeps a non-admin from staging vaultconfig pushes the server
	// would only reject — which would trip the client's own failed-push
	// breaker for no benefit. It mirrors the server gate; it does not enforce
	// it.
	AllowControlPlane bool
	// IsSymlink reports whether path is a symlink. Optional: nil means "no symlinks".
	IsSymlink func(path string) bool
	// IsInsideNestedVault reports whether path lives under a nested vault
	// (any directory containing its own .leyline/). Optional.
	IsInsideNestedVault func(path string) bool
}

// Filter combines built-in exclusion rules with user-supplied gitignore patterns.
//
// AllowControlPlane is held as an atomic so the daemon can flip it after auth once
// the server-confirmed capability set is known. All other opts are set at
// construction and treated as immutable.
type Filter struct {
	patterns     *gitignore.GitIgnore
	opts         FilterOpts
	allowControlPlane atomic.Bool
}

// NewFilter parses .leyline/leylineignore content from r and returns a Filter.
// An empty reader is allowed; only the built-in rules will apply.
func NewFilter(r io.Reader, opts FilterOpts) (*Filter, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ignore file: %w", err)
	}
	gi := gitignore.CompileIgnoreLines(lines...)
	f := &Filter{patterns: gi, opts: opts}
	f.allowControlPlane.Store(opts.AllowControlPlane)
	return f, nil
}

// SetAllowControlPlane updates the .leyline/ admission flag at runtime. The daemon
// calls this after MsgAuthOK reveals whether the session holds vault.admin.
func (f *Filter) SetAllowControlPlane(allow bool) {
	f.allowControlPlane.Store(allow)
}

// Excluded reports whether path must NOT be synced. Built-in rules are checked
// first and cannot be overridden by .leyline/leylineignore patterns.
// Four hardcoded carve-outs always fire regardless of policy: see the type
// doc comment for the list.
func (f *Filter) Excluded(path string) bool {
	if isHardcodedCarveOut(path) {
		return true
	}
	if f.opts.IsSymlink != nil && f.opts.IsSymlink(path) {
		return true
	}
	if f.opts.IsInsideNestedVault != nil && f.opts.IsInsideNestedVault(path) {
		return true
	}
	if pathutil.IsHidden(path) {
		// .leyline/ is the only hidden prefix admins are allowed to see.
		if f.allowControlPlane.Load() && pathutil.IsControlPlanePath(path) {
			// fall through to user patterns
		} else {
			return true
		}
	}
	return f.patterns != nil && f.patterns.MatchesPath(path)
}

// isHardcodedCarveOut tests the four built-in carve-outs that always fire
// regardless of AllowControlPlane or user ignore patterns.
func isHardcodedCarveOut(path string) bool {
	if path == "" {
		return false
	}
	// Atomic-write artifacts surface as ".leyline-tmp-*" siblings of the
	// target. The fsnotify Create+Write+Rename+Remove sequence on these
	// would otherwise produce ops with no manifest entry.
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".leyline-tmp-") {
			return true
		}
	}
	if path == ".git" || strings.HasPrefix(path, ".git/") {
		return true
	}
	if path == "LEYLINE_CONFIRM_NEEDED.txt" {
		return true
	}
	if path == ".leyline/trash" || strings.HasPrefix(path, ".leyline/trash/") {
		return true
	}
	return false
}
