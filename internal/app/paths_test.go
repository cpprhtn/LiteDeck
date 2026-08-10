package app

import (
	"strings"
	"testing"
)

func TestCleanRemotePath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"/etc", "/etc"},
		{"/etc/", "/etc"},
		{"/etc//ssh///", "/etc/ssh"},
		{"/etc/./ssh", "/etc/ssh"},
		{"/etc/ssh/../passwd", "/etc/passwd"},
		{"/", "/"},
		{"  /var/log  ", "/var/log"},
		{"/파일 이름/a b.txt", "/파일 이름/a b.txt"},
	}
	for _, c := range ok {
		got, err := CleanRemotePath(c.in)
		if err != nil {
			t.Errorf("CleanRemotePath(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("CleanRemotePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{"", "   ", "relative/path", "etc", "/nul\x00byte"}
	for _, in := range bad {
		if got, err := CleanRemotePath(in); err == nil {
			t.Errorf("CleanRemotePath(%q) = %q, want an error", in, got)
		}
	}
}

// Remote paths are POSIX whatever the client runs on. Using filepath here would
// rewrite "/etc/ssh" as `\etc\ssh` on Windows and every later call would fail.
func TestCleanRemotePathKeepsForwardSlashes(t *testing.T) {
	got, err := CleanRemotePath("/etc/ssh/sshd_config")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(got, '\\') {
		t.Errorf("path was rewritten with backslashes: %q", got)
	}
}

func TestIsProtectedPath(t *testing.T) {
	protected := []string{
		"/", "/etc", "/usr", "/var", "/home", "/opt", "/srv", "/boot",
		"/data", // whatever happens to be at the top of this server
		"/usr/local", "/var/log", "/etc/ssh", "/root/.ssh",
		"/home/alice", "/Users/bob",
	}
	for _, p := range protected {
		if !IsProtectedPath(p) {
			t.Errorf("IsProtectedPath(%q) = false, want true", p)
		}
	}

	ordinary := []string{
		"/home/alice/projects", "/var/log/nginx/access.log",
		"/opt/myapp/releases/v3", "/tmp/scratch/build",
	}
	for _, p := range ordinary {
		if IsProtectedPath(p) {
			t.Errorf("IsProtectedPath(%q) = true; ordinary work would need retyping", p)
		}
	}
}

// TestCheckDeleteRefusesRoot: there is no confirmation strong enough. A file
// manager should not be able to delete the root filesystem at any level of
// insistence.
func TestCheckDeleteRefusesRoot(t *testing.T) {
	for _, typed := range []string{"", "/", "yes", "DELETE"} {
		if _, err := CheckDelete([]string{"/"}, true, typed); err == nil {
			t.Errorf("deleting / was allowed with typed=%q", typed)
		}
	}
	// Even non-recursively.
	if _, err := CheckDelete([]string{"/"}, false, "/"); err == nil {
		t.Error("deleting / was allowed non-recursively")
	}
}

func TestCheckDeleteProtectedNeedsTypedPath(t *testing.T) {
	// Without the typed path: refused.
	if _, err := CheckDelete([]string{"/etc"}, true, ""); err == nil {
		t.Fatal("recursive delete of /etc proceeded without typed confirmation")
	}
	// A near miss is still a miss.
	for _, typed := range []string{"/etc/", "etc", "/Etc", " /etc x", "yes"} {
		if _, err := CheckDelete([]string{"/etc"}, true, typed); err == nil {
			t.Errorf("typed %q was accepted for /etc", typed)
		}
	}
	// Exact match, with surrounding whitespace tolerated.
	for _, typed := range []string{"/etc", "  /etc  "} {
		got, err := CheckDelete([]string{"/etc"}, true, typed)
		if err != nil {
			t.Errorf("typed %q was rejected: %v", typed, err)
			continue
		}
		if len(got) != 1 || got[0] != "/etc" {
			t.Errorf("CheckDelete returned %q", got)
		}
	}
}

// TestCheckDeleteProtectedCannotHideInABatch: a single typed path cannot
// meaningfully confirm a batch, and slipping one into a multi-select is exactly
// how the accident happens.
func TestCheckDeleteProtectedCannotHideInABatch(t *testing.T) {
	batch := []string{"/tmp/scratch", "/etc", "/tmp/other"}
	if _, err := CheckDelete(batch, true, "/etc"); err == nil {
		t.Fatal("a protected path was deleted as part of a batch")
	}
}

func TestCheckDeleteOrdinaryPathsNeedNoTyping(t *testing.T) {
	paths := []string{"/home/alice/projects/old", "/tmp/build", "/var/log/app/2024.log"}
	got, err := CheckDelete(paths, true, "")
	if err != nil {
		t.Fatalf("ordinary recursive delete was blocked: %v", err)
	}
	if len(got) != len(paths) {
		t.Errorf("got %d paths, want %d", len(got), len(paths))
	}
}

// A non-recursive delete of a protected directory is just rmdir on an empty
// directory: it fails by itself if the directory has anything in it, so the
// typing ceremony is not warranted.
func TestCheckDeleteNonRecursiveProtectedIsAllowed(t *testing.T) {
	if _, err := CheckDelete([]string{"/etc"}, false, ""); err != nil {
		t.Errorf("non-recursive delete of /etc was blocked: %v", err)
	}
}

func TestCheckDeleteRejectsBadInput(t *testing.T) {
	if _, err := CheckDelete(nil, false, ""); err == nil {
		t.Error("empty delete request was accepted")
	}
	if _, err := CheckDelete([]string{"relative"}, false, ""); err == nil {
		t.Error("relative path was accepted")
	}
}

func TestDepth(t *testing.T) {
	cases := map[string]int{"/": 0, "/etc": 1, "/etc/ssh": 2, "/a/b/c": 3}
	for in, want := range cases {
		if got := Depth(in); got != want {
			t.Errorf("Depth(%q) = %d, want %d", in, got, want)
		}
	}
}
