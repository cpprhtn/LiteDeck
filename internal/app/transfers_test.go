package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Walking a tree is the part of a directory transfer that costs the server:
// readdir after readdir over its sftp-server, before a single byte moves. It
// runs inside the job so the Cancel button reaches it — these pin that it
// actually looks.

func TestWalkStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.txt", "sub/b.txt", "sub/deeper/c.txt"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	files, total, err := walkLocalDir(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("walkLocalDir on a cancelled context = (%d files, %d bytes, %v), want context.Canceled",
			len(files), total, err)
	}
}

func TestWalkCountsWhatItFinds(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{
		"a.txt":               "hello",
		"sub/b.txt":           "world!",
		"sub/deeper/c.txt":    "x",
		"sub/deeper/empty.md": "",
	}
	var want int64
	for rel, body := range bodies {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		want += int64(len(body))
	}
	// A symlink pointing outside the tree. Following it would copy something
	// the user never selected, so it is skipped and contributes nothing.
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("not part of the tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, total, err := walkLocalDir(context.Background(), root)
	if err != nil {
		t.Fatalf("walkLocalDir: %v", err)
	}
	if len(files) != len(bodies) {
		t.Errorf("walked %d files, want %d: %+v", len(files), len(bodies), files)
	}
	if total != want {
		t.Errorf("total = %d bytes, want %d", total, want)
	}
	for _, f := range files {
		if _, ok := bodies[f.Rel]; !ok {
			t.Errorf("unexpected entry %q — paths must be relative and forward-slashed", f.Rel)
		}
	}
}

// A downloaded name is the server's, not ours. Joining it to the folder the
// user picked is the step scp got wrong (CVE-2019-6111): a server — or anyone
// who can write into a directory on it — decides where bytes land on the client.
//
// The cases below are platform-independent on purpose. The backslash ones are
// harmless on this machine and an escape on a Windows client, and CI runs on
// Linux, so a check written against the running platform's separator would be
// green everywhere except a user's laptop.
func TestSafeLocalNameRefusesNamesThatClimbOut(t *testing.T) {
	unsafe := []struct {
		rel string
		why string
	}{
		{"../evil", "a POSIX name cannot contain a slash, but the walk can still produce this"},
		{"../../etc/cron.d/x", "several levels up"},
		{"a/../../b", "climbs out after descending"},
		{`..\evil.exe`, "backslash is a separator on Windows and an ordinary character on Linux"},
		{`..\..\..\Users\victim\evil.exe`, "the real shape of the attack"},
		{`a\..\..\b`, "climbs out after descending, Windows separators"},
		{"/etc/passwd", "absolute"},
		{`\Windows\System32\x`, "absolute, Windows separators"},
		{`C:\Windows\x`, "a volume is not a name inside the tree"},
		{"", "no name at all"},
	}
	for _, tc := range unsafe {
		if safeLocalName(tc.rel) {
			t.Errorf("accepted %q — %s", tc.rel, tc.why)
		}
	}
}

// The guard must not start refusing ordinary files, or a download of anything
// with a nested directory in it would stop working.
func TestSafeLocalNameAcceptsOrdinaryNames(t *testing.T) {
	for _, rel := range []string{
		"file.txt",
		"sub/dir/file.txt",
		"..hidden",       // leading dots are a name, not a climb
		"a..b/c",         //
		"...",            // three dots is a legal file name
		"dir/..name",     //
		`weird\name.txt`, // legal on the Linux box that served it
		"공백 있는 이름.txt",   //
	} {
		if !safeLocalName(rel) {
			t.Errorf("refused %q, which is an ordinary name", rel)
		}
	}
}

// The refusal has to name what was rejected. "transfer failed" on a security
// stop tells nobody anything.
func TestUnsafeNameErrorSaysWhichName(t *testing.T) {
	const name = `..\..\evil.exe`
	err := errUnsafeName(name)
	if err == nil {
		t.Fatal("no error")
	}
	// Quoted, not raw: a file name may contain newlines and terminal escapes,
	// and this message exists precisely because the far side is not being
	// straightforward.
	if !strings.Contains(err.Error(), strconv.Quote(name)) {
		t.Errorf("error does not name the offending entry: %v", err)
	}
}

// A name that would be unreadable — or would rewrite the panel around it — must
// not reach the screen raw.
func TestUnsafeNameErrorNeutralisesTheName(t *testing.T) {
	err := errUnsafeName("../evil\n\x1b[2Jinnocent.txt")
	if strings.ContainsAny(err.Error(), "\n\x1b") {
		t.Errorf("a control character survived into the message: %q", err.Error())
	}
}
