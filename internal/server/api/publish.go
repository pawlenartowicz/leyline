package api

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/protocol/pathutil"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// Publish upload limits. The compressed cap is enforced by MaxBytesReader at the
// route (see RegisterRoutes); these two bound the *decompressed* stream so a
// gzip bomb can't exhaust memory while untarring. Generous for a docs/site vault;
// the authoritative per-vault ceiling is vault_limits (enforced in hub.Publish).
const (
	publishMaxCompressedBytes   = 100 << 20 // 100 MiB request body
	publishMaxDecompressedBytes = 512 << 20
	publishMaxEntries           = 50_000
)

func (v *V1API) handlePublish(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	vs := vsFromContext(r)

	desired, err := untarToMap(r.Body)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	allowEmpty := r.URL.Query().Get("allow_empty") == "true"
	res, err := v.hub.Publish(vs, desired, callerName(r), allowEmpty)
	if err != nil {
		if errors.Is(err, hub.ErrEmptyPublish) || errors.Is(err, hub.ErrInvalidContent) {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := map[string]any{
		"commit":  res.Commit,
		"written": res.Written,
		"deleted": res.Deleted,
	}

	if tag := r.URL.Query().Get("tag"); tag != "" {
		ref, status, msg := v.tagPublishCommit(r, vs, tag, res.Commit)
		if status != 0 {
			// Content commit already landed (and broadcast); the tag is a
			// best-effort add-on. Surface the publish result alongside the tag
			// error so the caller does NOT retry the whole publish (double-apply).
			out["tag_error"] = msg
			writeJSON(w, status, out)
			return
		}
		out["ref"] = ref
	}

	writeJSON(w, http.StatusOK, out)
}

// untarToMap stream-reads a gzipped tarball into path → content, excluding
// directories and every hidden tree (mirrors DiskStore.ListFiles), bounding
// cumulative decompressed size and entry count to defuse a gzip bomb.
func untarToMap(body io.Reader) (map[string][]byte, error) {
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, errors.New("body is not valid gzip")
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	desired := make(map[string][]byte)
	var total int64
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("malformed tar stream")
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // skip dirs, symlinks, etc.
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		// Mirror DiskStore.ListFiles: skip every hidden tree (.git, .obsidian,
		// .leyline, dotfiles). The diff baseline never surfaces these, so
		// including them would desync the overwrite and trip ValidatePath's
		// hidden-component rejection (a hard 400 on a normal `tar czf - .`).
		if name == "" || pathutil.IsHidden(name) {
			continue
		}
		entries++
		if entries > publishMaxEntries {
			return nil, errors.New("too many files in tarball")
		}
		// Bound by actual decompressed bytes, not hdr.Size (a bomb can understate
		// it). LimitReader caps each entry at the remaining budget; reading
		// remaining+1 lets the post-read check detect an overrun.
		remaining := publishMaxDecompressedBytes - total
		content, err := io.ReadAll(io.LimitReader(tr, remaining+1))
		if err != nil {
			return nil, errors.New("error reading tar entry")
		}
		total += int64(len(content))
		if total > publishMaxDecompressedBytes {
			return nil, errors.New("tarball too large when decompressed")
		}
		desired[name] = content
	}
	return desired, nil
}

// tagPublishCommit tags the explicit publish commit (race-free — the sha is
// pinned). Returns (ref, status, body); status == 0 means "no error, use ref".
func (v *V1API) tagPublishCommit(r *http.Request, vs *hub.VaultState, tag, commit string) (string, int, any) {
	// ?tag= is an extra privileged sub-action: the route gates vault.admin, but
	// tagging requires history.tag (a custom role could hold one without the
	// other). Check it explicitly only when ?tag= is present.
	// Mirror vaultAuth's SWA fallthrough: a server-wide admin satisfies the
	// route's vault.admin gate transitively, so honor it for history.tag too —
	// otherwise an SWA whose vault-local role lacks history.tag would publish
	// fine but 403 on the tag step.
	if set, ok := capsFromContext(r); (!ok || !set.Has(caps.HistoryTag)) && !v.a.authorizedServerWide(r) {
		return "", http.StatusForbidden, map[string]string{"error": "capability required: history.tag"}
	}
	if !isValidTagName(tag) {
		return "", http.StatusBadRequest, map[string]string{"error": "invalid tag name"}
	}
	res := vs.SubmitTag(tag, commit, callerName(r))
	if errors.Is(res.Err, storage.ErrTagExists) {
		cur := ""
		if tags, _ := vs.Git().ListTags(tag); len(tags) > 0 {
			for _, t := range tags {
				if t.Name == tag {
					cur = t.Commit
					break
				}
			}
		}
		return "", http.StatusConflict, tagErrResp{Error: "tag_exists", Name: tag, CurrentCommit: cur}
	}
	if res.Err != nil {
		return "", http.StatusBadRequest, map[string]string{"error": res.Err.Error()}
	}
	return res.Ref, 0, nil
}
