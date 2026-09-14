package adapter

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The five Windows probes, run against a real machine and kept.
//
// # Why these exist alongside the hand-written fixtures
//
// The other tests in this package feed the parsers text written by hand. That
// catches a parser mishandling a shape somebody thought of, and misses
// everything else: a regex that stopped matching because this build of
// PowerShell words a property differently, a cmdlet that returns nothing on a
// newer release, a script that prints a warning ahead of its first record.
// Nothing in a hand-written fixture can tell you the *script* broke, because the
// person writing the fixture writes what the script is supposed to print.
//
// These files are what the scripts actually printed. `testdata/windows/capture.sh`
// records them by running `go run ./internal/adapter/cmd/winscripts`, so the text
// came from the same source the adapter ships and there is no second copy of a
// script to drift.
//
// # What is asserted, and what is not
//
// Record counts are read out of the fixture rather than written down, so a
// re-capture does not invalidate the test and a parser that silently drops
// records still fails. The literals are the things a new capture would not
// change: that all three firewall profiles are on, that the Korean rule names
// survived the transport, that the busiest attacker sorts first.
//
// Captured from Windows 10 Pro 19045, Korean install, OpenSSH for Windows, over
// the same -EncodedCommand transport the adapter uses. The account name and
// every routable address were replaced at capture time; the structure, the
// Korean text and the CRLF line endings are exactly as they arrived.
func windowsGolden(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "windows", "golden", "script-"+name+".out")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !strings.Contains(string(b), "\r\n") {
		// Not pedantry. Every one of these parsers strips the carriage return
		// itself, and a golden that lost it on the way into the tree stops
		// testing that. It has happened: the anonymiser in capture.sh read and
		// wrote through Python's newline translation and quietly converted
		// every file it touched.
		t.Fatalf("script-%s.out has no CRLF: the capture lost its line endings", name)
	}
	return string(b)
}

// goldenRecords counts the lines of one record type in a fixture.
func goldenRecords(raw, tag string) int {
	n := 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), tag+"|") {
			n++
		}
	}
	return n
}

// goldenField returns the nth field of the first line carrying a tag.
func goldenField(t *testing.T, raw, tag string, n int) string {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, tag+"|") {
			continue
		}
		if f := strings.Split(line, "|"); len(f) > n {
			return f[n]
		}
	}
	t.Fatalf("no %s record in the fixture", tag)
	return ""
}

func TestGoldenWindowsShells(t *testing.T) {
	got := ParseWindowsShells(windowsGolden(t, "shells"))
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	// cmd is unconditional, PowerShell was found, and the machine has one
	// distribution registered and default.
	want := []string{"cmd", "powershell", "wsl:Ubuntu-24.04"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("shells = %v, want %v", ids, want)
	}
}

// The probe was pointed at a distribution name that does not exist, so the
// recorded answer is the failure branch.
//
// WSLBroken is the zero value, so asserting it alone would also pass for a
// script that printed nothing at all. The token is checked too: that is what
// proves the script ran and reached its own conclusion.
func TestGoldenWindowsWSLProbe(t *testing.T) {
	raw := windowsGolden(t, "wslprobe")
	found := ""
	for _, tok := range []string{"WSL=ready", "WSL=starting", "WSL=hung", "WSL=broken"} {
		if strings.Contains(raw, tok) {
			found = tok
		}
	}
	if found == "" {
		t.Fatalf("the probe printed no verdict at all: %q", raw)
	}
	if got := ParseWSLProbe(raw); got != WSLBroken {
		t.Errorf("ParseWSLProbe(%s) = %v, want WSLBroken", found, got)
	}
}

func TestGoldenWindowsSessions(t *testing.T) {
	raw := windowsGolden(t, "sessions")
	start, err := strconv.ParseInt(goldenField(t, raw, "#self", 1), 10, 64)
	if err != nil {
		t.Fatalf("#self: %v", err)
	}
	self := int(start)

	// Four sshd processes were running when this was taken: the listener and a
	// per-connection parent, both SYSTEM; a pre-authentication helper owned by
	// sshd_NNNN; and one session. Only the last is a person.
	if n := goldenRecords(raw, "#proc"); n != 4 {
		t.Fatalf("the fixture has %d processes, not the four it was captured with", n)
	}

	// Five minutes after the login, so the elapsed time is a number and not
	// zero — a parser that lost the start time would pass at zero.
	at, err := strconv.ParseInt(goldenField(t, raw, "#auth", 1), 10, 64)
	if err != nil {
		t.Fatalf("#auth: %v", err)
	}
	got := ParseWindowsSessions(raw, time.Unix(at+300, 0))
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.PID != self {
		t.Errorf("session pid = %d, want %d", s.PID, self)
	}
	if s.User != "TESTUSER" {
		t.Errorf("User = %q, want the anonymised account name", s.User)
	}
	if !s.Self {
		t.Error("the capture ran inside this very session and it is not marked Self")
	}
	if s.From != "192.0.2.1" {
		t.Errorf("From = %q: the login record did not reach the process", s.From)
	}
	if s.Elapsed != 300 {
		t.Errorf("Elapsed = %d, want 300", s.Elapsed)
	}
}

