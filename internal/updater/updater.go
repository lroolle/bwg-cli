package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxBody = 4 << 20 // 4 MiB is plenty for a release JSON

// releaseURL is a var so tests can point it at httptest.
var releaseURL = "https://api.github.com/repos/lroolle/bwg-cli/releases/latest"

// setReleaseURL overrides the GitHub API URL. Test-only.
func setReleaseURL(u string) { releaseURL = u }

// Release describes a GitHub release.
type Release struct {
	Version     string    // semver without leading v
	TagName     string    // raw tag, e.g. "v0.3.0"
	URL         string    // browser URL for the release page
	PublishedAt time.Time // when the release was published
	HasUpdate   bool      // true when Version > current
	assets      []asset   // unexported; used by Download
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// ghRelease is the subset of the GitHub releases response we decode.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []asset   `json:"assets"`
}

// CheckLatest fetches the latest release from GitHub and reports
// whether it is newer than current.
func CheckLatest(ctx context.Context, current string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return nil, fmt.Errorf("updater: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "bwg/"+current)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updater: fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updater: GitHub API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("updater: read response: %w", err)
	}

	var gh ghRelease
	if err := json.Unmarshal(body, &gh); err != nil {
		return nil, fmt.Errorf("updater: decode release: %w", err)
	}

	version := stripV(gh.TagName)
	return &Release{
		Version:     version,
		TagName:     gh.TagName,
		URL:         gh.HTMLURL,
		PublishedAt: gh.PublishedAt,
		HasUpdate:   newer(version, stripV(current)),
		assets:      gh.Assets,
	}, nil
}

// Download fetches the archive for the current OS/arch, extracts the
// bwg binary, and returns the path to a temp file containing it. The
// caller is responsible for removing the temp file after use.
func Download(ctx context.Context, rel *Release) (string, error) {
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	var dlURL string
	for _, a := range rel.assets {
		if a.Name == name {
			dlURL = a.URL
			break
		}
	}
	if dlURL == "" {
		return "", fmt.Errorf("updater: no asset %q in release %s", name, rel.TagName)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", fmt.Errorf("updater: build download request: %w", err)
	}
	req.Header.Set("User-Agent", "bwg/"+rel.Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("updater: download %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("updater: download returned %d", resp.StatusCode)
	}

	if runtime.GOOS == "windows" {
		return extractZip(resp.Body)
	}
	return extractTarGz(resp.Body)
}

// Replace swaps the running binary at binaryPath with the new one at
// newPath. The old binary is kept as binaryPath + ".old".
//
// The move-aside dance is not decoration: a running executable cannot
// be written to (ETXTBSY on Linux), so the live inode is renamed out of
// the way and the new binary takes its name.
//
// The rename of the *downloaded* file can fail even when everything is
// healthy: Download writes to $TMPDIR, which is a separate filesystem
// from $HOME on most Linux systems, and rename cannot cross that
// boundary. So a failed rename falls back to a copy rather than being
// reported as a broken update. Whatever happens, the old binary goes
// back if the install does not complete.
func Replace(binaryPath, newPath string) error {
	backup := binaryPath + ".old"
	if err := os.Rename(binaryPath, backup); err != nil {
		return fmt.Errorf("updater: backup current binary: %w", err)
	}
	if err := os.Rename(newPath, binaryPath); err != nil {
		if cerr := installCopy(newPath, binaryPath); cerr != nil {
			os.Rename(backup, binaryPath)
			return fmt.Errorf("updater: install new binary: %w", cerr)
		}
	}
	return nil
}

// installCopy writes src to dst as an executable, for the case where
// rename cannot. dst must not exist as the running binary — Replace
// has already moved that aside — or the copy hits ETXTBSY.
func installCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	mode := os.FileMode(0o755)
	if info, err := in.Stat(); err == nil {
		mode = info.Mode() | 0o111
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	// A half-written binary that survives a crash is worse than a
	// failed update, so the bytes are on disk before we claim success.
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	// O_CREATE honours umask; the binary has to be executable anyway.
	return os.Chmod(dst, mode)
}

// AssetName returns the goreleaser archive name for a given GOOS/GOARCH.
func AssetName(goos, goarch string) string {
	os := goos
	switch goos {
	case "darwin":
		os = "macOS"
	case "windows":
		os = "windows"
	case "linux":
		os = "linux"
	}

	arch := goarch
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	}

	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("bwg_%s_%s%s", os, arch, ext)
}

func extractTarGz(r io.Reader) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("updater: gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("updater: tar: %w", err)
		}
		if filepath.Base(hdr.Name) != "bwg" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		return writeTemp(tr, hdr.FileInfo().Mode())
	}
	return "", fmt.Errorf("updater: bwg binary not found in archive")
}

func extractZip(r io.Reader) (string, error) {
	// zip needs random access, so buffer the whole thing.
	tmp, err := os.CreateTemp("", "bwg-zip-*")
	if err != nil {
		return "", fmt.Errorf("updater: create temp for zip: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, r)
	if err != nil {
		return "", fmt.Errorf("updater: buffer zip: %w", err)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return "", fmt.Errorf("updater: zip: %w", err)
	}

	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if base != "bwg" && base != "bwg.exe" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("updater: open %s in zip: %w", f.Name, err)
		}
		defer rc.Close()
		return writeTemp(rc, f.Mode())
	}
	return "", fmt.Errorf("updater: bwg binary not found in zip archive")
}

func writeTemp(r io.Reader, mode os.FileMode) (string, error) {
	out, err := os.CreateTemp("", "bwg-update-*")
	if err != nil {
		return "", fmt.Errorf("updater: create temp: %w", err)
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", fmt.Errorf("updater: write temp: %w", err)
	}
	if err := out.Chmod(mode | 0111); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", fmt.Errorf("updater: chmod: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return "", fmt.Errorf("updater: close temp: %w", err)
	}
	return out.Name(), nil
}

// stripV removes a leading "v" from a version string.
func stripV(v string) string {
	return strings.TrimPrefix(v, "v")
}

// newer reports whether a > b using numeric semver comparison.
func newer(a, b string) bool {
	ap := parseSemver(a)
	bp := parseSemver(b)
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			return ap[i] > bp[i]
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	var parts [3]int
	for i, s := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(s)
		parts[i] = n
	}
	return parts
}
