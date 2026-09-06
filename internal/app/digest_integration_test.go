package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// Against a real server.
//
// The first visit is the case worth pinning. There is no mark yet, so every
// count would be "since the beginning of the journal" — which on a server that
// has been up for months is a year of history presented as news.
func TestDigestFirstVisitDoesNotReportHistoryAsNews(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)

	first, err := a.HostDigest("fixture")
	if err != nil {
		t.Fatalf("HostDigest: %v", err)
	}
	t.Logf("first: %+v", first)
	if !first.First {
		t.Error("a host never looked at before was not reported as a first visit")
	}

	// After marking, the window starts from the mark rather than from the floor.
	if err := a.MarkHostSeen("fixture"); err != nil {
		t.Fatalf("MarkHostSeen: %v", err)
	}
	second, err := a.HostDigest("fixture")
	if err != nil {
		t.Fatalf("HostDigest after mark: %v", err)
	}
	t.Logf("second: %+v", second)
	if second.First {
		t.Error("the mark did not take")
	}
	if second.Since == 0 {
		t.Error("no mark came back after one was written")
	}
	if second.Readable && second.Window <= first.Window {
		t.Errorf("window did not move forward: %q then %q", first.Window, second.Window)
	}
	// Nothing happened in the moment between the two calls.
	if !second.Quiet {
		t.Errorf("counted %+v in the time between two calls", second.Digest)
	}
	// "I cannot read the journal" must not arrive as "nothing happened with a
	// window attached". The container cannot, and that is the case under test.
	if !second.Readable && second.Window != "" {
		t.Errorf("an unreadable journal came back with a window of %q", second.Window)
	}
}
