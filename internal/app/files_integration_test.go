package app

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The file explorer against a real SFTP server (§4.2, §7.4).
//
// Filenames here are deliberately hostile — quotes, semicolons, spaces, newlines,
// non-ASCII. The point of using the SFTP subsystem rather than parsing `ls` is
// that these are ordinary filenames, and a test with only tidy names would not
// notice if that broke.

func connectedApp(t *testing.T) *App {
	t.Helper()
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)
	t.Cleanup(stop)
	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	return a
}

// scratchDir makes a disposable remote directory under /tmp.
func scratchDir(t *testing.T, a *App, name string) string {
	t.Helper()
	dir := path.Join("/tmp", name)
	if res := a.MakeDir("fixture", dir); !res.OK {
		t.Fatalf("MakeDir %s: %+v", dir, res)
	}
	t.Cleanup(func() { a.DeletePaths("fixture", []string{dir}, true, "") })
	return dir
}

func TestFileExplorerBasics(t *testing.T) {
	a := connectedApp(t)

	// The explorer opens in the login user's home.
	home, err := a.HomeDir("fixture")
	if err != nil {
		t.Fatalf("HomeDir: %v", err)
	}
	if home != "/home/litedeck" {
		t.Errorf("HomeDir = %q, want /home/litedeck", home)
	}

	listing, err := a.ListDir("fixture", "/etc")
	if err != nil {
		t.Fatalf("ListDir /etc: %v", err)
	}
	if len(listing.Entries) < 10 {
		t.Errorf("/etc has only %d entries", len(listing.Entries))
	}
	if listing.Parent != "/" {
		t.Errorf("Parent = %q, want /", listing.Parent)
	}
	// /etc is a root-level directory, so the UI must know to demand typing
	// before a recursive delete (§7.4).
	if !listing.Protected {
		t.Error("/etc not flagged as protected")
	}

	// Directories sort before files.
	sawFile := false
	for _, e := range listing.Entries {
		if !e.IsDir {
			sawFile = true
		} else if sawFile {
			t.Error("a directory appears after a file; sort order is wrong")
			break
		}
	}

	// Symlinks must be recognised and resolved: whether double-clicking one
	// opens a directory depends on the target, not the link.
	l2, err := a.ListDir("fixture", "/")
	if err != nil {
		t.Fatal(err)
	}
	var links int
	for _, e := range l2.Entries {
		if e.IsSymlink {
			links++
			if e.LinkTarget == "" {
				t.Errorf("symlink %s has no resolved target", e.Name)
			}
		}
	}
	if links == 0 {
		t.Log("no symlinks at / on this image; resolution path not exercised here")
	}
}

// TestFileNamesThatWouldBreakAShell is the reason file work goes through SFTP.
func TestFileNamesThatWouldBreakAShell(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-hostile-names")

	names := []string{
		"plain.txt",
		"with space.txt",
		"it's a file.txt",
		`"double quoted".txt`,
		"semi;colon.txt",
		"dollar$(whoami).txt",
		"back`tick`.txt",
		"pipe|and&more.txt",
		"-leading-dash.txt",
		"한글 파일.txt",
		"newline\nin\nname.txt",
		"*glob?.txt",
	}

	client, err := a.mgr.SFTP("fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		f, err := client.Create(path.Join(dir, n))
		if err != nil {
			t.Fatalf("create %q: %v", n, err)
		}
		f.Write([]byte("x"))
		f.Close()
	}

	listing, err := a.ListDir("fixture", dir)
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	got := make(map[string]bool, len(listing.Entries))
	for _, e := range listing.Entries {
		got[e.Name] = true
	}
	for _, n := range names {
		if !got[n] {
			t.Errorf("filename %q did not survive the round trip", n)
		}
	}

	// Renaming and deleting them must work too — these go through SFTP
	// primitives, so the name is data at every step.
	// No embedded slash in the new name: a filename cannot contain one, and
	// path.Join would quietly turn it into a nested path whose parent does not
	// exist. (Made this mistake twice while writing these tests.)
	src := path.Join(dir, "it's a file.txt")
	dst := path.Join(dir, "renamed; rm -rf *.txt")
	if res := a.RenamePath("fixture", src, dst); !res.OK {
		t.Fatalf("RenamePath: %+v", res)
	}
	if st, err := a.StatPath("fixture", dst); err != nil || !st.Exists {
		t.Errorf("renamed file missing: %+v %v", st, err)
	}
	if res := a.DeletePaths("fixture", []string{dst}, false, ""); !res.OK {
		t.Fatalf("DeletePaths: %+v", res)
	}
	if st, _ := a.StatPath("fixture", dst); st.Exists {
		t.Error("file still present after delete")
	}
}

