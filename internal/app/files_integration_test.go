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

// The save path is the one the app uses most, on the files a server can least
// afford to lose (§4.7-3). These check the three things that make it safe:
// the target is replaced rather than emptied and refilled, its mode survives,
// and an edit that arrived from somewhere else is not silently overwritten.

func TestSaveIsAtomicAndLeavesNoLitter(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-atomic")
	file := path.Join(dir, "nginx.conf")

	res := a.SaveTextFile("fixture", SaveRequest{Path: file, Content: "server {}\n"})
	if !res.OK || res.InPlace {
		t.Fatalf("create: %+v", res)
	}
	if res.ModTime == 0 {
		t.Error("no mtime returned — the next save has nothing to compare against")
	}

	if res := a.Chmod("fixture", file, 0o600); !res.OK {
		t.Fatalf("Chmod: %+v", res)
	}
	saved := a.SaveTextFile("fixture", SaveRequest{Path: file, Content: "server { listen 80; }\n"})
	if !saved.OK || saved.InPlace {
		t.Fatalf("rewrite: %+v", saved)
	}

	got, err := a.ReadTextFile("fixture", file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "server { listen 80; }\n" {
		t.Errorf("content = %q", got.Content)
	}
	// A staged write creates a new inode, so the mode has to be carried across
	// deliberately — this is the case where "it worked before" stops being true.
	if got.Perm != 0o600 {
		t.Errorf("mode after atomic save = %o, want 600", got.Perm)
	}

	// The staged copy is a hidden sibling, so a listing that ignores dotfiles
	// would call this clean while the directory filled up with .tmp files.
	listing, err := a.ListDir("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listing.Entries {
		if strings.HasPrefix(e.Name, ".litedeck-") {
			t.Errorf("staged file left behind: %s", e.Name)
		}
	}

	// The flag above is only the code's own account of itself. This is the
	// observable difference: replacing a directory entry leaves a second link to
	// the old content alone, where writing through the file would change what
	// both names point at. It proves the rename happened, and it pins the cost
	// of taking that path — a save breaks hard links (§4.7-3).
	client, err := a.mgr.SFTP("fixture")
	if err != nil {
		t.Fatal(err)
	}
	link := path.Join(dir, "nginx.conf.hardlink")
	if err := client.Link(file, link); err != nil {
		t.Skipf("서버가 하드링크를 지원하지 않습니다: %v", err)
	}
	if res := a.SaveTextFile("fixture", SaveRequest{Path: file, Content: "server { listen 443; }\n"}); !res.OK || res.InPlace {
		t.Fatalf("%+v", res)
	}
	old, err := a.ReadTextFile("fixture", link)
	if err != nil {
		t.Fatal(err)
	}
	if old.Content != "server { listen 80; }\n" {
		t.Errorf("the old content was written through, not replaced: %q — the save was not atomic", old.Content)
	}
}

func TestSaveDetectsAnEditThatArrivedFromElsewhere(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-conflict")
	file := path.Join(dir, "app.yaml")

	if res := a.WriteTextFile("fixture", file, "version: 1\n"); !res.OK {
		t.Fatalf("%+v", res)
	}
	opened, err := a.ReadTextFile("fixture", file)
	if err != nil {
		t.Fatal(err)
	}

	// Somebody else edits it. mtime is whole seconds over SFTP v3, so a same-second
	// rewrite of the same length is genuinely undetectable — wait it out rather
	// than write a test that passes for the wrong reason.
	time.Sleep(1100 * time.Millisecond)
	if res := a.WriteTextFile("fixture", file, "version: 2\nfrom: elsewhere\n"); !res.OK {
		t.Fatalf("out-of-band edit: %+v", res)
	}

	req := SaveRequest{
		Path:        file,
		Content:     "version: 99\n",
		BaseModTime: opened.ModTime,
		BaseSize:    opened.Size,
	}
	res := a.SaveTextFile("fixture", req)
	if !res.Conflict || res.OK {
		t.Fatalf("overwrote a file that had changed: %+v", res)
	}
	// A refused save must not have written anything.
	now, err := a.ReadTextFile("fixture", file)
	if err != nil {
		t.Fatal(err)
	}
	if now.Content != "version: 2\nfrom: elsewhere\n" {
		t.Errorf("a conflicting save still wrote: %q", now.Content)
	}

	// Once the user has seen the conflict, they get to overrule it.
	req.Force = true
	if res := a.SaveTextFile("fixture", req); !res.OK || res.Conflict {
		t.Fatalf("forced save: %+v", res)
	}
	if now, _ := a.ReadTextFile("fixture", file); now.Content != "version: 99\n" {
		t.Errorf("forced save did not land: %q", now.Content)
	}

	// The mtime a save hands back has to be good enough for the save after it,
	// or every second save in a session reports a conflict with itself.
	first := a.SaveTextFile("fixture", SaveRequest{Path: file, Content: "a\n"})
	if !first.OK {
		t.Fatalf("%+v", first)
	}
	time.Sleep(1100 * time.Millisecond)
	second := a.SaveTextFile("fixture", SaveRequest{
		Path: file, Content: "b\n", BaseModTime: first.ModTime, BaseSize: first.Size,
	})
	if !second.OK || second.Conflict {
		t.Fatalf("consecutive saves conflicted with each other: %+v", second)
	}

	// Deletion is a conflict too: saving would recreate a file the user may have
	// meant to be gone.
	if res := a.DeletePaths("fixture", []string{file}, false, ""); !res.OK {
		t.Fatalf("%+v", res)
	}
	gone := a.SaveTextFile("fixture", SaveRequest{
		Path: file, Content: "c\n", BaseModTime: second.ModTime, BaseSize: second.Size,
	})
	if !gone.Conflict || gone.OK {
		t.Errorf("silently recreated a deleted file: %+v", gone)
	}
}

func TestSaveFallsBackWhenTheDirectoryWillNotTakeATempFile(t *testing.T) {
	a := connectedApp(t)
	dir := scratchDir(t, a, "litedeck-readonly-dir")
	file := path.Join(dir, "sshd_config")

	if res := a.WriteTextFile("fixture", file, "Port 22\n"); !res.OK {
		t.Fatalf("%+v", res)
	}
	// Write permission on a file and on its directory are different things, and
	// the atomic path needs the directory. Dropping the directory's write bit is
	// exactly the shape of /etc on a machine where the operator owns the config
	// but not the folder — the save has to still work, and has to admit how.
	if res := a.Chmod("fixture", dir, 0o555); !res.OK {
		t.Fatalf("Chmod dir: %+v", res)
	}
	t.Cleanup(func() { a.Chmod("fixture", dir, 0o755) })

	res := a.SaveTextFile("fixture", SaveRequest{Path: file, Content: "Port 2222\n"})
	if !res.OK {
		t.Fatalf("fallback save failed: %+v", res)
	}
	if !res.InPlace {
		t.Error("wrote in place without saying so — the UI cannot warn about what it is not told")
	}
	if got, _ := a.ReadTextFile("fixture", file); got.Content != "Port 2222\n" {
		t.Errorf("content = %q", got.Content)
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
