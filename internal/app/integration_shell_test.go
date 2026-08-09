package app

import (
	"encoding/base64"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// `code` and `vi` caught before they are sent (§4.6a).
//
// The app answers them itself, so nothing runs on the server and nothing has to
// exist there — that is what makes them work on a Windows box with neither VS
// Code nor vi installed. All Go is asked for is the path, and only a relative
// one costs a question to the live shell.

func revealApp(t *testing.T) (*App, TerminalInfo) {
	t.Helper()
	a := connectedApp(t)
	if _, err := a.DetectHost("fixture"); err != nil {
		t.Fatalf("DetectHost: %v", err)
	}
	term, err := a.OpenTerminal("fixture", TerminalOptions{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	t.Cleanup(func() { _ = a.CloseTerminal(term.ID) })
	return a, term
}

func TestRevealFromTerminalResolvesPaths(t *testing.T) {
	a, term := revealApp(t)

	// Absolute: answered without asking the shell anything at all, which is why
	// it works even while something is running in that terminal.
	if got := a.RevealFromTerminal(term.ID, "/etc"); got.Path != "/etc" || !got.IsDir || got.Error != "" {
		t.Errorf("code /etc → %+v", got)
	}
	if got := a.RevealFromTerminal(term.ID, "/etc/hostname"); got.IsDir || got.Error != "" {
		t.Errorf("vi /etc/hostname → %+v", got)
	}

	// `vi test.cpp` on a file that is not there yet still opens an editor.
	if got := a.RevealFromTerminal(term.ID, "/tmp/fresh.cpp"); !got.New || got.Error != "" {
		t.Errorf("vi on a new file → %+v", got)
	}

	// Neither the file nor its directory exists: say so rather than navigate
	// somewhere arbitrary.
	if got := a.RevealFromTerminal(term.ID, "/nope/nope/nope"); got.Error == "" {
		t.Errorf("a typo reported no error: %+v", got)
	}
}

func TestRevealFromTerminalAsksTheShellForRelativePaths(t *testing.T) {
	a, term := revealApp(t)

	// Only the shell knows where it is standing, so this is the one case that
	// costs a question.
	if err := a.WriteTerminal(term.ID, base64.StdEncoding.EncodeToString([]byte("cd /var/log\n"))); err != nil {
		t.Fatalf("WriteTerminal: %v", err)
	}

	// `code .`
	if got := a.RevealFromTerminal(term.ID, ""); got.Path != "/var/log" || !got.IsDir {
		t.Errorf("code . in /var/log → %+v", got)
	}
	if got := a.RevealFromTerminal(term.ID, "."); got.Path != "/var/log" || !got.IsDir {
		t.Errorf("code . → %+v", got)
	}
	// A relative file below it.
	if got := a.RevealFromTerminal(term.ID, "../log"); got.Path != "/var/log" || !got.IsDir {
		t.Errorf("code ../log → %+v", got)
	}
}

func TestRevealFromTerminalRefusesAClosedTerminal(t *testing.T) {
	a, term := revealApp(t)
	if err := a.CloseTerminal(term.ID); err != nil {
		t.Fatalf("CloseTerminal: %v", err)
	}
	if got := a.RevealFromTerminal(term.ID, "/etc"); got.Error == "" {
		t.Error("resolved against a terminal that is gone")
	}
}

// Absolute means absolute on the *server*, and `C:\Users\KTJ` is a relative
// filename here. Getting this wrong would send a Windows path off to be joined
// onto a POSIX working directory.
func TestAbsolutePathsAreJudgedByTheServersRules(t *testing.T) {
	for _, tc := range []struct {
		path    string
		windows bool
		want    bool
	}{
		{"/etc/nginx", false, true},
		{"etc/nginx", false, false},
		{".", false, false},
		{`C:\Users\KTJ`, true, true},
		{`C:/Users/KTJ`, true, true},
		{`\\server\share`, true, true},
		{"/Users/KTJ", true, true},
		{`Documents\notes.txt`, true, false},
		// On a POSIX host a colon is just a character in a filename.
		{`C:\Users\KTJ`, false, false},
	} {
		if got := isAbsoluteRemote(tc.path, tc.windows); got != tc.want {
			t.Errorf("isAbsoluteRemote(%q, windows=%v) = %v, want %v",
				tc.path, tc.windows, got, tc.want)
		}
	}
}

func TestWindowsShellIsRecognised(t *testing.T) {
	a := New()
	a.detected.put("w", adapter.ServerInfo{Platform: adapter.PlatformWindows})
	if !a.isWindows("w") {
		t.Error("did not recognise a Windows host")
	}
	a.detected.put("l", adapter.ServerInfo{Platform: adapter.PlatformLinux})
	if a.isWindows("l") {
		t.Error("called a Linux host Windows")
	}
	// Never probed: POSIX is the safe assumption, and being wrong costs one
	// failed question rather than a broken terminal.
	if a.isWindows("unprobed") {
		t.Error("assumed Windows for a host nothing is known about")
	}
}
