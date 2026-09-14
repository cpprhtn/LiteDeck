package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

func variantGolden(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", dir, name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return string(b)
}

// OpenSSH 9.8 split the per-session work into its own binary, and the process
// title went with it. Debian 13, Fedora 41+, Ubuntu 25.04+ and Arch all ship it.
//
// Captured from a debian:trixie container running OpenSSH 10.0p2 with two
// accounts logged in — see testdata/golden/sessions/provenance-sshd-session.txt.
// The old fixture, ps-sshd.txt, has only the pre-9.8 titles, so it could not
// have caught this: matching `sshd:` alone made the sessions tab come back empty
// on every one of those distributions, and empty reads as "nobody is logged in".
func TestSessionsOnOpenSSHWithSplitSession(t *testing.T) {
	ps := variantGolden(t, "sessions", "ps-sshd-session.txt")

	// The fixture has to be the new shape or it is testing nothing.
	if !strings.Contains(ps, "sshd-session: deploy@pts/0") {
		t.Fatal("the fixture does not carry a split-session title")
	}

	got, err := ParseSSHSessions([]byte(ps), nil)
	if err != nil {
		t.Fatalf("ParseSSHSessions: %v", err)
	}
	byUser := map[string]SSHSession{}
	for _, s := range got {
		byUser[s.User] = s
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (deploy and litedeck): %+v", len(got), got)
	}
	for user, tty := range map[string]string{"deploy": "pts/0", "litedeck": "pts/1"} {
		s, ok := byUser[user]
		if !ok {
			t.Errorf("%s is not listed", user)
			continue
		}
		if s.TTY != tty {
			t.Errorf("%s is on %q, want %q", user, s.TTY, tty)
		}
	}

	// The listener is not a session. On this box it still carries the old
	// title, because only the per-session part moved.
	for _, s := range got {
		if strings.Contains(s.What, "[listener]") {
			t.Errorf("the listening daemon was counted as a session: %+v", s)
		}
	}
}

// The set of processes that must never be offered as a kill target has to know
// the new name too — it is what stops "end this session" from stopping sshd.
//
// The set is the daemon and the privileged halves. On this box the daemon still
// carries the old title and both halves carry the new one, so a scan that knew
// only `sshd:` would have let somebody kill a connection's privileged parent
// while believing they were ending one session.
func TestListenerPIDsOnOpenSSHWithSplitSession(t *testing.T) {
	ps := variantGolden(t, "sessions", "ps-sshd-session.txt")
	got := SessionListenerPIDs([]byte(ps))

	// 1 is the daemon, 50 and 62 are the per-connection privileged halves.
	for _, pid := range []int{1, 50, 62} {
		if !got[pid] {
			t.Errorf("pid %d is not protected; the set is %v", pid, got)
		}
	}
	// 69 and 76 are the sessions themselves, which the tab exists to end.
	for _, pid := range []int{69, 76} {
		if got[pid] {
			t.Errorf("pid %d is a session and must stay endable", pid)
		}
	}
}

// SelfAncestorsScript has to match /proc/PID/comm, which on these servers is
// "sshd-session" for everything but the daemon itself.
func TestSelfAncestorsScriptKnowsTheSplitName(t *testing.T) {
	if !strings.Contains(SelfAncestorsScript, `"sshd-session"`) {
		t.Error("the ancestor walk only looks for the pre-9.8 name")
	}
	// Measured in the container: two, because the listener is PID 1 there and
	// the loop stops before reaching it. On a real server the listener is not
	// PID 1 and the same walk returns three.
	got := ParseSelfAncestors([]byte(variantGolden(t, "sessions", "ancestors-sshd-session.txt")))
	if len(got) != 2 {
		t.Errorf("got %d ancestors, want the 2 the capture recorded: %v", len(got), got)
	}
}

// busybox has a `ps`. It answers none of the flags these two tabs send.
//
// Captured from alpine:3 (BusyBox v1.37.0) — see
// testdata/golden/busybox/provenance.txt. Alpine and most container images ship
// it, and the capabilities used to be unconditional, so both tabs opened and
// then printed "unrecognized option" on every poll.
func TestBusyboxRejectsTheProcpsFlagsForReal(t *testing.T) {
	stderr := variantGolden(t, "busybox", "ps-probe.err")
	if !strings.Contains(stderr, "unrecognized option") || !strings.Contains(stderr, "BusyBox") {
		t.Fatalf("the capture is not a busybox rejection:\n%s", stderr)
	}
	if out := variantGolden(t, "busybox", "ps-probe.out"); strings.TrimSpace(out) != "" {
		t.Errorf("busybox printed something on stdout: %q", out)
	}

	// Fed to Detect exactly as the server sent it, rather than to a stub that
	// paraphrases it. The earlier stub said "unrecognized option: e"; busybox
	// names the last argument, "no-headers".
	r := &fakeRunner{replies: map[string]sshcore.Result{
		"uname -s":                 {ExitCode: 0, Stdout: []byte("Linux\n")},
		"cat /etc/os-release":      {ExitCode: 0, Stdout: []byte("PRETTY_NAME=\"Alpine Linux v3.22\"\n")},
		"ps -eo pid= --no-headers": {ExitCode: 1, Stderr: []byte(stderr)},
	}}
	info, err := Detect(context.Background(), r)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.HasProcps {
		t.Fatal("busybox ps was taken for procps")
	}
	caps := info.Capabilities()
	for _, c := range []Capability{CapProcesses, CapSessions} {
		if caps[c] {
			t.Errorf("%v is on over a ps that cannot answer it", c)
		}
	}
}

// busybox's date has no relative parsing, and the ban-log filter uses it.
func TestBusyboxDateHasNoRelativeParsing(t *testing.T) {
	stderr := variantGolden(t, "busybox", "date-relative.err")
	if !strings.Contains(stderr, "invalid date") {
		t.Errorf("the capture does not show the failure:\n%s", stderr)
	}
	if out := variantGolden(t, "busybox", "date-relative.out"); strings.TrimSpace(out) != "" {
		t.Errorf("busybox printed a date after all: %q", out)
	}
}
