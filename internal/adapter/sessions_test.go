package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessionGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "sessions", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return b
}

// TestParseSSHSessions runs against a real capture: the systemd fixture with two
// users logged in over SSH plus the probe's own connection.
func TestParseSSHSessions(t *testing.T) {
	ps := sessionGolden(t, "ps-all.txt")
	self := ParseSelfAncestors(sessionGolden(t, "ancestors.txt"))

	got, err := ParseSSHSessions(ps, self)
	if err != nil {
		t.Fatalf("ParseSSHSessions: %v", err)
	}
	if got == nil {
		t.Fatal("nil slice; the frontend unmounts on JSON null")
	}

	byPID := map[int]SSHSession{}
	for _, s := range got {
		byPID[s.PID] = s
	}

	// The two interactive logins, and nothing else with a terminal.
	var interactive []string
	for _, s := range got {
		if s.Interactive() {
			interactive = append(interactive, s.User+"@"+s.TTY)
		}
	}
	if len(interactive) != 2 {
		t.Errorf("interactive sessions = %v, want two (litedeck and deploy)", interactive)
	}

	users := map[string]bool{}
	for _, s := range got {
		users[s.User] = true
	}
	for _, want := range []string{"litedeck", "deploy"} {
		if !users[want] {
			t.Errorf("%s missing from the session list", want)
		}
	}

	// The privileged halves and the listening daemon are not sessions and must
	// never appear as rows someone can act on.
	for _, s := range got {
		if strings.Contains(s.User, "[priv]") || strings.Contains(s.User, "listener") {
			t.Errorf("non-session row leaked in: %+v", s)
		}
	}
	if _, ok := byPID[39]; ok {
		t.Error("the listening daemon (pid 39) was listed as a session")
	}

	// Elapsed comes from ps etimes.
	for _, s := range got {
		if s.Elapsed < 0 {
			t.Errorf("negative elapsed on %+v", s)
		}
	}
}

