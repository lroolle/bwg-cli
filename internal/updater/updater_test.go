package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// Download is the whole point of the package and was the one part
// nothing exercised: it picks the asset for this platform, fetches it
// and hands back an executable.
func TestDownloadPicksThisPlatformsAsset(t *testing.T) {
	name := AssetName(runtime.GOOS, runtime.GOARCH)
	body := buildTarGz(t, "bwg", "#!/bin/sh\necho hello\n")
	if runtime.GOOS == "windows" {
		body = buildZip(t, "bwg.exe", "#!/bin/sh\necho hello\n")
	}

	var served string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = r.URL.Path
		w.Write(body)
	}))
	defer srv.Close()

	rel := &Release{Version: "0.3.0", TagName: "v0.3.0", assets: []asset{
		{Name: "bwg_someOtherOS_mips.tar.gz", URL: srv.URL + "/wrong"},
		{Name: name, URL: srv.URL + "/" + name},
	}}

	path, err := Download(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	if served != "/"+name {
		t.Errorf("downloaded %q, want the asset for %s/%s", served, runtime.GOOS, runtime.GOARCH)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "#!/bin/sh\necho hello\n" {
		t.Errorf("extracted content = %q", content)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		t.Error("the downloaded binary is not executable")
	}
}

// A release built without this platform's archive must say so, not
// download the first thing it finds.
func TestDownloadWithoutAnAssetForThisPlatform(t *testing.T) {
	rel := &Release{Version: "0.3.0", TagName: "v0.3.0",
		assets: []asset{{Name: "bwg_someOtherOS_mips.tar.gz", URL: "http://example.invalid/x"}}}

	_, err := Download(context.Background(), rel)
	if err == nil {
		t.Fatal("a release with no matching asset downloaded something")
	}
	if !strings.Contains(err.Error(), AssetName(runtime.GOOS, runtime.GOARCH)) {
		t.Errorf("the error does not name the asset it wanted: %v", err)
	}
}

func TestDownloadReportsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	name := AssetName(runtime.GOOS, runtime.GOARCH)
	rel := &Release{Version: "0.3.0", assets: []asset{{Name: name, URL: srv.URL + "/" + name}}}

	if _, err := Download(context.Background(), rel); err == nil {
		t.Fatal("a 404 produced no error")
	}
}

func TestExtractRejectsAnArchiveWithoutTheBinary(t *testing.T) {
	if _, err := extractTarGz(bytes.NewReader(buildTarGz(t, "README.md", "hi"))); err == nil {
		t.Error("a tar.gz without bwg extracted successfully")
	}
	if _, err := extractZip(bytes.NewReader(buildZip(t, "README.md", "hi"))); err == nil {
		t.Error("a zip without bwg extracted successfully")
	}
	if _, err := extractTarGz(strings.NewReader("this is not gzip")); err == nil {
		t.Error("garbage extracted successfully")
	}
}

// The Windows path is never exercised on CI's Linux runners unless it
// is called directly, and a broken updater is only discovered by the
// people it breaks.
func TestExtractZip(t *testing.T) {
	path, err := extractZip(bytes.NewReader(buildZip(t, "bwg.exe", "binary bytes")))
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "binary bytes" {
		t.Errorf("extracted content = %q", content)
	}
}

// $TMPDIR is a different filesystem from $HOME on most Linux boxes, so
// the rename in Replace fails with EXDEV on a perfectly good update.
// The copy fallback is what makes `bwg update` work there at all.
func TestInstallCopyIsTheCrossFilesystemFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "downloaded")
	dst := filepath.Join(dir, "bwg")
	if err := os.WriteFile(src, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := installCopy(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("copied content = %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Errorf("the installed binary is not executable (mode %v)", info.Mode())
	}
	// The source is left for the caller to clean up, as Download's
	// contract promises.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the downloaded file was consumed: %v", err)
	}
}

// A failed install must leave a working bwg behind, not a hole where
// one used to be.
func TestReplaceRestoresTheOldBinaryWhenTheInstallFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bwg")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing to install: neither the rename nor the copy can work.
	missing := filepath.Join(dir, "download-that-vanished")

	if err := Replace(bin, missing); err == nil {
		t.Fatal("replacing with a missing file succeeded")
	}
	got, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("the old binary was not restored: %v", err)
	}
	if string(got) != "old" {
		t.Errorf("restored binary = %q, want the original", got)
	}
}

func buildZip(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
