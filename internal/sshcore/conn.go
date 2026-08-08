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
	// them: the SFTP subsystem and terminal tabs. 0 means DefaultMaxLongLived.
	MaxLongLived int
}

// The channel budget, split in two.
//
// sshd's default MaxSessions is 10. A single pool would let a few terminal tabs
// starve command execution — every view would begin failing with an error the
// server does not explain. Two pools make the failure impossible instead of
// unlikely: terminals can fill their own budget and no more, and Exec always has
// its slots. The total stays under 10 with margin.
const (
	DefaultMaxSessions  = 4 // transient: Exec
	DefaultMaxLongLived = 4 // long-lived: SFTP subsystem + terminal tabs
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
	obs    Observer
	sem    chan struct{} // transient Exec channels
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

	d := net.Dialer{Timeout: timeout}
	tcp, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("sshcore: dial %s: %w", cfg.Addr, err)
	}

	// The handshake has no context-aware form, so bound it with a deadline and
	// clear that deadline once the connection is up.
	_ = tcp.SetDeadline(time.Now().Add(timeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(tcp, cfg.Addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            cfg.Auth,
		HostKeyCallback: cfg.HostKeyCallback,
		Timeout:         timeout,
	})
	if err != nil {
		tcp.Close()
		return nil, fmt.Errorf("sshcore: handshake with %s: %w", cfg.Addr, err)
	}
	_ = tcp.SetDeadline(time.Time{})

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
	// Queue rather than let sshd reject the channel outright.
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("sshcore: waiting for a session slot: %w", ctx.Err())
	}

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
		}, fmt.Errorf("sshcore: %q: %w", line, ctx.Err())
	}

	res := &Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: time.Since(start),
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
	// The SFTP subsystem is a long-lived channel too, so it takes a slot from
	// that budget rather than appearing out of nowhere against the server limit.
	select {
	case c.longLived <- struct{}{}:
	default:
		return nil, errors.New("sshcore: no channel slots left for the SFTP subsystem — close a terminal tab")
	}
	cl, err := sftp.NewClient(c.client)
	if err != nil {
		<-c.longLived
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
		<-c.longLived
	}
	c.mu.Unlock()
	return c.client.Close()
}
