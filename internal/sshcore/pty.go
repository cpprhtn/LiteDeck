package sshcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

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
}

// PTYSession is one live terminal.
type PTYSession struct {
	sess    *ssh.Session
	stdin   io.WriteCloser
	release func() // returns the long-lived channel slot

	mu     sync.Mutex
	closed bool
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
			"sshcore: 이 호스트에 열 수 있는 터미널 수를 초과했습니다 (동시 %d개)",
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

	p := &PTYSession{sess: sess, stdin: stdin, release: release}

	startErr := p.start(opts)
	if startErr != nil {
		p.Close()
		return nil, startErr
	}

	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				onOutput(chunk)
			}
			if err != nil {
				break
			}
		}
		waitErr := sess.Wait()
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

	case opts.InitialDir != "":
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

	_ = p.stdin.Close()
	err := p.sess.Close()
	p.release()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
