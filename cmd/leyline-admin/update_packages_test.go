package main

import (
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/pawlenartowicz/leyline/protocol/version"
)

func TestDetectPackageManager_PicksFirstOnPath(t *testing.T) {
	// Only apt resolves → expect the deb manager.
	look := func(name string) (string, error) {
		if name == "apt" {
			return "/usr/bin/apt", nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	pm, err := detectPackageManager(look)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if pm.bin != "apt" || pm.installCmd != "install" || pm.ext != "deb" {
		t.Fatalf("got %+v, want apt/install/deb", pm)
	}
}

func TestDetectPackageManager_NoneFound(t *testing.T) {
	look := func(name string) (string, error) {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	if _, err := detectPackageManager(look); err == nil {
		t.Fatal("expected error when no package manager on PATH")
	}
}

func TestAssetFileName(t *testing.T) {
	got := assetFileName("leyline-server", "0.1.1", "amd64", "rpm")
	if got != "leyline-server_0.1.1_amd64.rpm" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeTagThenCompare(t *testing.T) {
	latest := normalizeTag("v0.1.1") // CompareVersions does not strip the v itself
	if version.CompareVersions("0.1.0", latest) >= 0 {
		t.Fatalf("0.1.0 should be older than %q", latest)
	}
	if version.CompareVersions("0.1.1", latest) != 0 {
		t.Fatalf("0.1.1 should equal %q", latest)
	}
}

func TestPickAsset(t *testing.T) {
	rel := ghRelease{Assets: []ghAsset{
		{Name: "leyline-server_0.1.1_arm64.rpm", DownloadURL: "u-arm"},
		{Name: "leyline-server_0.1.1_amd64.rpm", DownloadURL: "u-amd"},
	}}
	url, ok := pickAsset(rel, "leyline-server_0.1.1_amd64.rpm")
	if !ok || url != "u-amd" {
		t.Fatalf("got %q ok=%v", url, ok)
	}
	if _, ok := pickAsset(rel, "leyline-server_0.1.1_amd64.deb"); ok {
		t.Fatal("expected miss for absent asset")
	}
}

func TestLatestRelease_ParsesTagAndAssets(t *testing.T) {
	body := `{"tag_name":"v0.1.1","assets":[{"name":"a.rpm","browser_download_url":"http://x/a.rpm"}]}`
	get := func(url string) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	rel, err := latestRelease(get, "pawlenartowicz/leyline")
	if err != nil {
		t.Fatalf("latestRelease: %v", err)
	}
	if rel.TagName != "v0.1.1" || len(rel.Assets) != 1 || rel.Assets[0].DownloadURL != "http://x/a.rpm" {
		t.Fatalf("bad parse: %+v", rel)
	}
}
