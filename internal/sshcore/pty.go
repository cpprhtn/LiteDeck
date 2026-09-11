package sshcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/shellquote"
	"golang.org/x/crypto/ssh"
)

// Interactive terminals (§4.6).
//
// A PTY holds its channel for as long as the tab is open, which makes it a
// different animal from the transient channels Exec uses. See the long-lived
// budget in conn.go: a terminal that quietly ate an Exec slot would make the
// other views start failing once a few tabs were open.

// PTYOptions configures a terminal session.
type PTYOptions struct {
	// Term is the TERM value; xterm-256color unless overridden.
	Term       string
	Cols, Rows int

	// InitialDir starts the shell in a directory ("open in terminal" from the
	// file explorer). Quoted before use.
	InitialDir string

	// Exec replaces the login shell with a specific argv — a container shell,
	// for instance. Given as argv rather than a string so it goes through the
	// same quoting as every other command (§3.2b). What the *user* subsequently
	// types is raw by definition; that is what a terminal is.
	Exec []string

	// Windows makes the shell cmd.exe rather than a POSIX one, which changes
	// the only question this package ever asks a live terminal (§4.6a).
	Windows bool

	// Line is a ready-made command line to start instead of the login shell.
	//
	// Windows only, and deliberately a string rather than argv: the quoting
	// rules there are per-shell — cmd, PowerShell and wsl each want a different
	// spelling of "start here" — so the adapter builds the whole line and this
	// package does not try to quote it a second time.
	Line string
}

// PTYSession is one live terminal.
type PTYSession struct {
	sess    *ssh.Session
	stdin   io.WriteCloser
	release func() // returns the long-lived channel slot
	windows bool
	// done is closed when the remote command has ended. Close waits on it
	// briefly so the shell gets to leave on its own — see Close.
	done chan struct{}

	mu     sync.Mutex
	closed bool
	// Set while a question is in flight. Output goes here instead of to the
	// screen, so the user never sees what was asked or what came back.
	probe *pendingProbe
}

type pendingProbe struct {
	seen bytes.Buffer
	done chan string
	spec cwdProbe
}

const defaultTerm = "xterm-256color"

// OpenPTY starts an interactive session.
//
// onOutput is called from a reader goroutine with each chunk the server sends;
// it must not block. onClose fires once when the session ends, with the exit
// error if there was one.
func (c *Conn) OpenPTY(
	ctx context.Context,
	opts PTYOptions,
	onOutput func([]byte),
	onClose func(error),
) (*PTYSession, error) {
	if onOutput == nil {
		return nil, errors.New("sshcore: OpenPTY needs an output handler")
	}
	if opts.Cols <= 0 {
		opts.Cols = 80
	}
	if opts.Rows <= 0 {
		opts.Rows = 24
	}
	if opts.Term == "" {
		opts.Term = defaultTerm
	}

	// Terminals draw from the long-lived budget, never from the Exec pool.
	select {
	case c.longLived <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("sshcore: waiting for a terminal slot: %w", ctx.Err())
	default:
		return nil, fmt.Errorf(
			i18n.S("sshcore: 이 호스트에 열 수 있는 터미널·로그 창 수를 초과했습니다 (동시 %d개) — ")+
				i18n.S("쓰지 않는 터미널 탭이나 로그 창을 닫으세요. ")+
				i18n.S("서버의 sshd MaxSessions가 기본값(10)보다 낮으면 이보다 먼저 막힐 수 있습니다"),
			cap(c.longLived))
	}
	release := func() { <-c.longLived }

	sess, err := c.client.NewSession()
	if err != nil {
		release()
		return nil, fmt.Errorf("sshcore: open terminal session: %w", err)
	}

	// ECHO on and sane speeds: the remote shell does its own line editing, and
	// turning echo off here would make typing invisible.
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
	}
	if err := sess.RequestPty(opts.Term, opts.Rows, opts.Cols, modes); err != nil {
		sess.Close()
		release()
		return nil, fmt.Errorf("sshcore: request pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		release()
		return nil, fmt.Errorf("sshcore: terminal stdin: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		release()
		return nil, fmt.Errorf("sshcore: terminal stdout: %w", err)
	}
	// With a PTY the server merges stderr into stdout, so only one reader is
	// needed; asking for a separate stderr pipe would just sit idle.

	p := &PTYSession{sess: sess, stdin: stdin, release: release, windows: opts.Windows, done: make(chan struct{})}

	startErr := p.start(opts)
	if startErr != nil {
		p.Close()
		return nil, startErr
	}

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 && !p.absorb(buf[:n]) {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				onOutput(chunk)
			}
			if err != nil {
				break
			}
		}
		waitErr := sess.Wait()
		close(p.done)
		p.Close()
		if onClose != nil {
			onClose(waitErr)
		}
	}()

	return p, nil
}

// start launches either a login shell or a specific command.
func (p *PTYSession) start(opts PTYOptions) error {
	switch {
	case opts.Line != "":
		if err := p.sess.Start(opts.Line); err != nil {
			return fmt.Errorf("sshcore: start terminal shell: %w", err)
		}

	case len(opts.Exec) > 0:
		line, err := shellquote.Join(opts.Exec...)
		if err != nil {
			return fmt.Errorf("sshcore: build terminal command: %w", err)
		}
		if opts.InitialDir != "" {
			dir, err := shellquote.Quote(opts.InitialDir)
			if err != nil {
				return fmt.Errorf("sshcore: quote directory: %w", err)
			}
			line = "cd " + dir + " && exec " + line
		}
		if err := p.sess.Start(line); err != nil {
			return fmt.Errorf("sshcore: start terminal command: %w", err)
		}

	case opts.InitialDir != "" && !opts.Windows:
		dir, err := shellquote.Quote(opts.InitialDir)
		if err != nil {
			return fmt.Errorf("sshcore: quote directory: %w", err)
		}
		// exec replaces the wrapper so the user gets their real login shell
		// rather than a subshell whose exit leaves them somewhere unexpected.
		if err := p.sess.Start("cd " + dir + " && exec \"${SHELL:-/bin/sh}\" -l"); err != nil {
			return fmt.Errorf("sshcore: start shell: %w", err)
		}

	default:
		if err := p.sess.Shell(); err != nil {
			return fmt.Errorf("sshcore: start shell: %w", err)
		}
	}
	return nil
}

