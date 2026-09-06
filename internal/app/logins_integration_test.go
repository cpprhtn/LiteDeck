package app

import (
	"strings"
	"testing"
)

// Against a real server.
//
// The container has no wtmp history and no sshd of its own, so what this pins
// is the shape rather than the contents: the two halves come back separately,
// and the failure half is labelled rather than reported as zero when it cannot
// be read.
func TestLoginsSplitsTheTwoHalves(t *testing.T) {
	a := connectedApp(t)

	view, err := a.HostLogins("fixture", false)
	if err != nil {
		t.Fatalf("HostLogins: %v", err)
	}
	t.Logf("logins=%d access=%q failed=%d accepted=%d",
		len(view.Logins), view.Access, view.Auth.Failed, view.Auth.Accepted)

	if view.Window != authWindow {
		t.Errorf("window = %q, want %q — the screen must not label a day as an hour",
			view.Window, authWindow)
	}
	if view.Access == "" {
		t.Error("the failure half came back with no access answer at all")
	}
	// The one thing that must never happen: a confident zero from a journal
	// nobody could read.
	if view.Access != EventAccessOK && view.Auth.Failed != 0 {
		t.Errorf("counted %d failures while access was %q", view.Auth.Failed, view.Access)
	}
}

// The script's two sections are found by marker, not by counting lines — `last`
// prints a variable number of rows and a trailing "wtmp begins".
func TestLoginsOutputSplitsOnMarkers(t *testing.T) {
	const out = "#last\nroot pts/0 10.0.0.1 Sun Sep 6 20:00:00 2026   still logged in\n" +
		"\nwtmp begins Sun Jan 12 23:08:18 2025\n#auth\ntotal 12 3\nip 9 10.0.0.9\n"

	last, auth := splitLoginsOutput(out)
	if !strings.Contains(last, "still logged in") || strings.Contains(last, "total 12") {
		t.Errorf("last section = %q", last)
	}
	if !strings.Contains(auth, "total 12 3") || strings.Contains(auth, "wtmp begins") {
		t.Errorf("auth section = %q", auth)
	}
}

// A server that answers nothing at all must not look like a server with a clean
// record.
func TestLoginsOutputToleratesSilence(t *testing.T) {
	for _, out := range []string{"", "\n", "unexpected preamble\n"} {
		last, auth := splitLoginsOutput(out)
		if last != "" || auth != "" {
			t.Errorf("%q gave last=%q auth=%q, want both empty", out, last, auth)
		}
	}
}
