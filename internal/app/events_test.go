package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// An empty timeline has to say which kind of empty it is.
//
// A user outside systemd-journal/adm gets an empty journal with no error at
// all — journalctl answers successfully and says nothing. Rendered plainly that
// reads as "nothing has gone wrong here", which is the most expensive thing
// this feature could get wrong.
func TestEventsDistinguishUnreadableFromQuiet(t *testing.T) {
	a := connectedApp(t)

	info, err := a.DetectHost("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasSystemd {
		t.Skip("this fixture has no journal")
	}

	view, err := a.HostEvents("fixture", adapter.EventRangeDay, false)
	if err != nil {
		t.Fatalf("HostEvents: %v", err)
	}

	switch {
	case info.CanReadJournal:
		if view.Access != EventAccessOK {
			t.Errorf("access %q on a host this user can read", view.Access)
		}
	case info.HasSudo:
		if view.Access != EventAccessNeedsSudo {
			t.Errorf("access %q, want needs-sudo — the view cannot offer to "+
				"escalate without it", view.Access)
		}
		if len(view.Events) != 0 {
			t.Error("events were read despite the user not being able to read them")
		}
	default:
		if view.Access != EventAccessDenied {
			t.Errorf("access %q, want denied", view.Access)
		}
	}

	// Never nil: the view maps over this.
	if view.Events == nil {
		t.Error("Events is nil; it has to arrive as an empty list")
	}
}

// The range shown must be the range read, or the view labels a day of events
// as a week's.
func TestEventsEchoTheRange(t *testing.T) {
	a := connectedApp(t)
	for _, r := range []adapter.EventRange{
		adapter.EventRangeHour, adapter.EventRangeDay, adapter.EventRangeWeek,
	} {
		view, err := a.HostEvents("fixture", r, false)
		if err != nil {
			t.Fatalf("HostEvents(%s): %v", r, err)
		}
		if view.Range != r {
			t.Errorf("asked for %s, view says %s", r, view.Range)
		}
	}
}

// Nothing the user typed may reach journalctl's --since, which accepts free
// English and would be a poor thing to validate.
func TestEventRangeIsAClosedSet(t *testing.T) {
	for _, tc := range []struct {
		in   adapter.EventRange
		want string
	}{
		{adapter.EventRangeHour, "-1h"},
		{adapter.EventRangeDay, "-24h"},
		{adapter.EventRangeWeek, "-7d"},
		{adapter.EventRange("; rm -rf /"), "-24h"},
		{adapter.EventRange(""), "-24h"},
	} {
		if got := tc.in.Since(); got != tc.want {
			t.Errorf("%q → %q, want %q", tc.in, got, tc.want)
		}
	}
}

// journalctl announces "you are not seeing messages from other users" inside
// its own output. Without -q that notice is the first thing the parser reads.
func TestJournalArgsSilenceTheNotice(t *testing.T) {
	args := adapter.JournalArgs("-24h", 4, 10)
	var quiet, noPager bool
	for _, a := range args {
		switch a {
		case "-q":
			quiet = true
		case "--no-pager":
			noPager = true
		}
	}
	if !quiet {
		t.Error("-q missing: journalctl's notice would land in the JSON stream")
	}
	if !noPager {
		t.Error("--no-pager missing: the read can hang on a pager")
	}
}