// TestSelfSessionIsMarked is the safety property. A session belonging to the
// connection LiteDeck is using must be flagged, whether it is the process the
// probe ran under or a sibling created for another command on the same
// connection.
func TestSelfSessionIsMarked(t *testing.T) {
	ps := sessionGolden(t, "ps-all.txt")

	// The capture's ancestors are from a different connection than the one in
	// ps-all.txt — both are real, they were taken seconds apart. So the property
	// is tested by asserting on the mechanism rather than on that one capture:
	// a PID in the ancestor set, and a PID whose parent is in it, are both self.
	sessions, err := ParseSSHSessions(ps, map[int]bool{})
	if err != nil {
		t.Fatalf("ParseSSHSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("no sessions in the capture")
	}

	// Direct hit: the session process itself is an ancestor.
	target := sessions[0]
	marked, _ := ParseSSHSessions(ps, map[int]bool{target.PID: true})
	if !findPID(marked, target.PID).Self {
		t.Errorf("pid %d not marked self when it is in the ancestor set", target.PID)
	}

	// Parent hit: the transient per-command session processes hang off the
	// connection's privileged process, so matching only on the session PID would
	// miss them.
	marked, _ = ParseSSHSessions(ps, map[int]bool{target.PPID: true})
	if !findPID(marked, target.PID).Self {
		t.Errorf("pid %d not marked self when its parent %d is an ancestor",
			target.PID, target.PPID)
	}

	// And nothing else gets swept up.
	var others int
	for _, s := range marked {
		if s.PID != target.PID && s.Self && s.PPID != target.PPID {
			others++
		}
	}
	if others > 0 {
		t.Errorf("%d unrelated sessions marked self", others)
	}
}

func findPID(ss []SSHSession, pid int) SSHSession {
	for _, s := range ss {
		if s.PID == pid {
			return s
		}
	}
	return SSHSession{}
}

func TestParseSelfAncestors(t *testing.T) {
	got := ParseSelfAncestors(sessionGolden(t, "ancestors.txt"))
	if len(got) == 0 {
		t.Fatal("no ancestors parsed")
	}
	// The listening daemon is always in the chain, and killing it stops sshd for
	// everyone — the same class of mistake as signalling PID 1.
	if !got[39] {
		t.Errorf("the listener (pid 39) is not in the ancestor set: %v", got)
	}
	for _, junk := range []string{"", "abc", "0", "-1", "  "} {
		if len(ParseSelfAncestors([]byte(junk))) != 0 {
			t.Errorf("ParseSelfAncestors(%q) produced entries", junk)
		}
	}
}

// TestSessionListenerPIDs pins the processes that must never be offered as
// targets, using the real capture's three-layer structure: listener, privileged
// halves, session processes.
func TestSessionListenerPIDs(t *testing.T) {
	got := SessionListenerPIDs(sessionGolden(t, "ps-all.txt"))
	if !got[39] {
		t.Error("listener not identified")
	}
	// The privileged halves in the capture.
	for _, pid := range []int{378, 379, 405} {
		if !got[pid] {
			t.Errorf("privileged process %d not identified", pid)
		}
	}
	// The session processes must NOT be in this set — they are what the view
	// lists.
	for _, pid := range []int{400, 401, 416} {
		if got[pid] {
			t.Errorf("session process %d wrongly treated as a listener", pid)
		}
	}
}

// TestParseSSHPeers covers both privilege levels, because they produce different
// output and only one of them carries the PID.
func TestParseSSHPeers(t *testing.T) {
	root := ParseSSHPeers(sessionGolden(t, "ss-root.txt"))
	if len(root) == 0 {
		t.Fatal("no peers from the privileged capture")
	}
	for pid, peer := range root {
		if pid <= 0 || !strings.Contains(peer, ":") {
			t.Errorf("bad entry %d → %q", pid, peer)
		}
	}

	// Without privileges ss omits the process entirely. The addresses are still
	// there, but nothing maps them to a session — the column is blank rather than
	// the view being broken.
	noroot := ParseSSHPeers(sessionGolden(t, "ss-noroot.txt"))
	if len(noroot) != 0 {
		t.Errorf("unprivileged ss produced %d mappings; it has no pid= to read", len(noroot))
	}
}

// TestEmptySourcesDegrade is the finding that shaped this whole file: w, who and
// loginctl all came back empty on a machine with three live SSH sessions, because
// a container writes neither utmp nor logind records. A view built on them would
// have reported "nobody is logged in" while three people were.
func TestEmptySourcesDegrade(t *testing.T) {
	for _, name := range []string{"w-empty.txt", "who-empty.txt", "loginctl-empty.json"} {
		b := sessionGolden(t, name)
		if len(strings.TrimSpace(strings.Trim(string(b), "[]"))) != 0 {
			t.Fatalf("%s is not empty; the fixture changed and this test no longer covers the case", name)
		}
	}

	from, idle, what := ParseWIdle(sessionGolden(t, "w-empty.txt"))
	if len(from) != 0 || len(idle) != 0 || len(what) != 0 {
		t.Error("empty w produced entries")
	}
	if got := ParseWho(sessionGolden(t, "who-empty.txt")); len(got) != 0 {
		t.Errorf("empty who produced %d entries", len(got))
	}

	// And the session list is still complete without them.
	got, err := ParseSSHSessions(sessionGolden(t, "ps-all.txt"), nil)
	if err != nil {
		t.Fatalf("ParseSSHSessions: %v", err)
	}
	if len(got) < 2 {
		t.Errorf("only %d sessions without w/who/loginctl; ps alone has to be enough", len(got))
	}
}

func TestParseWIdle(t *testing.T) {
	// Shape taken from `w -h` on a machine that does write utmp. Columns are
	// USER TTY FROM LOGIN@ IDLE JCPU PCPU WHAT.
	sample := "deploy   pts/1    10.0.3.7         09:15    4:02m  0.04s  0.04s -bash\n" +
		"ktj      pts/0    192.0.2.14       14:22    0.00s  0.05s  0.00s w -h\n" +
		"root     tty1     -                08:00    2days  0.01s  0.01s -bash\n"
	from, idle, what := ParseWIdle([]byte(sample))
	if idle["pts/1"] != "4:02m" {
		t.Errorf("idle[pts/1] = %q", idle["pts/1"])
	}
	if what["pts/0"] != "w -h" {
		t.Errorf("what[pts/0] = %q", what["pts/0"])
	}
	// The origin is in this output and used to be thrown away, which left the
	// column blank on every server LiteDeck logs into as an ordinary user.
	if from["pts/1"] != "10.0.3.7" {
		t.Errorf("from[pts/1] = %q, want 10.0.3.7", from["pts/1"])
	}
	if from["pts/0"] != "192.0.2.14" {
		t.Errorf("from[pts/0] = %q, want 192.0.2.14", from["pts/0"])
	}
	// A local login has no origin. "-" is the absence of an answer, not an answer.
	if v, ok := from["tty1"]; ok {
		t.Errorf("from[tty1] = %q; a dash is not an origin", v)
	}
}

// TestParseWho covers the source that kept working where w did not.
func TestParseWho(t *testing.T) {
	// Shape taken from `who -u` on the demo fixture, where `w -h` printed nothing
	// at all for the same four logins. The columns before the origin move around
	// between builds — LOGIN@ splits differently, IDLE is "." or a clock, the PID
	// is optional — so only the parenthesised tail is anchored on.
	sample := "deploy   pts/2        Aug 10 15:13   .          1458 (192.168.215.1)\n" +
		"ktj      pts/3        2026-08-10 15:13 00:12      1459 (jump.example.net)\n" +
		"root     tty1         Aug 10 09:02   old         912\n" +
		"gdm      tty2         Aug 10 09:02   ?           940 (:0)\n"
	got := ParseWho([]byte(sample))
	if got["pts/2"] != "192.168.215.1" {
		t.Errorf("pts/2 = %q, want 192.168.215.1", got["pts/2"])
	}
	if got["pts/3"] != "jump.example.net" {
		t.Errorf("pts/3 = %q, want jump.example.net", got["pts/3"])
	}
	// No parentheses at all: a console login with nowhere to have come from.
	if v, ok := got["tty1"]; ok {
		t.Errorf("tty1 = %q; that line has no origin", v)
	}
	// ":0" is the local X display, not a remote host.
	if v, ok := got["tty2"]; ok {
		t.Errorf("tty2 = %q; the local display is not an origin", v)
	}
}

// TestSSArgsUseTheHostsPort pins the defect this pass fixed: the filter named
// port 22 outright, so on any server that moved sshd the probe matched nothing
// and every session's origin came back blank with no error to explain it.
func TestSSArgsUseTheHostsPort(t *testing.T) {
	joined := strings.Join(SSArgsForPort(2222), " ")
	if !strings.Contains(joined, "sport = :2222") {
		t.Errorf("args = %q; want a filter on the port that was asked for", joined)
	}
	if strings.Contains(joined, ":22 ") || strings.HasSuffix(joined, ":22)") {
		t.Errorf("args = %q; port 22 is still hard-coded", joined)
	}
	// Zero is "the host record is gone"; the default is the only sane guess.
	if !strings.Contains(strings.Join(SSArgsForPort(0), " "), "sport = :22") {
		t.Error("an unknown port should fall back to 22")
	}
}

func TestKillSessionArgs(t *testing.T) {
	got := KillSessionArgs(1234)
	want := []string{"-TERM", "--", "1234"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
	// `--` is not optional: without it a negative PID reads as an option and
	// `kill -TERM -1` signals every process the user can reach.
	if got[1] != "--" {
		t.Error("missing -- before the pid")
	}
}