// TestDeleteGuardsOnRealServer: the guard has to hold against the live server,
// not just in the unit test (§7.4).
func TestDeleteGuardsOnRealServer(t *testing.T) {
	a := connectedApp(t)

	// Root, at any level of insistence.
	for _, typed := range []string{"", "/", "yes"} {
		if res := a.DeletePaths("fixture", []string{"/"}, true, typed); res.OK {
			t.Fatalf("deleting / succeeded with typed=%q", typed)
		}
	}
	// A protected directory without the typed path.
	if res := a.DeletePaths("fixture", []string{"/etc"}, true, ""); res.OK {
		t.Fatal("recursive delete of /etc proceeded without confirmation")
	}
	// Still there.
	if st, err := a.StatPath("fixture", "/etc"); err != nil || !st.Exists {
		t.Fatalf("/etc is gone: %+v %v", st, err)
	}
	// A protected path hidden in a batch.
	if res := a.DeletePaths("fixture", []string{"/tmp", "/etc"}, true, "/etc"); res.OK {
		t.Fatal("a protected path was deleted as part of a batch")
	}
	// Renaming a protected path is refused as well.
	if res := a.RenamePath("fixture", "/etc", "/etc-moved"); res.OK {
		t.Fatal("renaming /etc succeeded")
	}
}

func TestRecursiveDeleteWorksOnOrdinaryPaths(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-recursive")

	nested := path.Join(dir, "a", "b")
	if res := a.MakeDir("fixture", path.Join(dir, "a")); !res.OK {
		t.Fatalf("%+v", res)
	}
	if res := a.MakeDir("fixture", nested); !res.OK {
		t.Fatalf("%+v", res)
	}
	if res := a.WriteTextFile("fixture", path.Join(nested, "f.txt"), "content"); !res.OK {
		t.Fatalf("%+v", res)
	}

	// Non-recursive removal of a non-empty directory fails, as it should.
	if res := a.DeletePaths("fixture", []string{path.Join(dir, "a")}, false, ""); res.OK {
		t.Error("non-recursive delete of a non-empty directory succeeded")
	}
	// Recursive works, no typing needed for an ordinary path.
	if res := a.DeletePaths("fixture", []string{path.Join(dir, "a")}, true, ""); !res.OK {
		t.Fatalf("recursive delete failed: %+v", res)
	}
	if st, _ := a.StatPath("fixture", nested); st.Exists {
		t.Error("directory survived a recursive delete")
	}
}

