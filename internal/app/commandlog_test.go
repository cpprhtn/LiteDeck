package app

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

func poll(line string) sshcore.CommandInfo {
	return sshcore.CommandInfo{HostID: "h", Line: line, Kind: sshcore.CommandPoll}
}

func action(line string) sshcore.CommandInfo {
	return sshcore.CommandInfo{HostID: "h", Line: line}
}

func run(l *commandLog, info sshcore.CommandInfo, res *sshcore.Result) {
	l.CommandStarted(info)
	l.CommandFinished(info, res, nil)
}

const metricsPoll = "sh -c 'echo #stat; cat /proc/stat'"

// A view that refreshes every two seconds must not produce a row every two
// seconds.
func TestBackgroundPollsFoldIntoOneRow(t *testing.T) {
	l := newCommandLog(New())
	for range 300 {
		run(l, poll(metricsPoll), &sshcore.Result{})
	}
	if got := len(l.entries); got != 1 {
		t.Fatalf("%d rows for one repeated poll, want 1", got)
	}
	if got := l.entries[0].Repeat; got != 300 {
		t.Errorf("count %d, want 300 — the row is not saying how often it ran", got)
	}
	if l.entries[0].Status != "ok" {
		t.Errorf("status %q", l.entries[0].Status)
	}
}

// The bug this was written for: the log holds 500 entries, and at two pollers
// on a two second cadence the restart the user actually performed was gone
// within about eight minutes. The panel exists to answer "what did the GUI do
// on my behalf" and it was answering "it polled".
func TestBackgroundPollsDoNotEvictUserCommands(t *testing.T) {
	l := newCommandLog(New())
	run(l, action("docker compose --project-name web restart"), &sshcore.Result{})

	// Eight minutes of two pollers, several times over.
	for range 5000 {
		run(l, poll(metricsPoll), &sshcore.Result{})
		run(l, poll("docker ps --format '{{json .}}'"), &sshcore.Result{})
	}

	var found bool
	for _, e := range l.entries {
		if e.Line == "docker compose --project-name web restart" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the user's own command was evicted by polling; %d rows remain",
			len(l.entries))
	}
	// And the whole log stayed small, which is the point.
	if len(l.entries) != 3 {
		t.Errorf("%d rows, want 3 (one action + two folded polls)", len(l.entries))
	}
}

// A poll that starts failing has to be visible, not absorbed into the row that
// had been counting its successes.
//
// This is the v1.3.0 shape: nvidia-smi exiting 127 on every poll. Folding it in
// silently would recolour one row and leave the count meaning nothing.
func TestFailingBackgroundPollGetsItsOwnRow(t *testing.T) {
	l := newCommandLog(New())
	for range 10 {
		run(l, poll(metricsPoll), &sshcore.Result{})
	}
	for range 7 {
		run(l, poll(metricsPoll), &sshcore.Result{ExitCode: 127})
	}

	if got := len(l.entries); got != 2 {
		t.Fatalf("%d rows, want 2 — success and failure must not share one", got)
	}
	ok, bad := l.entries[0], l.entries[1]
	if ok.Status != "ok" || ok.Repeat != 10 {
		t.Errorf("success row: status %q count %d, want ok/10", ok.Status, ok.Repeat)
	}
	if bad.Status != "failed" || bad.Repeat != 7 {
		t.Errorf("failure row: status %q count %d, want failed/7", bad.Status, bad.Repeat)
	}
	if bad.ExitCode != 127 {
		t.Errorf("exit %d, want 127", bad.ExitCode)
	}
}

// Folding is for background reads only. What the user did stays one row per
// doing, in order, even when they do the same thing twice.
func TestUserCommandsAreNeverFolded(t *testing.T) {
	l := newCommandLog(New())
	for range 3 {
		run(l, action("systemctl restart nginx"), &sshcore.Result{})
	}
	if got := len(l.entries); got != 3 {
		t.Fatalf("%d rows for three restarts, want 3 — the user did it three times",
			got)
	}
	for _, e := range l.entries {
		if e.Repeat != 0 {
			t.Errorf("a user command carries a repeat count of %d", e.Repeat)
		}
	}
}

