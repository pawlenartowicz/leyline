package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func gzTar(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestPublish_HappyPath(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	body := gzTar(t, map[string]string{"note.md": "hello\n"})
	req := httptest.NewRequest("POST", "/_leyline/api/v1/a/publish", body)
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	req.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Commit  string `json:"commit"`
		Written int    `json:"written"`
		Deleted int    `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Written != 1 || resp.Commit == "" {
		t.Fatalf("written=%d commit=%q", resp.Written, resp.Commit)
	}
}

func TestPublish_EditorForbidden(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	body := gzTar(t, map[string]string{"note.md": "x\n"})
	req := httptest.NewRequest("POST", "/_leyline/api/v1/a/publish", body)
	req.Header.Set("Authorization", "Bearer "+tokens["editor"])
	req.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403 (vault.admin gate)", w.Code)
	}
}

func TestPublish_EmptyRejected(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	body := gzTar(t, map[string]string{}) // zero entries
	req := httptest.NewRequest("POST", "/_leyline/api/v1/a/publish", body)
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	req.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (empty tarball)", w.Code)
	}
}

func TestPublish_WithTag(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	body := gzTar(t, map[string]string{"note.md": "v1\n"})
	req := httptest.NewRequest("POST", "/_leyline/api/v1/a/publish?tag=release-1", body)
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	req.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ref"] != "release-1" {
		t.Fatalf("ref=%v, want release-1", resp["ref"])
	}
}

func TestPublish_TagCollision(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	post := func(content string) *httptest.ResponseRecorder {
		body := gzTar(t, map[string]string{"note.md": content})
		req := httptest.NewRequest("POST", "/_leyline/api/v1/a/publish?tag=dup", body)
		req.Header.Set("Authorization", "Bearer "+tokens["admin"])
		req.Header.Set("Content-Type", "application/gzip")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := post("v1\n"); w.Code != http.StatusOK {
		t.Fatalf("first publish status=%d", w.Code)
	}
	if w := post("v2\n"); w.Code != http.StatusConflict {
		t.Fatalf("second publish status=%d, want 409 tag_exists", w.Code)
	}
}

func TestPublish_InvalidTagName(t *testing.T) {
	mux, _, tokens := newV1TestServer(t)
	body := gzTar(t, map[string]string{"note.md": "x\n"})
	req := httptest.NewRequest("POST", "/_leyline/api/v1/a/publish?tag=../evil", body)
	req.Header.Set("Authorization", "Bearer "+tokens["admin"])
	req.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (invalid tag name)", w.Code)
	}
}
