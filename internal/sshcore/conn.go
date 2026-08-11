// Package sshcore owns every byte that travels between LiteDeck and a server.
//
// One host means one TCP connection; individual commands, file transfers and
// terminals ride on separate SSH channels over it (§3.2a). Reconnecting per
// action is what makes other tools feel sluggish, so it is not done here.
package sshcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/shellquote"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// HostConfig describes how to reach one server.
type HostConfig struct {
	ID              string // stable identifier used in events and logs
	Addr            string // "host:port"
	User            string
	Auth            []ssh.AuthMethod
	HostKeyCallback ssh.HostKeyCallback // must not be nil; see hostkey.go
	Timeout         time.Duration       // handshake timeout; 0 means 15s

	// MaxSessions bounds concurrent *transient* channels — the ones Exec opens
	// and closes. It exists because sshd enforces its own limit (MaxSessions,
	// default 10) and rejects channels past it with a bare "open failed", which
	// would surface as random command failures whenever polling fans out.
	// Staying under the server's limit and queueing locally turns that into a
	// short wait instead. 0 means DefaultMaxSessions.
	MaxSessions int

	// MaxLongLived bounds channels that stay open for as long as the user keeps
	// them: terminal tabs and live log follows. 0 means DefaultMaxLongLived.
	MaxLongLived int

	// Jump is the bastion to reach Addr through (§4.1). Nil means dial directly.
	//
	// The bastion is a full HostConfig, not a bare address, because it is a
	// server being logged in to: it has its own user, its own credentials and —
	// the part that matters — **its own host key to verify**. A ProxyJump that
	// skips that check hands the whole session to whoever answers on the
	// bastion's port, which is the one thing the hop was supposed to prevent.
	//
	// One hop. Chains would be a linked list and the same code, but nothing
	// verifies them, so the config layer refuses to build one.
	Jump *HostConfig
}

// The channel budget.
//
// sshd's default MaxSessions is 10, and it rejects channels past it with a bare
// "open failed" that surfaces as random unexplained failures. So the app counts
// its own channels and stays under that number.
//
// The split matters because the two pools fail differently. Exec *queues*: a
// full pool costs a short wait and nothing else. A terminal or a log follow
// cannot queue — the user asked for it now — so it fails outright. The pool that
// fails hard gets the larger share, and the one that degrades gracefully gets
// less.
//
// The SFTP subsystem is not in either pool. It is structurally one channel per
// connection (created once, reused by every file operation), so a semaphore
// around it would bound something already bounded. It is counted in the total
// below instead. Charging it to the long-lived pool is what made the terminal
// budget quietly 3 rather than the 4 the error message claimed.
//
//	1 (SFTP) + 5 (terminals, logs) + 3 (Exec) = 9, one under the default.
const (
	DefaultMaxSessions  = 3 // transient: Exec. Queues when full.
	DefaultMaxLongLived = 5 // long-lived: terminal tabs + log follows.
)

// Result is the outcome of one remote command.
//
// A non-zero ExitCode is reported as a value, not an error: adapters probe
// constantly (`command -v docker`, `sudo -n true`) and failure is a normal
// answer there. A non-nil error means the command could not be run or its
// result could not be collected.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
	// Queued is how long this command waited for one of the Exec channels
	// before it started. Recorded because it is otherwise invisible: a command
	// that queued behind two others and then ran out of budget looks exactly
	// like a slow server.
	Queued time.Duration
}

// OK reports whether the command exited zero.
func (r *Result) OK() bool { return r.ExitCode == 0 }

// Err returns a diagnostic carrying the remote stderr, or nil if the command
// succeeded. The stderr text is preserved verbatim because the UI shows it to
// the user rather than hiding it (§8).
func (r *Result) Err() error {
	if r.OK() {
		return nil
	}
	msg := bytes.TrimSpace(r.Stderr)
	if len(msg) == 0 {
		return fmt.Errorf("command exited %d", r.ExitCode)
	}
	return fmt.Errorf("command exited %d: %s", r.ExitCode, msg)
}