func TestTextEditor(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-editor")
	file := path.Join(dir, "config.yaml")

	const body = "key: value\n한글: 있음\nlist:\n  - a\n  - b\n"
	if res := a.WriteTextFile("fixture", file, body); !res.OK {
		t.Fatalf("WriteTextFile: %+v", res)
	}

	got, err := a.ReadTextFile("fixture", file)
	if err != nil {
		t.Fatalf("ReadTextFile: %v", err)
	}
	if got.Content != body {
		t.Errorf("content = %q, want %q", got.Content, body)
	}
	if got.TooLarge || got.Binary {
		t.Errorf("flags wrong: %+v", got)
	}

	// Saving must preserve the mode rather than resetting it to a default.
	if res := a.Chmod("fixture", file, 0o600); !res.OK {
		t.Fatalf("Chmod: %+v", res)
	}
	if res := a.WriteTextFile("fixture", file, "changed\n"); !res.OK {
		t.Fatalf("rewrite: %+v", res)
	}
	after, err := a.ReadTextFile("fixture", file)
	if err != nil {
		t.Fatal(err)
	}
	if after.Perm != 0o600 {
		t.Errorf("mode after save = %o, want 600", after.Perm)
	}

	// Binary content must be refused rather than rendered into a textarea and
	// corrupted on save.
	bin := path.Join(dir, "blob.bin")
	client, _ := a.mgr.SFTP("fixture")
	f, err := client.Create(bin)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0x00, 0x01, 0x02, 'a', 'b'})
	f.Close()

	bt, err := a.ReadTextFile("fixture", bin)
	if err != nil {
		t.Fatal(err)
	}
	if !bt.Binary || bt.Content != "" {
		t.Errorf("binary file was offered for editing: %+v", bt)
	}
}

func TestChmod(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-chmod")
	file := path.Join(dir, "script.sh")

	if res := a.WriteTextFile("fixture", file, "#!/bin/sh\necho hi\n"); !res.OK {
		t.Fatalf("%+v", res)
	}
	for _, mode := range []uint32{0o755, 0o644, 0o600} {
		if res := a.Chmod("fixture", file, mode); !res.OK {
			t.Fatalf("Chmod %o: %+v", mode, res)
		}
		listing, err := a.ListDir("fixture", dir)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, e := range listing.Entries {
			if e.Name == "script.sh" {
				found = true
				if e.Perm != mode {
					t.Errorf("perm = %o, want %o", e.Perm, mode)
				}
			}
		}
		if !found {
			t.Fatal("script.sh missing from listing")
		}
	}
	// Nonsense modes are rejected before anything is sent.
	if res := a.Chmod("fixture", file, 0o10000); res.OK {
		t.Error("an out-of-range mode was accepted")
	}
}

