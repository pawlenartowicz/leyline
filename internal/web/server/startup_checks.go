package server

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

// vaultPopulated reports whether a vault root exists and holds at least one
// entry. An unpopulated vault (missing root or zero entries) is skipped by the
// New() loop — no deps built, no watcher — and serves the built-in fallback
// page (fallback.go) instead of a themed render. Note os.ReadDir counts
// .leyline/, so an empty-but-present root with a control-plane dir reads as
// populated; in practice a *missing* root is what triggers the fallback.
func vaultPopulated(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// reservedVaultLevel are URL segments reserved at the position immediately
// after the vault prefix. A vault containing a top-level entry matching any of
// these (or starting with '@') refuses to start.
var reservedVaultLevel = []string{"_theme", "_login", "_logout", "_panel", "_search"}

// CheckReservedSegments scans every vault's top-level directory and returns an
// error if any filename collides with a reserved URL segment.
func CheckReservedSegments(r *vault.Registry, log *slog.Logger) error {
	for _, v := range r.All() {
		entries, err := os.ReadDir(v.Root)
		if err != nil {
			// Unpopulated vault (missing root): nothing to collide with. The
			// New() loop skips it too and it serves the fallback page.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read vault root %q: %w", v.Root, err)
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, "@") {
				return fmt.Errorf("vault %q (%s) contains top-level entry %q which collides with reserved @-prefix", v.Prefix, v.Root, name)
			}
			for _, res := range reservedVaultLevel {
				if name == res {
					return fmt.Errorf("vault %q (%s) contains top-level entry %q which collides with reserved URL segment", v.Prefix, v.Root, name)
				}
			}
		}
		log.Info("vault startup check OK", "prefix", v.Prefix, "root", v.Root)
	}
	return nil
}

// CheckPrefixShadowing logs a WARN for each case where a non-root vault prefix
// "/<seg>/..." shadows a top-level entry "<seg>" inside another vault.
func CheckPrefixShadowing(r *vault.Registry, log *slog.Logger) error {
	all := r.All()
	for _, candidate := range all {
		if candidate.Prefix == "/" {
			continue
		}
		seg := strings.TrimPrefix(candidate.Prefix, "/")
		if i := strings.IndexByte(seg, '/'); i >= 0 {
			seg = seg[:i]
		}
		for _, other := range all {
			if other.Prefix == candidate.Prefix {
				continue
			}
			entries, err := os.ReadDir(other.Root)
			if err != nil {
				// Unpopulated vault (missing root): nothing to shadow.
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return fmt.Errorf("read vault root %q: %w", other.Root, err)
			}
			for _, e := range entries {
				if e.Name() == seg {
					log.Warn("vault prefix shadows directory in another vault",
						"prefix", candidate.Prefix,
						"shadow_dir", seg,
						"shadowed_in_vault", other.Prefix,
						"shadowed_path", other.Root,
						"hint", fmt.Sprintf("files at %s/%s/** are not reachable via web (vault %q owns that URL space)", other.Root, seg, candidate.Prefix),
					)
				}
			}
		}
	}
	return nil
}
