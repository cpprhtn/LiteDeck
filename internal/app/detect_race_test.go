package app

import (
	"strings"
	"sync"
	"testing"
)

// Detection is cached, but the cache was only locked around the two ends: a
// reader that missed released the lock, ran the whole probe set, and only then
// wrote the answer. Every caller that missed in that window did the same, so
// three views mounting at once meant three full detections — several round
// trips each, on a server the app is meant to go easy on.
//
// It shows up in the sudo journal, which is where this was actually noticed:
// `sudo -n true` appeared three times in the same second, and on a host that
// needs a password each one is an alert-priority "a password is required".
// That journal is what the command history feature reads, so the app was
// filling its own source with its own noise.
func TestConcurrentDetectionProbesOnce(t *testing.T) {
	a := connectedApp(t)

	const callers = 6
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.DetectHost("fixture"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("DetectHost: %v", err)
	}

	// `uname -s` is the first thing every detection runs, so counting it counts
	// detections. Background reads fold into one row carrying the count.
	runs := 0
	for _, e := range a.CommandLog() {
		if !strings.Contains(e.Line, "uname -s") {
			continue
		}
		if e.Repeat > 0 {
			runs += e.Repeat
		} else {
			runs++
		}
	}
	if runs != 1 {
		t.Errorf("detection ran %d times for %d concurrent callers, want 1 — "+
			"the cache does not hold anyone off while it is being filled", runs, callers)
	}
}
