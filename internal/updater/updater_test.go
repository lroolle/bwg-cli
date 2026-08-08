package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.1", "0.1.0", true},
		{"2.0.0", "1.99.99", true},
		{"0.1.0", "0.2.0", false},
		{"0.1.0", "0.1.0", false},
		{"1.0.0", "1.0.0", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s>%s", tc.a, tc.b), func(t *testing.T) {
			if got := newer(tc.a, tc.b); got != tc.want {
				t.Errorf("newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestStripV(t *testing.T) {
	if stripV("v1.2.3") != "1.2.3" {
		t.Error("did not strip v")
	}
	if stripV("1.2.3") != "1.2.3" {
		t.Error("stripped too much")
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
	}{
		{"darwin", "amd64", "bwg_macOS_x86_64.tar.gz"},
		{"darwin", "arm64", "bwg_macOS_arm64.tar.gz"},
		{"linux", "amd64", "bwg_linux_x86_64.tar.gz"},
		{"linux", "arm64", "bwg_linux_arm64.tar.gz"},
		{"windows", "amd64", "bwg_windows_x86_64.zip"},
		{"windows", "arm64", "bwg_windows_arm64.zip"},
	}
	for _, tc := range cases {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			if got := AssetName(tc.goos, tc.goarch); got != tc.want {
				t.Errorf("AssetName(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

func TestCheckLatest(t *testing.T) {
	const response = `{
		"tag_name": "v0.3.0",
		"html_url": "https://github.com/lroolle/bwg-cli/releases/tag/v0.3.0",
		"published_at": "2025-06-15T10:00:00Z",
		"assets": [
			{"name": "bwg_linux_x86_64.tar.gz", "browser_download_url": "https://example.com/bwg_linux_x86_64.tar.gz"},
			{"name": "bwg_macOS_arm64.tar.gz", "browser_download_url": "https://example.com/bwg_macOS_arm64.tar.gz"}
		]
	}`

	var lastUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastUA = r.Header.Get("User-Agent")
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(response))
	}))
	defer srv.Close()

	// Point CheckLatest at the test server.
	orig := releaseURL
	defer func() { setReleaseURL(orig) }()
	setReleaseURL(srv.URL)

	rel, err := CheckLatest(context.Background(), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if lastUA != "bwg/0.1.0" {
		t.Errorf("User-Agent = %q, want bwg/0.1.0", lastUA)
	}
	if rel.Version != "0.3.0" {
		t.Errorf("Version = %q, want 0.3.0", rel.Version)
	}
	if rel.TagName != "v0.3.0" {
		t.Errorf("TagName = %q", rel.TagName)
	}
	if !rel.HasUpdate {
		t.Error("HasUpdate = false, want true for 0.3.0 > 0.1.0")
	}

	// Same version: no update.
	setReleaseURL(srv.URL)
	rel2, err := CheckLatest(context.Background(), "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if rel2.HasUpdate {
		t.Error("HasUpdate = true for same version")
	}
}

func TestCheckLatestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	orig := releaseURL
	defer func() { setReleaseURL(orig) }()
	setReleaseURL(srv.URL)

	_, err := CheckLatest(context.Background(), "0.1.0")
	if err == nil {
		t.Fatal("expected an error for 404")
	}
}

func TestDownloadAndExtract(t *testing.T) {
	// Build a tar.gz with a fake bwg binary inside.
	archiveBody := buildTarGz(t, "bwg", "#!/bin/sh\necho hello\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(archiveBody)
	}))
	defer srv.Close()

	rel := &Release{
		Version: "0.3.0",
		TagName: "v0.3.0",
		assets: []asset{
			{Name: AssetName("linux", "amd64"), URL: srv.URL + "/bwg_linux_x86_64.tar.gz"},
			{Name: AssetName("darwin", "arm64"), URL: srv.URL + "/bwg_macOS_arm64.tar.gz"},
		},
	}

	// Force the asset name to match one we have, regardless of test host OS.
	name := AssetName("linux", "amd64")
	found := false
	for _, a := range rel.assets {
		if a.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Skipf("no test asset for %s", name)
	}

	// Temporarily override runtime lookups by calling extractTarGz directly.
	resp, err := http.Get(srv.URL + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	path, err := extractTarGz(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "#!/bin/sh\necho hello\n" {
		t.Errorf("extracted content = %q", content)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		t.Error("extracted binary is not executable")
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "bwg")
	newBin := filepath.Join(dir, "bwg-new")

	os.WriteFile(oldBin, []byte("old"), 0755)
	os.WriteFile(newBin, []byte("new"), 0755)

	if err := Replace(oldBin, newBin); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(oldBin)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("after replace, binary = %q, want new", got)
	}

	backup, err := os.ReadFile(oldBin + ".old")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "old" {
		t.Errorf("backup = %q, want old", backup)
	}
}

func TestReplaceMissingSource(t *testing.T) {
	dir := t.TempDir()
	oldBin := filepath.Join(dir, "bwg")
	if err := Replace(oldBin, filepath.Join(dir, "nope")); err == nil {
		t.Error("expected an error when the current binary is missing")
	}
}

func buildTarGz(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: name,
		Mode: 0755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
