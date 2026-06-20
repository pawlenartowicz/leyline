package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	"github.com/pawlenartowicz/leyline/internal/server/httpx"
	"github.com/pawlenartowicz/leyline/internal/server/hub"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// V1API hosts the /_leyline/api/v1/{vault}/... endpoints introduced in Tier 3.
// Reuses AdminAPI's vaultAuth middleware so the bearer-key + capability gate
// is identical to existing admin routes.
type V1API struct {
	hub *hub.Hub
	a   *AdminAPI
}

func NewV1API(h *hub.Hub) *V1API {
	return &V1API{hub: h, a: NewAdminAPI(h)}
}

func (v *V1API) RegisterRoutes(mux *http.ServeMux) {
	m := httpx.Mounter{
		Mux:    mux,
		Prefix: "/_leyline/api/v1/{vault}",
		Auth: func(c any) func(http.Handler) http.Handler {
			return v.a.vaultAuth(c.(caps.Capability), 1<<20)
		},
		DefaultTimeout: 30 * time.Second,
	}
	m.Handle("POST", "/tag", caps.HistoryTag, v.handleTag)
	m.Handle("POST", "/review", caps.HistoryTag, v.handleReview)
	m.Handle("POST", "/revert", caps.HistoryRevert, v.handleRevert)
	m.Handle("POST", "/restore", caps.HistoryRevert, v.handleRestore)
	m.Handle("GET", "/log", caps.SyncPull, v.handleLog)
	m.Handle("GET", "/diff", caps.SyncPull, v.handleDiff)
	m.Handle("GET", "/tags", caps.SyncPull, v.handleTags)
	m.Handle("DELETE", "/tag/{name}", caps.HistoryTag, v.handleDeleteTag)
	m.Handle("DELETE", "/tags", caps.HistoryTag, v.handleDeleteTagsByCommit)

	// /publish takes a whole-vault tarball — far larger than the 1 MiB default
	// and longer-running than 30s. Its own Mounter raises both.
	mPub := httpx.Mounter{
		Mux:    mux,
		Prefix: "/_leyline/api/v1/{vault}",
		Auth: func(c any) func(http.Handler) http.Handler {
			return v.a.vaultAuth(c.(caps.Capability), publishMaxCompressedBytes)
		},
		DefaultTimeout: 5 * time.Minute,
	}
	mPub.Handle("POST", "/publish", caps.VaultAdmin, v.handlePublish)
}

type tagReq struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

type tagResp struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

type tagErrResp struct {
	Error         string `json:"error"`
	Name          string `json:"name"`
	CurrentCommit string `json:"current_commit"`
}

func (v *V1API) rateLimited(w http.ResponseWriter, r *http.Request) bool {
	vs := vsFromContext(r)
	limiter := vs.GetPushLimiter(callerName(r), v.hub.GetCfg().Sync.PushRateLimit)
	if limiter.Exceeded() {
		writeJSONError(w, "rate limit exceeded", http.StatusTooManyRequests)
		return true
	}
	limiter.Record()
	return false
}

func (v *V1API) handleTag(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	vs := vsFromContext(r)
	var req tagReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !isValidTagName(req.Name) {
		writeJSONError(w, "invalid tag name", http.StatusBadRequest)
		return
	}
	res := vs.SubmitTag(req.Name, req.Commit, callerName(r))
	if errors.Is(res.Err, storage.ErrTagExists) {
		tags, _ := vs.Git().ListTags(req.Name)
		cur := ""
		for _, t := range tags {
			if t.Name == req.Name {
				cur = t.Commit
				break
			}
		}
		writeJSON(w, http.StatusConflict, tagErrResp{
			Error: "tag_exists", Name: req.Name, CurrentCommit: cur,
		})
		return
	}
	if res.Err != nil {
		writeJSONError(w, res.Err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, tagResp{Ref: res.Ref, Commit: res.SHA})
}

func (v *V1API) handleReview(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	vs := vsFromContext(r)
	var req struct {
		Commit string `json:"commit"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	for attempt := 0; attempt < 5; attempt++ {
		ts := time.Now().UTC().Add(time.Duration(attempt) * time.Second)
		res := vs.SubmitReview(req.Commit, callerName(r), ts)
		if res.Err == nil {
			writeJSON(w, http.StatusOK, tagResp{Ref: res.Ref, Commit: res.SHA})
			return
		}
		if !errors.Is(res.Err, storage.ErrTagExists) {
			writeJSONError(w, res.Err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSONError(w, "review name collision after 5 retries", http.StatusTooManyRequests)
}

func (v *V1API) handleRevert(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	vs := vsFromContext(r)
	var req struct {
		Commit string `json:"commit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Commit == "" {
		writeJSONError(w, "commit required", http.StatusBadRequest)
		return
	}
	res := vs.SubmitRevert(req.Commit, callerName(r))
	if len(res.Conflicts) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"paths": res.Conflicts})
		return
	}
	if res.Err != nil {
		writeJSONError(w, res.Err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"commit": res.SHA})
}

