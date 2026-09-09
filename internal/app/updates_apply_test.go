package app

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The updater replaces the running application. Everything it does before the
// swap is a chance to install the wrong bytes, so the parts that decide *which*
// bytes are tested here: what the checksum file says, what the archive is
// allowed to write, and whether the download's hash is computed over what was
// actually written.

func TestChecksumForBothFormats(t *testing.T) {
	// sha256sum prints two spaces; shasum -a 256 -b prints a "*" before the
	// name. The release workflow uses whichever the runner has, so both land in
	// real SHA256SUMS.txt files.
	body := strings.Join([]string{
		"aaaa  litedeck-desktop-linux-amd64.tar.gz",
		"bbbb *litedeck-desktop-macos.zip",
		"cccc  SHA256SUMS.txt",
		"",
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	for _, tc := range []struct{ name, want string }{
		{"litedeck-desktop-linux-amd64.tar.gz", "aaaa"},
		{"litedeck-desktop-macos.zip", "bbbb"},
	} {
		got, err := checksumFor(context.Background(), srv.URL, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}

	if _, err := checksumFor(context.Background(), srv.URL, "litedeck-desktop-windows-amd64.zip"); err == nil {
		t.Error("목록에 없는 이름인데 통과했다 — 검증 없이 설치될 수 있다")
	}
}

func TestDownloadHashesWhatItWrote(t *testing.T) {
	payload := strings.Repeat("litedeck", 40_000) // bigger than the read buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, payload)
	}))
	defer srv.Close()

	a := &App{}
	path := filepath.Join(t.TempDir(), "asset")
	got, err := a.download(context.Background(), srv.URL, path)
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	// Hash the file on disk, not the payload: the point is that what was
	// written and what was hashed are the same bytes.
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(written)
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("hash %s, file on disk %s", got, want)
	}
	if len(written) != len(payload) {
		t.Errorf("wrote %d bytes of %d", len(written), len(payload))
	}
}

func TestSafeJoinRefusesEscapes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"../escaped",
		"../../etc/passwd",
		"a/../../escaped",
		"/absolute/escape/../../../etc/passwd",
	} {
		if got, err := safeJoin(dir, name); err == nil {
			t.Errorf("%q was accepted and resolved to %q", name, got)
		}
	}
	// The ordinary case still works.
	got, err := safeJoin(dir, "litedeck.app/Contents/MacOS/litedeck")
	if err != nil {
		t.Fatalf("plain entry rejected: %v", err)
	}
	if !strings.HasPrefix(got, dir) {
		t.Errorf("got %q, want it under %q", got, dir)
	}
}

func TestUnzipKeepsTheExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no executable bit on Windows")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	hdr := &zip.FileHeader{Name: "litedeck.app/Contents/MacOS/litedeck", Method: zip.Deflate}
	hdr.SetMode(0o755)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out := filepath.Join(dir, "out")
	if err := unpack(archive, out); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	// Without this the macOS bundle extracts and will not launch — the failure
	// arrives after the swap, when the old copy is already gone.
	fi, err := os.Stat(filepath.Join(out, "litedeck.app/Contents/MacOS/litedeck"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode %v — the executable bit was dropped", fi.Mode().Perm())
	}
}

func TestUnzipRefusesAnEscapingEntry(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("../../escaped.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()

	out := filepath.Join(dir, "out")
	if err := unpack(archive, out); err == nil {
		t.Fatal("탈출하는 항목을 그대로 풀었다")
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err == nil {
		t.Error("압축 바깥에 파일이 쓰였다")
	}
}

func TestUntargzKeepsTheExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no executable bit on Windows")
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "litedeck", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	out := filepath.Join(dir, "out")
	if err := unpack(archive, out); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	fi, err := os.Stat(filepath.Join(out, "litedeck"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("mode %v — the executable bit was dropped", fi.Mode().Perm())
	}
}

func TestAssetNameMatchesTheReleaseWorkflow(t *testing.T) {
	// These names are written in .github/workflows/release.yml. A rename there
	// with no change here means every client's update stops working, silently,
	// and only for people who already have the old build.
	name, err := assetName()
	if err != nil {
		// An unsupported platform is a legitimate answer, not a failure.
		t.Skipf("%s/%s: %v", runtime.GOOS, runtime.GOARCH, err)
	}
	known := map[string]bool{
		"litedeck-desktop-macos.zip":          true,
		"litedeck-desktop-windows-amd64.zip":  true,
		"litedeck-desktop-linux-amd64.tar.gz": true,
	}
	if !known[name] {
		t.Errorf("%q is not one of the names release.yml publishes", name)
	}
}

func TestSwapHelperTakesItsPathsAsArguments(t *testing.T) {
	// The paths are never pasted into the script, because a home directory with
	// a space in it is ordinary and shell quoting is where that goes wrong.
	staged := filepath.Join(t.TempDir(), "staged", "litedeck")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := writeSwapHelper(staged)
	if err != nil {
		t.Fatalf("writeSwapHelper: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), staged) {
		t.Error("스크립트 본문에 경로가 박혔다 — 인수로 넘겨야 한다")
	}
	args := swapArgs(4242, "/Applications/Lite Deck.app", staged)
	if len(args) != 3 || args[0] != "4242" || args[2] != staged {
		t.Errorf("swapArgs = %q", args)
	}
}
