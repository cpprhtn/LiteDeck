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

// The mark has to be planted by the read, not by a button on a strip that a
// first visit never draws.
//
// The only writer used to be that button. So LastSeen stayed zero, First stayed
// true, the strip returned null on every render, and "since you last looked"
// never appeared on any host for anybody — the feature could not be reached.
func TestDigestPlantsItsMarkOnTheFirstRead(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)

	if got := a.settings.Get().LastSeen["fixture"]; got != 0 {
		t.Fatalf("a fresh settings store already has a mark: %d", got)
	}

	first, err := a.HostDigest("fixture")
	if err != nil {
		t.Fatalf("HostDigest: %v", err)
	}
	// The first visit still says nothing: every count would be "since the
	// journal began" dressed up as news.
	if !first.First {
		t.Error("the first visit was not reported as one")
	}
	// But the mark is now on disk, which is what the second visit needs.
	mark := a.settings.Get().LastSeen["fixture"]
	if mark == 0 {
		t.Fatal("the first read left no mark — every later visit is a first visit again")
	}

	// A different connection, so the cache does not answer for it.
	a.digests = newDigestCache()
	second, err := a.HostDigest("fixture")
	if err != nil {
		t.Fatalf("HostDigest again: %v", err)
	}
	if second.First {
		t.Error("still a first visit after the mark was planted")
	}
	if second.Since != mark {
		t.Errorf("Since = %d, want the planted mark %d", second.Since, mark)
	}
	// Reading twice must not move the mark: the window would never cover
	// anything that happened while the app was closed.
	if now := a.settings.Get().LastSeen["fixture"]; now != mark {
		t.Errorf("the mark moved on a later read: %d then %d", mark, now)
	}
}
