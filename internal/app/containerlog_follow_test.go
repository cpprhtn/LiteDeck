package app

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// Does a container follow actually follow? (§4.5)
//
// TestContainerLogs next door reads a bounded tail and passes whether or not
// the stream stays open, because it asks for output the container had already
// produced. The panel's claim is stronger than that: lines written *after* the
// window opened have to arrive without anyone pressing anything.

func TestFollowContainerLogKeepsDelivering(t *testing.T) {
	a := dockerApp(t)
	runInDind(t, "pull", "-q", "alpine:3.20")

	// One line a second, forever — so anything that arrives after the follow
	// starts is unambiguous, and its number says how long it kept going.
	runInDind(t, "run", "-d", "--name", "litedeck-ticker", "alpine:3.20",
		"sh", "-c", "i=0; while true; do i=$((i+1)); echo tick-$i; sleep 1; done")
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", dindID, "docker", "rm", "-f", "litedeck-ticker").Run()
	})

	var mu sync.Mutex
	var lines []string
	ended := make(chan string, 1)

	base := a.emit
	a.emit = func(event string, payload any) {
		switch {
		case strings.HasPrefix(event, "log:data:"):
			if l, ok := payload.(LogLine); ok {
				mu.Lock()
				lines = append(lines, l.Text)
				mu.Unlock()
			}
		case strings.HasPrefix(event, "log:exit:"):
			select {
			case ended <- payload.(string):
			default:
			}
		default:
			base(event, payload)
		}
	}

	// Let it build a backlog, so the tail is not empty and "new" is meaningful.
	time.Sleep(3 * time.Second)

	stream, err := a.FollowContainerLog("dind", "litedeck-ticker", 5)
	if err != nil {
		t.Fatalf("FollowContainerLog: %v", err)
	}
	t.Cleanup(func() { _ = a.StopLogStream(stream.ID) })

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(lines)
	}

	// Whatever the tail delivered is the baseline; the question is what comes
	// after it.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && count() == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	baseline := count()
	if baseline == 0 {
		t.Fatal("the follow delivered nothing at all")
	}

	// Eight seconds of a one-a-second ticker: several more lines, or the follow
	// is not following.
	time.Sleep(8 * time.Second)

	select {
	case msg := <-ended:
		t.Fatalf("the stream ended on its own after %d lines (%q) — the panel "+
			"then shows a frozen log until the user reopens it", baseline, msg)
	default:
	}

	if got := count(); got <= baseline {
		mu.Lock()
		seen := strings.Join(lines, " | ")
		mu.Unlock()
		t.Fatalf("no new lines in 8s: %d at the start, %d now [%s]", baseline, got, seen)
	}
}

// A follow on a container that is not running ends as soon as the tail is out.
//
// That is `docker logs -f` behaving correctly — there is nothing to follow —
// and it is why the panel has a Reconnect button rather than a retry loop.
// Retrying would reopen a channel every few seconds against a container that
// may never start, on a budget of nine.
func TestFollowContainerLogEndsOnAStoppedContainer(t *testing.T) {
	a := dockerApp(t)
	runInDind(t, "pull", "-q", "alpine:3.20")
	runInDind(t, "run", "--name", "litedeck-oneshot", "alpine:3.20",
		"sh", "-c", "echo first; echo second")
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", dindID, "docker", "rm", "-f", "litedeck-oneshot").Run()
	})

	ended := make(chan string, 1)
	var mu sync.Mutex
	var lines []string

	base := a.emit
	a.emit = func(event string, payload any) {
		switch {
		case strings.HasPrefix(event, "log:data:"):
			if l, ok := payload.(LogLine); ok {
				mu.Lock()
				lines = append(lines, l.Text)
				mu.Unlock()
			}
		case strings.HasPrefix(event, "log:exit:"):
			select {
			case ended <- payload.(string):
			default:
			}
		default:
			base(event, payload)
		}
	}

	stream, err := a.FollowContainerLog("dind", "litedeck-oneshot", 50)
	if err != nil {
		t.Fatalf("FollowContainerLog: %v", err)
	}
	t.Cleanup(func() { _ = a.StopLogStream(stream.ID) })

	select {
	case msg := <-ended:
		if msg != "" {
			t.Errorf("exit carried %q; a stopped container is not an error", msg)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the follow never ended on a container that is not running")
	}

	// And it delivered what the container did say before ending.
	mu.Lock()
	got := strings.Join(lines, "\n")
	mu.Unlock()
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("output = %q, want both lines", got)
	}
}