// Clearing must not leave the fold map pointing at rows that are gone, or the
// next poll updates nothing and the panel stops moving.
func TestClearResetsFolding(t *testing.T) {
	a := New()
	l := newCommandLog(a)
	a.log = l
	run(l, poll(metricsPoll), &sshcore.Result{})
	a.ClearCommandLog()
	run(l, poll(metricsPoll), &sshcore.Result{})

	if got := len(l.entries); got != 1 {
		t.Fatalf("%d rows after clearing and polling once, want 1", got)
	}
	if got := l.entries[0].Repeat; got != 1 {
		t.Errorf("count %d after a clear, want 1 — the count survived the clear", got)
	}
}

// Distinct commands keep distinct rows; folding is per command, not per kind.
func TestFoldingIsPerCommand(t *testing.T) {
	l := newCommandLog(New())
	for i := range 4 {
		for range 50 {
			run(l, poll(fmt.Sprintf("read-%d", i)), &sshcore.Result{})
		}
	}
	if got := len(l.entries); got != 4 {
		t.Fatalf("%d rows for four distinct polls, want 4", got)
	}
}

// An fs_write carries the whole new file in its arguments, and the line built
// from them used to carry it too — into memory, into the webview, and into a
// panel whose only job is to stay scannable.
func TestAICallLineDoesNotCarryAWholeFile(t *testing.T) {
	a := appWithSettings(t)
	content := strings.Repeat("server { listen 80; }\n", 5000) // ~110 KB

	a.logAICall("fs_write", map[string]any{
		"hostId": "h", "path": "/etc/nginx/nginx.conf", "content": content,
	})

	entries := a.CommandLog()
	if len(entries) != 1 {
		t.Fatalf("%d rows, want 1", len(entries))
	}
	line := entries[0].Line
	if len(line) > 600 {
		t.Errorf("the log line is %d bytes — the file body is still in it", len(line))
	}
	// The parts that answer "what did the AI ask for" have to survive.
	for _, want := range []string{"fs_write", "/etc/nginx/nginx.conf", "hostId=h"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lost %q: %s", want, line)
		}
	}
	// "it rewrote 40 bytes" and "it rewrote 110 kilobytes" are different events,
	// and the clipped line is the only place that difference is still visible.
	if !strings.Contains(line, strconv.Itoa(len(content))) {
		t.Errorf("line does not say how much was written: %s", line)
	}
}

// A short argument is not touched — clipping everything would put an ellipsis
// on every path in the panel.
func TestAICallKeepsShortArgumentsWhole(t *testing.T) {
	a := appWithSettings(t)
	a.logAICall("svc_control", map[string]any{
		"hostId": "h", "unit": "nginx.service", "action": "restart",
	})
	line := a.CommandLog()[0].Line
	if strings.Contains(line, "…") {
		t.Errorf("a short call was clipped: %s", line)
	}
	if line != "svc_control(action=restart, hostId=h, unit=nginx.service)" {
		t.Errorf("line = %q", line)
	}
}

// Both clippers cut on rune boundaries. A server answering in Korean is the
// normal case here, not an edge one, and half a character at the end of the
// line is what byte slicing produces.
func TestClippingDoesNotSplitARune(t *testing.T) {
	t.Run("stderr", func(t *testing.T) {
		// 30 hangul syllables = 90 bytes; cutting at 100 is fine, at 50 is not.
		got := truncate(strings.Repeat("한", 40), 50)
		if !utf8.ValidString(got) {
			t.Errorf("truncate produced invalid UTF-8: %q", got)
		}
	})
	t.Run("mcp argument", func(t *testing.T) {
		// Shifted by an ASCII prefix each time. A hangul syllable is three
		// bytes, so a byte cut at the limit lands on a boundary for exactly one
		// of these three — and a test that happened to pick that one would pass
		// against a byte-slicing implementation. It did, before this loop.
		for pad := range 3 {
			in := strings.Repeat("x", pad) + strings.Repeat("한", 400)
			got := clipArg(in)
			if !utf8.ValidString(got) {
				t.Errorf("pad %d: clipArg produced invalid UTF-8: %q", pad, got)
			}
			if !strings.Contains(got, strconv.Itoa(len(in))+" bytes") {
				t.Errorf("pad %d: size note lost: %q", pad, got)
			}
		}
	})
}