func TestTransferRoundTrip(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-transfer")

	// A payload big enough that progress events actually fire.
	local := filepath.Join(t.TempDir(), "payload.bin")
	payload := strings.Repeat("litedeck transfer test payload\n", 40000) // ~1.2 MB
	if err := os.WriteFile(local, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := a.StartUpload("fixture", []string{local}, dir)
	if err != nil {
		t.Fatalf("StartUpload: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("got %d transfer ids", len(ids))
	}
	waitTransfer(t, a, ids[0], TransferDone)

	remote := path.Join(dir, "payload.bin")
	st, err := a.StatPath("fixture", remote)
	if err != nil || !st.Exists {
		t.Fatalf("uploaded file missing: %+v %v", st, err)
	}
	if st.Size != int64(len(payload)) {
		t.Errorf("uploaded size = %d, want %d", st.Size, len(payload))
	}
	// The temporary name must not be left behind.
	if partial, _ := a.StatPath("fixture", remote+".litedeck-partial"); partial.Exists {
		t.Error("a .litedeck-partial file was left on the server")
	}

	// Round trip back down and compare bytes.
	downDir := t.TempDir()
	ids, err = a.StartDownload("fixture", []string{remote}, downDir)
	if err != nil {
		t.Fatalf("StartDownload: %v", err)
	}
	waitTransfer(t, a, ids[0], TransferDone)

	got, err := os.ReadFile(filepath.Join(downDir, "payload.bin"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != payload {
		t.Errorf("downloaded content differs (%d vs %d bytes)", len(got), len(payload))
	}
}

// TestTransferRejectsMissingPaths: a path that is not there must fail at queue
// time, not silently produce a transfer that does nothing.
func TestTransferRejectsMissingPaths(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-missing")

	if _, err := a.StartUpload("fixture", []string{filepath.Join(t.TempDir(), "nope")}, dir); err == nil {
		t.Error("uploading a nonexistent local path was accepted")
	}
	if _, err := a.StartDownload("fixture", []string{"/tmp/definitely-not-here"}, t.TempDir()); err == nil {
		t.Error("downloading a nonexistent remote path was accepted")
	}
	// An empty directory is legitimate and must produce a completed transfer,
	// not an error and not a stuck queue entry.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	ids, err := a.StartUpload("fixture", []string{empty}, dir)
	if err != nil {
		t.Fatalf("uploading an empty directory: %v", err)
	}
	waitTransfer(t, a, ids[0], TransferDone)
	if st, err := a.StatPath("fixture", path.Join(dir, "empty")); err != nil || !st.IsDir {
		t.Errorf("empty directory was not created remotely: %+v %v", st, err)
	}
}

func waitTransfer(t *testing.T, a *App, id, want string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range a.Transfers() {
			if tr.ID != id {
				continue
			}
			switch tr.Status {
			case want:
				if want == TransferDone && tr.Done != tr.Size {
					t.Errorf("transfer reported done with %d/%d bytes", tr.Done, tr.Size)
				}
				return
			case TransferFailed, TransferCancelled:
				t.Fatalf("transfer %s ended as %s: %s", id, tr.Status, tr.Error)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("transfer %s did not reach %s in time", id, want)
}

// TestDirectoryTransferRoundTrip covers recursive transfer (v1.x): a tree goes
// up, comes back down, and arrives byte-identical with its structure intact.
func TestDirectoryTransferRoundTrip(t *testing.T) {
	a := connectedApp(t)
	remoteParent := scratchDir(t, a, "litedeck-dirxfer")

	// A tree with nesting, an empty-ish file, and a name that would break a
	// shell — the transfer path must not care.
	local := filepath.Join(t.TempDir(), "tree")
	files := map[string]string{
		"top.txt":                     "top level\n",
		"sub/nested.txt":              strings.Repeat("nested\n", 5000),
		"sub/deeper/leaf.txt":         "leaf\n",
		"sub/it's a file; rm -rf.txt": "hostile name\n",
		"한글/파일.txt":                   "korean\n",
	}
	for rel, body := range files {
		p := filepath.Join(local, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := a.StartUpload("fixture", []string{local}, remoteParent)
	if err != nil {
		t.Fatalf("StartUpload(dir): %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("a directory should be one queue entry, got %d", len(ids))
	}
	waitTransfer(t, a, ids[0], TransferDone)

	// One entry, but it knows how many files it covered.
	var up Transfer
	for _, tr := range a.Transfers() {
		if tr.ID == ids[0] {
			up = tr
		}
	}
	if !up.Dir || up.Files != len(files) {
		t.Errorf("transfer = %+v, want a directory of %d files", up, len(files))
	}
	if up.FilesDone != up.Files {
		t.Errorf("finished with %d/%d files", up.FilesDone, up.Files)
	}

	remoteTree := path.Join(remoteParent, "tree")
	for rel, body := range files {
		st, err := a.StatPath("fixture", path.Join(remoteTree, rel))
		if err != nil || !st.Exists {
			t.Errorf("%s missing on the server: %v", rel, err)
			continue
		}
		if st.Size != int64(len(body)) {
			t.Errorf("%s size = %d, want %d", rel, st.Size, len(body))
		}
	}

	// Back down.
	downDir := t.TempDir()
	ids, err = a.StartDownload("fixture", []string{remoteTree}, downDir)
	if err != nil {
		t.Fatalf("StartDownload(dir): %v", err)
	}
	waitTransfer(t, a, ids[0], TransferDone)

	for rel, body := range files {
		got, err := os.ReadFile(filepath.Join(downDir, "tree", filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s missing locally: %v", rel, err)
			continue
		}
		if string(got) != body {
			t.Errorf("%s differs after the round trip (%d vs %d bytes)", rel, len(got), len(body))
		}
	}
}
