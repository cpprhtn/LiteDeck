package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// The rule this whole source exists to get right: a line that could not be read
// might have been a `cd`, so everything recorded after it says "about here"
// rather than "here".
//
// A recalled command is the ordinary case, not the edge case. Somebody who
// presses the up arrow twice and hits enter has moved, possibly, and this side
// cannot tell. Carrying the old path forward as fact is how a history becomes
// confidently wrong, which is worse than one that admits it does not know.
func TestBlindLineCostsThePathItsConfidence(t *testing.T) {
	l := newTypedLog(t.TempDir())
	l.setCwd("term1", "/srv/app")

	first := l.enter("h", "term1", "ls -la", false)
	if first == nil || first.PWD != "/srv/app" || !first.PWDCertain {
		t.Fatalf("a readable line after an anchor should be certain: %+v", first)
	}

	// An arrow key, a Tab completion, a Ctrl-R. Nothing is recorded for it.
	if got := l.enter("h", "term1", "", true); got != nil {
		t.Errorf("an unreadable line was recorded as a command: %+v", got)
	}

	after := l.enter("h", "term1", "systemctl restart app", false)
	if after == nil {
		t.Fatal("nothing recorded after the blind line")
	}
	if after.PWD != "/srv/app" {
		t.Errorf("the path was thrown away rather than doubted: %q", after.PWD)
	}
	if after.PWDCertain {
		t.Error("the path is still claimed as certain after a line nobody could read")
	}

	// A readable cd is the only thing that earns the confidence back.
	back := l.enter("h", "term1", "cd /etc/nginx", false)
	if back == nil || back.PWD != "/etc/nginx" || !back.PWDCertain {
		t.Errorf("a readable cd did not re-anchor: %+v", back)
	}
}

func TestCdFormsThisCannotFollowLeaveThePathAlone(t *testing.T) {
	for _, line := range []string{
		"cd $DEPLOY_DIR", // the value is the shell's, not ours
		"cd -",           // needs the previous directory, which is not tracked
		"cd ~/x/../y",    // fine to resolve, but home is not known here
		"cd a b",         // not a cd this can reason about
		"cd 'my dir'",    // quoting this side does not parse
	} {
		l := newTypedLog(t.TempDir())
		l.setCwd("t", "/start")
		got := l.enter("h", "t", line, false)
		if got == nil {
			t.Fatalf("%q was not recorded at all", line)
		}
		if line == "cd ~/x/../y" {
			// The tilde is kept as written rather than expanded to a guess.
			if got.PWD != "~/x/../y" && got.PWD != "/start" {
				t.Errorf("%q moved the path to %q", line, got.PWD)
			}
			continue
		}
		if got.PWD != "/start" {
			t.Errorf("%q moved the path to %q; it should have stayed", line, got.PWD)
		}
	}
}

func TestRelativeCdIsJoinedOntoTheCurrentPath(t *testing.T) {
	l := newTypedLog(t.TempDir())
	l.setCwd("t", "/srv")
	if got := l.enter("h", "t", "cd app/config", false); got.PWD != "/srv/app/config" {
		t.Errorf("pwd = %q, want /srv/app/config", got.PWD)
	}
	if got := l.enter("h", "t", "cd ..", false); got.PWD != "/srv/app" {
		t.Errorf("pwd = %q, want /srv/app", got.PWD)
	}
	if got := l.enter("h", "t", "cd /var/log", false); got.PWD != "/var/log" {
		t.Errorf("pwd = %q, want /var/log", got.PWD)
	}
}

// Without an anchor there is no path at all, and none is claimed.
func TestNoAnchorMeansNoPath(t *testing.T) {
	l := newTypedLog(t.TempDir())
	got := l.enter("h", "t", "uptime", false)
	if got.PWD != "" || got.PWDCertain {
		t.Errorf("%+v: a path was invented with nothing to base it on", got)
	}
}

// Two terminals on one host stand in two different directories.
func TestTerminalsTrackTheirOwnDirectory(t *testing.T) {
	l := newTypedLog(t.TempDir())
	l.setCwd("a", "/one")
	l.setCwd("b", "/two")
	if got := l.enter("h", "a", "ls", false); got.PWD != "/one" {
		t.Errorf("terminal a is in %q", got.PWD)
	}
	if got := l.enter("h", "b", "ls", false); got.PWD != "/two" {
		t.Errorf("terminal b is in %q", got.PWD)
	}
}

func TestHistorySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	l := newTypedLog(dir)
	l.setCwd("t", "/srv")
	l.enter("h", "t", "systemctl restart app", false)

	// "What did I do here last time" is the question, so it has to outlive the
	// app being closed.
	again := newTypedLog(dir)
	rows := again.list("h")
	if len(rows) != 1 || rows[0].Command != "systemctl restart app" {
		t.Fatalf("history did not survive: %+v", rows)
	}
	if rows[0].Effect != adapter.SudoChange {
		t.Errorf("effect = %q, want change", rows[0].Effect)
	}
	// The directory is deliberately not persisted: a shell that is gone is not
	// standing anywhere, and restoring the path would claim otherwise.
	if got := again.enter("h", "t", "ls", false); got.PWD != "" {
		t.Errorf("a path survived the session that gave it meaning: %q", got.PWD)
	}
}

func TestHistoryIsNewestFirstAndBounded(t *testing.T) {
	l := newTypedLog(t.TempDir())
	for i := 0; i < typedLogMax+50; i++ {
		l.enter("h", "t", "echo "+string(rune('a'+i%26)), false)
	}
	rows := l.list("h")
	if len(rows) != typedLogMax {
		t.Errorf("kept %d rows, want the cap %d", len(rows), typedLogMax)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].At.After(rows[i-1].At) {
			t.Fatalf("row %d is newer than the one before it", i)
		}
	}
}
