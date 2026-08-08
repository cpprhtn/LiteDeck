package sshcore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cpprhtn/LiteDeck/internal/shellquote"
	"golang.org/x/crypto/ssh"
)

// Long-running commands whose output arrives over time — `journalctl -f`,
// `docker logs -f` (§4.3, §4.5).
//
// Distinct from Exec, which buffers everything and returns once. A follow can
// run for hours, so it holds a channel from the long-lived budget and has to be
// closable from either end.
//
// Distinct from a PTY as well: no terminal is allocated, so the output arrives
// clean instead of wrapped in escape sequences and line-wrapped to a width
// nobody chose.

// Stream is a running command delivering output as it appears.
type Stream struct {
	sess    *ssh.Session
	release func()

	mu     sync.Mutex
	closed bool
}

// OnLine receives one line of output, without its trailing newline. It must not
// block: it runs on the reader goroutine, and stalling it stalls the stream.
type OnLine func(line string, isStderr bool)

// maxStreamLine bounds one line. A program printing a gigabyte without a
// newline must not be able to exhaust memory on the client.
const maxStreamLine = 256 * 1024

// StreamOptions carries the parts of a streamed command that are not argv.
type StreamOptions struct {
	// Stdin is fed to the command before its output is read. Secrets belong
	// here and never in argv — this is how an elevated follow passes a sudo
	// password (§7.2).
	Stdin io.Reader
}

// OpenStream starts a command and delivers its output line by line. onClose
// fires once when the command ends or the stream is closed.
func (c *Conn) OpenStream(
	ctx context.Context,
	onLine OnLine,
	onClose func(error),
	cmd string,
	args ...string,
) (*Stream, error) {
	return c.OpenStreamOpts(ctx, StreamOptions{}, onLine, onClose, cmd, args...)
}

// OpenStreamOpts is OpenStream with stdin.
func (c *Conn) OpenStreamOpts(
	ctx context.Context,
	opts StreamOptions,
	onLine OnLine,
	onClose func(error),
	cmd string,
	args ...string,
) (*Stream, error) {
	if onLine == nil {
		return nil, errors.New("sshcore: OpenStream needs a line handler")
	}
	line, err := shellquote.Join(append([]string{cmd}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("sshcore: build command: %w", err)
	}

	// Follows hold their channel for as long as the panel is open, so they draw
	// from the long-lived budget alongside SFTP and terminals.
	select {
	case c.longLived <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("sshcore: waiting for a stream slot: %w", ctx.Err())
	default:
		return nil, fmt.Errorf(
			"sshcore: 동시에 열 수 있는 스트림 수를 초과했습니다 (%d개) — 터미널 탭이나 로그 창을 닫아보세요",
			cap(c.longLived))
	}
	release := func() { <-c.longLived }

	sess, err := c.client.NewSession()
	if err != nil {
		release()
		return nil, fmt.Errorf("sshcore: open stream session: %w", err)
	}

	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		release()
		return nil, fmt.Errorf("sshcore: stream stdout: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		release()
		return nil, fmt.Errorf("sshcore: stream stderr: %w", err)
	}
	if opts.Stdin != nil {
		sess.Stdin = opts.Stdin
	}

	s := &Stream{sess: sess, release: release}

	if c.obs != nil {
		info := CommandInfo{HostID: c.cfg.ID, Line: line, Kind: CommandAction}
		c.obs.CommandStarted(info)
		// A follow has no exit code worth waiting for — it ends when the user
		// closes it — so it is reported as finished immediately. Leaving it
		// "running" forever would make the panel look stuck.
		c.obs.CommandFinished(info, &Result{}, nil)
	}

	if err := sess.Start(line); err != nil {
		s.Close()
		return nil, fmt.Errorf("sshcore: start %q: %w", line, err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scan(stdout, false, onLine) }()
	go func() { defer wg.Done(); scan(stderr, true, onLine) }()

	go func() {
		wg.Wait()
		waitErr := sess.Wait()
		s.Close()
		if onClose != nil {
			onClose(waitErr)
		}
	}()

	// Closing the session is the only way to stop a follow, since the remote
	// command has no reason to exit on its own.
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	return s, nil
}

func scan(r io.Reader, isStderr bool, onLine OnLine) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8*1024), maxStreamLine)
	for sc.Scan() {
		onLine(sc.Text(), isStderr)
	}
}

// Close ends the stream and returns its channel slot. Safe to call twice.
func (s *Stream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	err := s.sess.Close()
	s.release()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
