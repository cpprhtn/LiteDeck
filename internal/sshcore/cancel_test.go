package sshcore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Cancelling a line the user typed, without running it (§4.6a).
//
// When the app answers `code .` itself it has to take that line back out of the
// shell, and the shell must not run it or keep it in history. Ctrl-U is
// readline's kill-line and was the obvious choice — but cmd.exe takes it as a
// literal `^U`, leaves the line intact and runs it, which turned `code .` on a
// Windows server into `code .^U` followed by whatever was sent next.
//
// Checked here against a POSIX shell; the cmd.exe half was checked by hand
// against a real Windows machine, where backspace was the only one of Ctrl-U,
// Escape and Ctrl-C that worked. Backspace is what both agree on.
func TestBackspaceCancelsATypedLine(t *testing.T) {
	c := dialTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out safeLog
	sess, err := c.OpenPTY(ctx, PTYOptions{Cols: 100, Rows: 30}, func(b []byte) { out.write(b) }, nil)
	if err != nil {
		t.Fatalf("OpenPTY: %v", err)
	}
	defer sess.Close()
	time.Sleep(500 * time.Millisecond)

	typed := "echo SHOULD_NOT_RUN"
	sess.Write([]byte(typed))
	time.Sleep(400 * time.Millisecond)
	sess.Write([]byte(strings.Repeat("\b", len(typed))))
	time.Sleep(400 * time.Millisecond)
	sess.Write([]byte("echo RAN_INSTEAD\n"))
	time.Sleep(900 * time.Millisecond)

	got := out.String()
	if !strings.Contains(got, "RAN_INSTEAD") {
		t.Fatalf("the replacement never ran:\n%s", got)
	}
	// The echo of what was typed is on screen; what must not appear is the
	// shell having executed it.
	if strings.Contains(got, "SHOULD_NOT_RUNecho") {
		t.Errorf("backspace did not cancel the line:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "SHOULD_NOT_RUN") {
			t.Errorf("the cancelled command ran anyway: %q", line)
		}
	}
}
