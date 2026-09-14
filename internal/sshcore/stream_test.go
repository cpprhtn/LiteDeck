package sshcore

import (
	"strings"
	"sync"
	"testing"
)

// A line longer than the cap used to end the follow.
//
// bufio.Scanner returns ErrTooLong and stops, so the reader loop exited while
// the stream still held its channel slot: the pane looked alive and had stopped
// receiving. One newline-free JSON line from a container does it, and nothing
// after that line was ever shown.
func TestScanSurvivesAnOverlongLine(t *testing.T) {
	var mu sync.Mutex
	var got []string
	onLine := func(s string, _ bool) {
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	}

	huge := strings.Repeat("x", maxStreamLine+9000)
	input := "first\n" + huge + "\nafter\nlast\n"
	scan(strings.NewReader(input), false, onLine)

	if len(got) < 4 {
		t.Fatalf("got %d lines, want the two ordinary ones either side plus a note: %q", len(got), got)
	}
	if got[0] != "first" {
		t.Errorf("first line = %q", got[0])
	}
	// The lines after the long one are the point: they used to never arrive.
	tail := got[len(got)-2:]
	if tail[0] != "after" || tail[1] != "last" {
		t.Errorf("lines after the overlong one = %q — the follow stopped there", tail)
	}
	// And it says something rather than dropping it silently.
	var noted bool
	for _, l := range got {
		if strings.Contains(l, overlongNote) {
			noted = true
		}
	}
	if !noted {
		t.Error("the skipped line was dropped without a word")
	}
}
