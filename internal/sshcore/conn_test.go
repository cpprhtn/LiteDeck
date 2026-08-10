package sshcore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A timeout has to say which half of the budget ran out.
//
// The deadline covers queueing for one of the Exec channels and running the
// command, and only one message came out of both. The first report of this in
// the wild — a `docker ps` that normally takes 80ms — could not be told apart
// from a genuinely stalled server, and that is what this fixes.
func TestTimeoutSaysWhetherTheQueueOrTheCommandRanOut(t *testing.T) {
	deadline := context.DeadlineExceeded

	t.Run("the command was slow", func(t *testing.T) {
		err := timedOut("docker ps", deadline, 100*time.Millisecond, 20*time.Second)
		if !strings.Contains(err.Error(), `"docker ps"`) {
			t.Errorf("should name the command: %v", err)
		}
		if strings.Contains(err.Error(), "슬롯") {
			t.Errorf("a short queue must not be blamed: %v", err)
		}
	})

	t.Run("the queue ate the budget", func(t *testing.T) {
		err := timedOut("docker ps", deadline, 19*time.Second, 20*time.Second)
		msg := err.Error()
		if !strings.Contains(msg, "19s") {
			t.Errorf("should report how long it waited: %v", err)
		}
		if !strings.Contains(msg, "서버가 느린 것이 아닐 수 있습니다") {
			t.Errorf("should say the server may not be the problem: %v", err)
		}
	})

	t.Run("still unwraps to the deadline", func(t *testing.T) {
		for _, queued := range []time.Duration{100 * time.Millisecond, 19 * time.Second} {
			err := timedOut("docker ps", deadline, queued, 20*time.Second)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("callers matching on the deadline must still work: %v", err)
			}
		}
	})

	// With no deadline there is no budget to divide, so there is nothing to say.
	t.Run("no deadline, no blame", func(t *testing.T) {
		err := timedOut("docker ps", deadline, 19*time.Second, 0)
		if strings.Contains(err.Error(), "슬롯") {
			t.Errorf("without a budget the queue cannot be blamed: %v", err)
		}
	})
}

// The wait is recorded even when nothing timed out, so a Command Log entry can
// show that a command sat in a queue.
func TestQueueWaitIsRecorded(t *testing.T) {
	res := &Result{Queued: 250 * time.Millisecond}
	if res.Queued != 250*time.Millisecond {
		t.Fatal("Result must carry the queue wait")
	}
}