func (v *V1API) handleRestore(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	vs := vsFromContext(r)
	var req struct {
		Commit string `json:"commit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Commit == "" {
		writeJSONError(w, "commit required", http.StatusBadRequest)
		return
	}
	res := vs.SubmitRestore(req.Commit, callerName(r))
	if res.Err != nil {
		writeJSONError(w, res.Err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"commit": res.SHA})
}

func (v *V1API) handleLog(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	// Tier 3 read trigger: flush any pending stages so the log reflects every
	// op the server has ack'd, not just what landed in git via the timer.
	if err := v.hub.FlushAllStages(vs); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	limit := parseIntDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}
	before := r.URL.Query().Get("before")
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = "HEAD"
	}
	since, _ := parseDurationLoose(r.URL.Query().Get("since"))
	entries, err := vs.Git().Log(ref, limit, before, since)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, formatLogEntries(entries))
}

func (v *V1API) handleDiff(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	// Tier 3 read trigger — see handleLog for rationale.
	if err := v.hub.FlushAllStages(vs); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if to == "" {
		to = "HEAD"
	}
	if from == "" {
		tags, _ := vs.Git().ListTags(protocol.ReviewTagPrefix)
		if len(tags) > 0 {
			from = tags[len(tags)-1].Name
		} else {
			walk, _ := vs.Git().Log("HEAD", 50, "", 0)
			if len(walk) == 0 {
				writeJSON(w, http.StatusOK, []storage.DiffEntry{})
				return
			}
			from = walk[len(walk)-1].SHA
		}
	}
	entries, err := vs.Git().Diff(from, to)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

type tagRefInfo struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

type tagDeleteResp struct {
	Removed []tagRefInfo `json:"removed"`
}

func (v *V1API) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	name := r.PathValue("name")
	if !isValidTagName(name) {
		writeJSONError(w, "invalid tag name", http.StatusBadRequest)
		return
	}
	vs := vsFromContext(r)
	res := vs.SubmitDeleteTag(name, callerName(r))
	if errors.Is(res.Err, storage.ErrTagNotFound) {
		writeJSONError(w, "not_found", http.StatusNotFound)
		return
	}
	if res.Err != nil {
		writeJSONError(w, res.Err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, tagDeleteResp{Removed: toTagRefInfos(res.Removed)})
}

func (v *V1API) handleDeleteTagsByCommit(w http.ResponseWriter, r *http.Request) {
	if v.rateLimited(w, r) {
		return
	}
	commit := r.URL.Query().Get("commit")
	if commit == "" {
		writeJSONError(w, "commit required", http.StatusBadRequest)
		return
	}
	vs := vsFromContext(r)
	res := vs.SubmitDeleteTagsByCommit(commit, callerName(r))
	if res.Err != nil {
		writeJSONError(w, res.Err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, tagDeleteResp{Removed: toTagRefInfos(res.Removed)})
}

func toTagRefInfos(in []storage.TagInfo) []tagRefInfo {
	out := make([]tagRefInfo, len(in))
	for i, t := range in {
		out[i] = tagRefInfo{Name: t.Name, Commit: t.Commit}
	}
	return out
}

type taggedKind struct {
	Name      string    `json:"name"`
	Commit    string    `json:"commit"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

func (v *V1API) handleTags(w http.ResponseWriter, r *http.Request) {
	vs := vsFromContext(r)
	// Tier 3 read trigger — flush pending stages so any tag created from a
	// staged-but-uncommitted op is visible.
	if err := v.hub.FlushAllStages(vs); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	tags, err := vs.Git().ListTags(prefix)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]taggedKind, 0, len(tags))
	for _, t := range tags {
		k := "tag"
		if strings.HasPrefix(t.Name, protocol.ReviewTagPrefix) {
			k = "review"
		}
		out = append(out, taggedKind{
			Name: t.Name, Commit: t.Commit, Kind: k, CreatedAt: t.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

var tagNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func isValidTagName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	if strings.HasPrefix(s, "-") {
		return false
	}
	return tagNameRE.MatchString(s)
}

// parseDurationLoose extends time.ParseDuration with `d` (24h) and `w` (7d)
// suffixes. Empty input → 0.
func parseDurationLoose(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "w") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func formatLogEntries(in []storage.LogEntry) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, e := range in {
		out[i] = map[string]any{
			"sha":     e.SHA,
			"author":  e.Author,
			"time":    e.Time,
			"message": e.Message,
			"files":   e.Files,
		}
	}
	return out
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