// ExecOptions carries the parts of a command that are not argv.
type ExecOptions struct {
	// Stdin is fed to the remote command. Secrets belong here and never in
	// argv, which is visible in the remote process table and in the Command
	// Log — this is how the sudo flow passes a password (§7.2).
	Stdin io.Reader

	// LogLine overrides what the Observer is told, for commands whose real
	// text must not be recorded.
	LogLine string

	// Kind separates what the user asked for from what the app did on its own.
	// Defaults to CommandAction.
	Kind CommandKind
}

// CommandKind classifies why a command ran.
//
// The Command Log exists so the user can see what the GUI does on their behalf
// (§4.6). Polling drowns that: a view refreshing every two seconds produces
// hundreds of entries an hour, and the one restart the user actually performed
// ends up buried. Classifying at the source lets the panel foreground the
// answer without ever hiding anything.
type CommandKind string

const (
	// CommandAction is something the user asked for. The default.
	CommandAction CommandKind = ""
	// CommandPoll is a background refresh of a visible view.
	CommandPoll CommandKind = "poll"
	// CommandProbe is a capability check whose non-zero exit is an answer, not
	// a fault — `command -v docker`, `sudo -n true`.
	CommandProbe CommandKind = "probe"
)

// CommandInfo identifies a command to an Observer.
type CommandInfo struct {
	HostID string
	Line   string
	Kind   CommandKind
}

// timedOut names which half of the budget ran out.
//
// The deadline covers queueing and running together, so a command that waited
// for a channel and then had no time left fails with a message that reads as
// "the server was slow at this" — when the truth is that it never got a fair
// share of the budget. The first report of this in the wild could not be told
// apart from a genuinely stalled server, which is the point of saying so.
func timedOut(line string, err error, queued, budget time.Duration) error {
	if budget > 0 && queued > budget/2 {
		// One literal, not a concatenation: the i18n coverage test reads these
		// out of the syntax tree and a split string is invisible to it.
		note := i18n.T(" — 세션 슬롯을 기다리는 데 %s 를 써서 실행할 시간이 남지 않았습니다 (동시 실행 %d개 제한). 서버가 느린 것이 아닐 수 있습니다",
			queued.Round(time.Millisecond), DefaultMaxSessions)
		return fmt.Errorf("sshcore: %q: %w"+note, line, err)
	}
	return fmt.Errorf("sshcore: %q: %w", line, err)
}

// Probe reports whether a non-zero exit is an expected answer.
func (c CommandInfo) Probe() bool { return c.Kind == CommandProbe }

// Background reports whether the app initiated this, rather than the user.
func (c CommandInfo) Background() bool { return c.Kind != CommandAction }

// Observer is notified of every command. It backs the Command Log (§4.6),
// which is the feature that lets a user see exactly what the GUI does on
// their behalf. Implementations must be safe for concurrent use.
type Observer interface {
	CommandStarted(CommandInfo)
	CommandFinished(CommandInfo, *Result, error)
}

// Conn is a live connection to one host.
type Conn struct {
	cfg    HostConfig
	client *ssh.Client
	// jump is the bastion this connection is tunnelled through, if any. Held
	// only so it dies with the session it carries — an orphaned bastion
	// connection is an idle login sitting on someone's jump host.
	jump *Conn
	obs  Observer
	sem  chan struct{} // transient Exec channels
	// longLived bounds the SFTP subsystem and terminal tabs, which hold their
	// channel until closed.
	longLived chan struct{}

	mu   sync.Mutex
	sftp *sftp.Client // created on first use, shared by all file operations
}

// syncBuffer collects command output. It is mutex-guarded because on
// cancellation the session's IO goroutines may still be writing while the
// caller reads what was captured so far.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b.Bytes()...)
}

