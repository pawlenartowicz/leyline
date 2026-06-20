package version

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Action mirrors urlx.Action for the tag-resolution path. We can't import
// urlx here (it would create a cycle with the dispatch layer), so the
// enum is duplicated. The page handler maps between them when dispatching
// the request.
type Action int

const (
	// ActionNotFound: no file resolves at this tag.
	ActionNotFound Action = iota
	// ActionServe: serve the file at RelPath (the bytes are read via
	// ReadBlob).
	ActionServe
	// ActionRedirect: 302 to Redirect (vault-relative; the page handler
	// re-applies the vault prefix and the `@<tag>` selector).
	ActionRedirect
)

// Disposition is the tag-resolution counterpart to urlx.Disposition.
type Disposition struct {
	Action   Action
	RelPath  string
	Redirect string
}

// ResolveAtTag is the tag-scoped analogue of urlx.ResolvePretty. Same
// canonicalisation rules (extensionless `/notes/foo` resolves to
// `notes/foo.md`; `.md` URLs redirect to extensionless; trailing-slash
// directories serve `index.md`/`README.md`), but it operates against the
// VaultIndex's snapshot of the tag's tree instead of the live filesystem.
func ResolveAtTag(idx *VaultIndex, tag, sub string) Disposition {
	if idx == nil {
		return Disposition{Action: ActionNotFound}
	}
	if sub == "" || strings.HasSuffix(sub, "/") {
		dir := strings.TrimSuffix(sub, "/")
		for _, candidate := range []string{"index.md", "README.md"} {
			rel := candidate
			if dir != "" {
				rel = dir + "/" + candidate
			}
			if idx.HasFile(tag, rel) {
				return Disposition{Action: ActionServe, RelPath: rel}
			}
		}
		return Disposition{Action: ActionNotFound}
	}

	if strings.HasSuffix(sub, ".md") {
		return Disposition{Action: ActionRedirect, Redirect: strings.TrimSuffix(sub, ".md")}
	}

	// Has an extension (after the last `/`): serve as-is when present.
	if hasExtension(sub) {
		if idx.HasFile(tag, sub) {
			return Disposition{Action: ActionServe, RelPath: sub}
		}
		return Disposition{Action: ActionNotFound}
	}

	mdRel := sub + ".md"
	if idx.HasFile(tag, mdRel) {
		return Disposition{Action: ActionServe, RelPath: mdRel}
	}
	if dirExistsAtTag(idx, tag, sub) {
		return Disposition{Action: ActionRedirect, Redirect: sub + "/"}
	}
	return Disposition{Action: ActionNotFound}
}

func hasExtension(p string) bool {
	base := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		base = p[i+1:]
	}
	return strings.Contains(base, ".")
}

func dirExistsAtTag(idx *VaultIndex, tag, dir string) bool {
	prefix := dir + "/"
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for path, fh := range idx.files {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		// Tag bound check inlined to avoid re-locking.
		if !hasFileLocked(idx.tags, fh, tag) {
			continue
		}
		return true
	}
	return false
}

func hasFileLocked(tags []string, fh *FileHistory, tag string) bool {
	tagIdx := indexOf(tags, tag)
	if tagIdx < 0 {
		return false
	}
	firstIdx := indexOf(tags, fh.FirstTag)
	if firstIdx < 0 || tagIdx > firstIdx {
		return false
	}
	if fh.LastTag != "" {
		lastIdx := indexOf(tags, fh.LastTag)
		if lastIdx >= 0 && tagIdx < lastIdx {
			return false
		}
	}
	return true
}

// ReadAndHashAtTag is the cache-friendly read for tag content: returns
// the raw bytes and their hex SHA-256 (matching cache.ReadAndHash's
// fingerprint shape). The hash is over blob *bytes*, not the git blob
// OID — that way identical content shared between a tag and the
// filesystem collides on the same render-cache key.
func ReadAndHashAtTag(vaultRoot, tag, path string) ([]byte, string, error) {
	b, err := ReadBlob(vaultRoot, tag, path)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