// exitGrace is how long Close waits for the shell to leave after being told to.
//
// Short: it is paid on every terminal that is still open when the app quits,
// and closeAll runs them together so the wall-clock cost is one grace, not one
// per tab.
const exitGrace = 1200 * time.Millisecond

// probeTimeout bounds how long the screen may stay frozen waiting for an
// answer. A shell that is busy running something will not reply, and the user
// must get their terminal back rather than a hang.
const probeTimeout = 3 * time.Second

// CurrentDir asks the live shell where it is standing (§4.6a).
//
// Needed only when somebody types a relative path — `code .` — because that is
// the one thing the client genuinely cannot know. Absolute paths never come
// here, so a session where nobody uses a relative path is never asked anything.
//
// The question and its answer are both kept off the screen: output is diverted
// from the moment it is asked until the reply arrives. What the user sees is
// their prompt, then their prompt again.
func (p *PTYSession) CurrentDir(ctx context.Context) (string, error) {
	spec := posixCWD
	if p.windows {
		spec = cmdCWD
	}

	pending := &pendingProbe{done: make(chan string, 1), spec: spec}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return "", errors.New("sshcore: terminal is closed")
	}
	if p.probe != nil {
		p.mu.Unlock()
		return "", errors.New("sshcore: already asking this terminal where it is")
	}
	p.probe = pending
	p.mu.Unlock()

	stop := func() {
		p.mu.Lock()
		p.probe = nil
		p.mu.Unlock()
	}

	// Carriage return, not newline. A terminal sends \r when Enter is pressed, and
	// cmd.exe submits on nothing else — given \n it appends the next thing sent to
	// the same line and runs them together. A POSIX pty maps \r to \n on input, so
	// this is what both shells accept.
	if _, err := p.stdin.Write([]byte(spec.command + "\r")); err != nil {
		stop()
		return "", fmt.Errorf("sshcore: ask terminal for its directory: %w", err)
	}

	select {
	case dir := <-pending.done:
		stop()
		if dir == "" {
			return "", errors.New(i18n.S("sshcore: 터미널이 현재 디렉터리를 알려주지 않았습니다"))
		}
		return dir, nil
	case <-ctx.Done():
		stop()
		return "", ctx.Err()
	case <-time.After(probeTimeout):
		stop()
		// Most likely a full-screen program is running and never saw the line.
		return "", errors.New(i18n.S("sshcore: 터미널이 응답하지 않습니다 — 실행 중인 프로그램이 있는지 확인하세요"))
	}
}

// absorb diverts output while a question is in flight.
//
// Reports whether the chunk was taken, in which case it must not be displayed:
// the echoed question and its answer are both noise the user did not ask for.
func (p *PTYSession) absorb(chunk []byte) bool {
	p.mu.Lock()
	pending := p.probe
	if pending == nil {
		p.mu.Unlock()
		return false
	}
	pending.seen.Write(chunk)
	dir := parseCWD(pending.seen.Bytes(), pending.spec)
	// Keep swallowing until the answer is complete; a reply split across reads
	// would otherwise be half-shown and half-parsed.
	if dir == "" {
		// Unless the shell is clearly saying something else entirely, in which
		// case the screen is more use to the user than the silence is.
		if pending.seen.Len() > 64<<10 {
			p.probe = nil
			p.mu.Unlock()
			pending.done <- ""
			return false
		}
		p.mu.Unlock()
		return true
	}
	p.probe = nil
	p.mu.Unlock()
	pending.done <- dir
	return true
}

// Write sends user keystrokes to the remote terminal.
func (p *PTYSession) Write(data []byte) (int, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return 0, errors.New("sshcore: terminal is closed")
	}
	return p.stdin.Write(data)
}

// Resize tells the remote side the window changed, so full-screen programs
// redraw at the right size.
func (p *PTYSession) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("sshcore: invalid terminal size %dx%d", cols, rows)
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errors.New("sshcore: terminal is closed")
	}
	return p.sess.WindowChange(rows, cols)
}

// Close ends the session and returns its channel slot. Safe to call twice — the
// reader goroutine and the UI both close, and whichever arrives second is a
// no-op rather than a double release.
func (p *PTYSession) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	// Ask the shell to leave before the channel goes.
	//
	// On Windows this is not politeness. Closing the channel out from under
	// wsl.exe leaves it holding LxssManager open; the service goes to
	// StopPending and WSL is then broken for every program on that machine
	// until it reboots. Measured on Windows 10 19045: several sessions torn
	// down this way and `wsl -- echo` stopped answering.
	//
	// Bounded, and the wait is skipped entirely when the remote end has already
	// finished — which is the common case, because the reader closes done
	// before calling this.
	if p.windows {
		_, _ = p.stdin.Write([]byte("exit\r\n"))
		select {
		case <-p.done:
		case <-time.After(exitGrace):
		}
	}

	_ = p.stdin.Close()
	err := p.sess.Close()
	p.release()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