// Dial establishes the connection and completes the SSH handshake.
func Dial(ctx context.Context, cfg HostConfig) (*Conn, error) {
	if cfg.HostKeyCallback == nil {
		// Refusing here rather than defaulting to InsecureIgnoreHostKey is
		// deliberate: an unverified host key defeats the point of §7.1.
		return nil, errors.New("sshcore: HostKeyCallback is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	// Through a bastion, the transport is a channel on that first connection
	// rather than a socket of our own (§4.1). Everything after this point is
	// identical either way: the target's host key is checked the same, the
	// credentials are the target's, and the bastion never sees the session —
	// it only forwards bytes it cannot read.
	var (
		jump *Conn
		tcp  net.Conn
		err  error
	)
	if cfg.Jump != nil {
		jump, err = Dial(ctx, *cfg.Jump)
		if err != nil {
			return nil, fmt.Errorf("sshcore: bastion: %w", err)
		}
		tcp, err = jump.client.DialContext(ctx, "tcp", cfg.Addr)
		if err != nil {
			_ = jump.Close()
			return nil, fmt.Errorf("sshcore: %s via %s: %w", cfg.Addr, cfg.Jump.Addr, err)
		}
	} else {
		d := net.Dialer{Timeout: timeout}
		tcp, err = d.DialContext(ctx, "tcp", cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("sshcore: dial %s: %w", cfg.Addr, err)
		}
	}

	// The handshake has no context-aware form, so a watchdog closes the
	// transport out from under it instead. A deadline would have done for a
	// socket, but a forwarded channel does not carry one — and this way the
	// caller's cancellation reaches the handshake too, which it never did.
	handshake := make(chan struct{})
	go func() {
		select {
		case <-handshake:
		case <-ctx.Done():
			_ = tcp.Close()
		case <-time.After(timeout):
			_ = tcp.Close()
		}
	}()

	sshConn, chans, reqs, err := ssh.NewClientConn(tcp, cfg.Addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            cfg.Auth,
		HostKeyCallback: cfg.HostKeyCallback,
		Timeout:         timeout,
	})
	close(handshake)
	if err != nil {
		tcp.Close()
		if jump != nil {
			_ = jump.Close()
		}
		return nil, fmt.Errorf("sshcore: handshake with %s: %w", cfg.Addr, err)
	}

	maxSessions := cfg.MaxSessions
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}
	maxLongLived := cfg.MaxLongLived
	if maxLongLived <= 0 {
		maxLongLived = DefaultMaxLongLived
	}
	return &Conn{
		cfg:       cfg,
		client:    ssh.NewClient(sshConn, chans, reqs),
		jump:      jump,
		sem:       make(chan struct{}, maxSessions),
		longLived: make(chan struct{}, maxLongLived),
	}, nil
}

// SetObserver installs the Command Log sink. Pass nil to detach.
func (c *Conn) SetObserver(o Observer) { c.obs = o }

// HostID returns the configured identifier for this host.
func (c *Conn) HostID() string { return c.cfg.ID }

// Exec runs cmd with args on the server and waits for it to finish.
//
// Arguments are quoted individually — callers pass argv, never an assembled
// string, so there is no path by which a filename can become a command (§3.2b).
func (c *Conn) Exec(ctx context.Context, cmd string, args ...string) (*Result, error) {
	return c.ExecOpts(ctx, ExecOptions{}, cmd, args...)
}