func TestGoldenWindowsLogins(t *testing.T) {
	raw := windowsGolden(t, "logins")
	got := ParseWindowsLogins(raw)

	if !got.HasLog {
		t.Fatal("HasLog is false over a log full of records")
	}
	// Every #ok becomes a row, and the boot marker is one more — where `last`
	// puts it, in the same list, in time order.
	wantRows := goldenRecords(raw, "#ok") + goldenRecords(raw, "#boot")
	if len(got.Logins) != wantRows {
		t.Errorf("got %d rows, want %d (one per #ok, plus the boot marker)", len(got.Logins), wantRows)
	}
	boots := 0
	for _, l := range got.Logins {
		if l.Boot {
			boots++
		}
		// Windows writes no logout record, so nothing may be reported as still
		// open. Claiming otherwise would put a green dot beside a session that
		// ended days ago.
		if l.Open {
			t.Errorf("%+v is marked open; Windows cannot know that", l)
			break
		}
	}
	if boots != 1 {
		t.Errorf("got %d boot rows, want 1", boots)
	}

	// The aggregate is what the summary counts from, and it is deliberately
	// whole: the screen says "8 of 15" and cannot count the 15 from a list of 8.
	if got.Auth.Failed == 0 || got.Auth.Accepted != goldenRecords(raw, "#ok") {
		t.Errorf("Auth = %d failed / %d accepted, want a failure count and %d accepted",
			got.Auth.Failed, got.Auth.Accepted, goldenRecords(raw, "#ok"))
	}
	if got.Auth.DistinctSources <= AuthTopN || got.Auth.DistinctUsers <= AuthTopN {
		t.Fatalf("the fixture no longer has more than %d distinct sources and users: %d / %d",
			AuthTopN, got.Auth.DistinctSources, got.Auth.DistinctUsers)
	}
	if len(got.Auth.Sources) != AuthTopN || len(got.Auth.Users) != AuthTopN {
		t.Errorf("listed %d sources / %d users, want %d of each",
			len(got.Auth.Sources), len(got.Auth.Users), AuthTopN)
	}

	// The log is circular and 1 MB. On this machine, under a password attack,
	// it held a little under three hours — which is why the oldest record it
	// still has is reported rather than the window that was asked for.
	span, err := strconv.ParseInt(goldenField(t, raw, "#span", 1), 10, 64)
	if err != nil {
		t.Fatalf("#span: %v", err)
	}
	if got.Since.Unix() != span {
		t.Errorf("Since = %d, want the #span record %d", got.Since.Unix(), span)
	}
}

func TestGoldenWindowsSecurity(t *testing.T) {
	raw := windowsGolden(t, "security")
	got := ParseWindowsSecurity(raw)

	if !got.ProfilesRead || len(got.Profiles) != 3 {
		t.Fatalf("got %d profiles (read=%v), want Domain, Private and Public",
			len(got.Profiles), got.ProfilesRead)
	}
	for _, p := range got.Profiles {
		if !p.Enabled {
			t.Errorf("%s reads as disabled; all three were on", p.Name)
		}
	}

	total, err := strconv.Atoi(goldenField(t, raw, "#rulecount", 1))
	if err != nil {
		t.Fatalf("#rulecount: %v", err)
	}
	if !got.RulesRead || got.RuleTotal != total {
		t.Errorf("RuleTotal = %d (read=%v), want %d", got.RuleTotal, got.RulesRead, total)
	}

	// A Korean install names its built-in rules in Korean. These fixtures are
	// the only evidence in the tree that the encoding prelude survives the
	// transport all the way into a parsed struct.
	korean := false
	for _, r := range got.Rules {
		if strings.Contains(r.Name, "네트워크 검색") {
			korean = true
			break
		}
	}
	if !korean {
		t.Error("no rule kept its Korean name: the encoding prelude is not doing its job")
	}

	if got.Lockout == nil || got.Lockout.Threshold != 10 {
		t.Errorf("Lockout = %+v, want a threshold of 10", got.Lockout)
	}
	if got.Defender == nil || !got.Defender.Enabled {
		t.Errorf("Defender = %+v, want enabled", got.Defender)
	}
	if got.Failed == 0 {
		t.Error("Failed = 0 over a log the login tab counts hundreds of failures in")
	}
	if n := goldenRecords(raw, "#fail"); len(got.Failures) != n {
		t.Errorf("got %d failure buckets, want %d", len(got.Failures), n)
	}

	// The list is capped below the number of addresses that knocked, and it is
	// sorted: the busiest has to be first or the screen names the wrong one.
	if len(got.Attackers) == 0 {
		t.Fatal("nobody is listed as knocking on a log full of failures")
	}
	for i := 1; i < len(got.Attackers); i++ {
		if got.Attackers[i].Count > got.Attackers[i-1].Count {
			t.Errorf("attackers are not sorted: %+v", got.Attackers)
			break
		}
	}

	span, err := strconv.ParseInt(goldenField(t, raw, "#logspan", 1), 10, 64)
	if err != nil {
		t.Fatalf("#logspan: %v", err)
	}
	if got.LogSince == nil || got.LogSince.Unix() != span {
		t.Errorf("LogSince = %v, want the #logspan record %d", got.LogSince, span)
	}
}
