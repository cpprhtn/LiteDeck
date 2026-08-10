package sshcore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeLog collects terminal output, which arrives on the reader goroutine while
// the test reads it from its own.
type safeLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeLog) write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Write(p)
}

func (s *safeLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *safeLog) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Len()
}

// Asking a live shell where it is (§4.6a).
//
// The reply has to be found among the shell's echo of the question, the prompt
// redraw and whatever else the terminal decides to emit, and none of it may
// reach the screen. A real shell is the only way to know that holds.

func TestCurrentDirAsksTheLiveShell(t *testing.T) {
	c := dialTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var shown safeLog
	sess, err := c.OpenPTY(ctx, PTYOptions{Cols: 120, Rows: 40},
		func(b []byte) { shown.write(b) }, nil)
	if err != nil {
		t.Fatalf("OpenPTY: %v", err)
	}
	defer sess.Close()

	// Let the login shell settle before asking it anything.
	time.Sleep(500 * time.Millisecond)
	before := shown.Len()

	if _, err := sess.Write([]byte("cd /tmp\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	dir, err := sess.CurrentDir(ctx)
	if err != nil {
		t.Fatalf("CurrentDir: %v\nterminal:\n%s", err, shown.String())
	}
	if dir != "/tmp" {
		t.Errorf("CurrentDir = %q, want /tmp", dir)
	}

	// The question and its answer are the app's business, not the user's.
	after := shown.String()[before:]
	if strings.Contains(after, "LDCWD") || strings.Contains(after, "printf") {
		t.Errorf("the probe was shown to the user:\n%q", after)
	}
}

func TestCurrentDirGivesUpRatherThanFreezingTheTerminal(t *testing.T) {
	c := dialTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := c.OpenPTY(ctx, PTYOptions{Cols: 80, Rows: 24}, func([]byte) {}, nil)
	if err != nil {
		t.Fatalf("OpenPTY: %v", err)
	}
	defer sess.Close()
	time.Sleep(300 * time.Millisecond)

	// A shell busy running something never sees the question. The terminal has
	// to come back rather than hang, and it has to come back reasonably soon.
	if _, err := sess.Write([]byte("sleep 30\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	if _, err := sess.CurrentDir(ctx); err == nil {
		t.Error("claimed an answer from a shell that was busy")
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Errorf("took %v to give up", elapsed)
	}

	// And the terminal is usable again afterwards.
	if _, err := sess.Write([]byte("\x03")); err != nil {
		t.Fatalf("Ctrl-C after a failed probe: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if dir, err := sess.CurrentDir(ctx); err != nil {
		t.Errorf("terminal did not recover: %v", err)
	} else if dir == "" {
		t.Error("recovered but answered nothing")
	}
}

func TestParseCWDIgnoresTheEchoedQuestion(t *testing.T) {
	// The shell echoes the command before running it, so the marker appears
	// twice — once carrying the placeholder, once carrying the answer.
	out := []byte("$ printf 'LDCWD:%s:END\\n' \"$PWD\"\r\nLDCWD:/home/litedeck:END\r\n$ ")
	if got := parseCWD(out, posixCWD); got != "/home/litedeck" {
		t.Errorf("parseCWD = %q", got)
	}

	// cmd.exe echoes its own placeholder.
	win := []byte("C:\\Users\\KTJ>echo LDCWD:%cd%:END\r\nLDCWD:C:\\Users\\KTJ:END\r\n")
	if got := parseCWD(win, cmdCWD); got != `C:\Users\KTJ` {
		t.Errorf("parseCWD(windows) = %q", got)
	}

	if got := parseCWD([]byte("nothing here"), posixCWD); got != "" {
		t.Errorf("invented an answer: %q", got)
	}
}