// ExecOpts is Exec with stdin and logging control.
func (c *Conn) ExecOpts(ctx context.Context, opts ExecOptions, cmd string, args ...string) (*Result, error) {
	line, err := shellquote.Join(append([]string{cmd}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("sshcore: build command: %w", err)
	}

	logged := opts.LogLine
	if logged == "" {
		logged = line
	}
	info := CommandInfo{HostID: c.cfg.ID, Line: logged, Kind: opts.Kind}
	if c.obs != nil {
		c.obs.CommandStarted(info)
	}

	res, err := c.run(ctx, line, opts.Stdin)
	if c.obs != nil {
		c.obs.CommandFinished(info, res, err)
	}
	return res, err
}

// Probe runs a command whose failure is an expected answer, such as testing
// whether a binary exists. Identical to Exec except in how it is logged.
func (c *Conn) Probe(ctx context.Context, cmd string, args ...string) (*Result, error) {
	return c.ExecOpts(ctx, ExecOptions{Kind: CommandProbe}, cmd, args...)
}

// Poll runs a background refresh for a visible view. Identical to Exec except
// in how it is logged.
func (c *Conn) Poll(ctx context.Context, cmd string, args ...string) (*Result, error) {
	return c.ExecOpts(ctx, ExecOptions{Kind: CommandPoll}, cmd, args...)
}

func (c *Conn) run(ctx context.Context, line string, stdin io.Reader) (*Result, error) {
	// The caller's deadline covers both waiting for a channel and running the
	// command, so how much of it the queue took decides which of the two a
	// timeout should be blamed on.
	var budget time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
	}
	queuedAt := time.Now()

	// Queue rather than let sshd reject the channel outright.
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("sshcore: waiting for a session slot: %w", ctx.Err())
	}
	queued := time.Since(queuedAt)

	sess, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("sshcore: open session: %w", err)
	}
	defer sess.Close()

	var stdout, stderr syncBuffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if stdin != nil {
		sess.Stdin = stdin
	}

	start := time.Now()
	if err := sess.Start(line); err != nil {
		return nil, fmt.Errorf("sshcore: start %q: %w", line, err)
	}

	// Session.Wait has no context-aware form, and closing the session does not
	// reliably unblock it — an interrupted `sleep 30` would otherwise pin the
	// caller for the full 30 seconds. So Wait runs on its own goroutine and the
	// caller races it against ctx.
	waitDone := make(chan error, 1)
	go func() { waitDone <- sess.Wait() }()

	var runErr error
	select {
	case runErr = <-waitDone:
	case <-ctx.Done():
		_ = sess.Close()
		// Brief grace period so output already in flight is captured, but the
		// caller is never held hostage to the remote process.
		select {
		case <-waitDone:
		case <-time.After(500 * time.Millisecond):
		}
		return &Result{
			Stdout:   stdout.Bytes(),
			Stderr:   stderr.Bytes(),
			Duration: time.Since(start),
			Queued:   queued,
		}, timedOut(line, ctx.Err(), queued, budget)
	}

	res := &Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: time.Since(start),
		Queued:   queued,
	}

	var exitErr *ssh.ExitError
	switch {
	case runErr == nil:
		return res, nil
	case errors.As(runErr, &exitErr):
		res.ExitCode = exitErr.ExitStatus()
		return res, nil
	default:
		return res, fmt.Errorf("sshcore: run %q: %w", line, runErr)
	}
}

// SFTP returns the connection's SFTP client, starting the subsystem on first
// use. File operations go through this rather than parsing `ls` (§3.2c).
func (c *Conn) SFTP() (*sftp.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sftp != nil {
		return c.sftp, nil
	}
	// No semaphore: this runs under c.mu with the c.sftp != nil check above, so
	// there is exactly one of these per connection for its whole life. It is
	// accounted for in the budget arithmetic, not policed by a counter.
	cl, err := sftp.NewClient(c.client)
	if err != nil {
		return nil, fmt.Errorf("sshcore: start sftp subsystem: %w", err)
	}
	c.sftp = cl
	return cl, nil
}

// Close tears down the SFTP subsystem and the underlying connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.sftp != nil {
		_ = c.sftp.Close()
		c.sftp = nil
	}
	c.mu.Unlock()
	err := c.client.Close()
	if c.jump != nil {
		_ = c.jump.Close()
	}
	return err
}
