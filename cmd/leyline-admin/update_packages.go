package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pawlenartowicz/leyline/protocol/version"
	"github.com/pawlenartowicz/leyline/internal/buildinfo"
)

// pkgManager is the detected host package manager: which binary to invoke, the
// install verb, and the release-asset extension to match.
type pkgManager struct {
	bin        string // dnf / apt / apk
	installCmd string // install / add
	ext        string // rpm / deb / apk
}

// detectPackageManager returns the first supported manager found on PATH.
// Order is rpm-family, deb-family, apk.
func detectPackageManager(lookPath func(string) (string, error)) (pkgManager, error) {
	for _, pm := range []pkgManager{
		{"dnf", "install", "rpm"},
		{"apt", "install", "deb"},
		{"apk", "add", "apk"},
	} {
		if _, err := lookPath(pm.bin); err == nil {
			return pm, nil
		}
	}
	return pkgManager{}, fmt.Errorf("no supported package manager (dnf/apt/apk) on PATH")
}

// ghAsset / ghRelease model the slice of the GitHub releases/latest response we use.
type ghAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ghGet issues a GitHub API/asset GET with the headers GitHub expects (a missing
// User-Agent is rejected with 403).
func ghGet(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "leyline-admin")
	req.Header.Set("Accept", "application/vnd.github+json")
	return httpClient.Do(req)
}

// latestRelease fetches releases/latest for owner/repo.
func latestRelease(get func(string) (*http.Response, error), repo string) (ghRelease, error) {
	resp, err := get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return ghRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ghRelease{}, fmt.Errorf("GitHub API %s: %s", repo, resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return ghRelease{}, err
	}
	return rel, nil
}

// normalizeTag strips the leading v that release tags carry; CompareVersions
// does not do this itself.
func normalizeTag(tag string) string { return strings.TrimPrefix(tag, "v") }

// assetFileName reproduces the nfpm file_name_template:
// <pkgName>_<version>_<goarch>.<ext>.
func assetFileName(pkgName, ver, goarch, ext string) string {
	return fmt.Sprintf("%s_%s_%s.%s", pkgName, ver, goarch, ext)
}

// pickAsset returns the download URL of the release asset with the exact name.
func pickAsset(rel ghRelease, name string) (string, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a.DownloadURL, true
		}
	}
	return "", false
}

// downloadAsset streams url to dst (a temp path).
func downloadAsset(get func(string) (*http.Response, error), url, dst string) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// component describes one updatable package: where its installed version comes
// from and which repo/package name to match release assets against.
type component struct {
	label   string
	pkgName string
	repo    string
	// installed returns the installed version and ok=false when the component
	// is not present on this box (e.g. the web binary is absent).
	installed func(lookPath func(string) (string, error)) (string, bool)
}

// componentsForUpdate is the fixed set update checks: the server bundle (this
// binary's own buildinfo) and the web reader (probed via `leyline-web --version`).
func componentsForUpdate() []component {
	return []component{
		{
			label:   "leyline-server",
			pkgName: "leyline-server",
			repo:    "pawlenartowicz/leyline",
			installed: func(func(string) (string, error)) (string, bool) {
				return buildinfo.Value, true
			},
		},
		{
			label:   "leyline-web",
			pkgName: "leyline-web",
			repo:    "pawlenartowicz/leyline",
			installed: func(lookPath func(string) (string, error)) (string, bool) {
				path, err := lookPath("leyline-web")
				if err != nil {
					return "", false
				}
				out, err := exec.Command(path, "--version").Output()
				if err != nil {
					return "", false
				}
				return strings.TrimSpace(string(out)), true
			},
		},
	}
}

// runUpdate checks both packaged components against their latest GitHub release,
// downloads any that are behind to a temp file, and prints the exact
// `sudo <pm> install <path>` line. It NEVER installs — fetch + instruct only —
// so it cannot fight the package manager that owns the files. Per-component
// network/asset errors are non-fatal: report on that line, keep checking the
// other component, exit non-zero if any check errored. No flags.
func runUpdate(_ []string, opts runOpts) int {
	lookPath := exec.LookPath
	pm, err := detectPackageManager(lookPath)
	if err != nil {
		fmt.Fprintln(opts.Stderr, "update:", err)
		return 1
	}

	exitCode := 0
	var downloaded []string // .deb/.rpm paths to install in one command
	for _, c := range componentsForUpdate() {
		installedVer, ok := c.installed(lookPath)
		if !ok {
			fmt.Fprintf(opts.Stdout, "%-15s not found (skipped)\n", c.label)
			continue
		}
		rel, err := latestRelease(ghGet, c.repo)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%-15s error: %v\n", c.label, err)
			exitCode = 1
			continue
		}
		latest := normalizeTag(rel.TagName)
		if version.CompareVersions(installedVer, latest) >= 0 {
			fmt.Fprintf(opts.Stdout, "%-15s up to date\n", c.label)
			continue
		}
		name := assetFileName(c.pkgName, latest, runtime.GOARCH, pm.ext)
		url, found := pickAsset(rel, name)
		if !found {
			fmt.Fprintf(opts.Stdout, "%-15s %s → %s   no %s asset for %s\n",
				c.label, installedVer, latest, pm.ext, runtime.GOARCH)
			exitCode = 1
			continue
		}
		dst := filepath.Join(os.TempDir(), name)
		if err := downloadAsset(ghGet, url, dst); err != nil {
			fmt.Fprintf(opts.Stdout, "%-15s %s → %s   download failed: %v\n",
				c.label, installedVer, latest, err)
			exitCode = 1
			continue
		}
		fmt.Fprintf(opts.Stdout, "%-15s %s → %s   downloaded %s\n",
			c.label, installedVer, latest, dst)
		downloaded = append(downloaded, dst)
	}

	// One combined install command so a user can't update only some packages
	// and end up running mismatched component versions against each other.
	if len(downloaded) > 0 {
		fmt.Fprintf(opts.Stdout, "\nrun:\nsudo %s %s %s\n",
			pm.bin, pm.installCmd, strings.Join(downloaded, " "))
	}
	return exitCode
}
