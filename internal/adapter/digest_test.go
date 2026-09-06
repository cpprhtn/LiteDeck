package adapter

import (
	"strings"
	"testing"
)

// The three lines one real reboot wrote, verbatim off a server.
//
// An unanchored "Startup finished" match counts this as three restarts. Two of
// them are user instances — systemd[1248] and systemd[1876] — which say the
// same words about themselves that PID 1 says about the machine.
const realBootLines = `2026-06-20T16:27:58+09:00 web-01 systemd[1248]: Startup finished in 9.778s.
2026-06-20T16:28:10+09:00 web-01 systemd[1]: Startup finished in 10.156s (firmware) + 4.792s (loader) + 3.286s (kernel) + 1min 8.229s (userspace) = 1min 26.465s.
2026-06-20T17:20:29+09:00 web-01 systemd[1876]: Startup finished in 124ms.`

func TestDigestScriptCountsPIDOneOnly(t *testing.T) {
	// The fixture is here to be read by a person changing the pattern: these
	// are the three lines the anchor exists for.
	if n := strings.Count(realBootLines, "Startup finished"); n != 3 {
		t.Fatalf("the captured boot has %d such lines, not 3", n)
	}
	if n := strings.Count(realBootLines, "systemd[1]: Startup finished"); n != 1 {
		t.Fatalf("only one of them is PID 1; found %d", n)
	}
	// The pattern lives in the script, so what is pinned here is that the
	// anchor is still in it. Losing the [1] is a one-character edit that turns
	// one restart into three and looks harmless in a diff.
	if !strings.Contains(DigestScript, `systemd\[1\]: Startup finished`) {
		t.Error("the boot pattern is no longer anchored on PID 1 — a user instance " +
			"says 'Startup finished' too, three times per boot on a measured server")
	}
	// journalctl does not print this under -q -o short-iso. Matching on it is a
	// counter that is always zero and looks like a server that never restarts.
	if strings.Contains(DigestScript, "-- Boot") {
		t.Error("the script matches journalctl's boot separator, which it never prints here")
	}
	// The window has to stay an argument. Pasting it in is how the argv-only
	// rule gets broken later by a change that looks like a simplification.
	if !strings.Contains(DigestScript, `since="$1"`) {
		t.Error("the window is no longer taken as an argument")
	}
}

func TestParseDigestReadsTheCounts(t *testing.T) {
	d := ParseDigest("boots 1\nauthFailures 2911\nsudoCommands 19\nunitFailures 4\n")
	if d.Boots != 1 || d.AuthFailures != 2911 || d.SudoCommands != 19 || d.UnitFailures != 4 {
		t.Errorf("%+v", d)
	}
	if d.Quiet() {
		t.Error("a server that restarted and failed four units is not quiet")
	}
}

func TestQuietIsAllFourZero(t *testing.T) {
	if !ParseDigest("boots 0\nauthFailures 0\nsudoCommands 0\nunitFailures 0\n").Quiet() {
		t.Error("all zeroes is quiet")
	}
	// One is enough to be worth saying.
	if ParseDigest("boots 0\nauthFailures 0\nsudoCommands 1\nunitFailures 0\n").Quiet() {
		t.Error("one sudo command is something that happened")
	}
}

func TestParseDigestIgnoresNoise(t *testing.T) {
	d := ParseDigest("some warning from the shell\nboots 2\ngarbage\nunitFailures x\n")
	if d.Boots != 2 {
		t.Errorf("boots = %d, want 2", d.Boots)
	}
	if d.UnitFailures != 0 {
		t.Errorf("a non-numeric count became %d", d.UnitFailures)
	}
}
