package sshcore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// State is what the UI shows next to a host in the sidebar (§4.1).
type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
	StateReconnecting
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	default:
		return "disconnected"
	}
}

// StateFunc is called on every transition. It feeds the conn:state:<hostID>
// event in §3.2e, so it must not block — the caller is holding the keepalive
// loop while it runs.
type StateFunc func(hostID string, s State, err error)

// ManagerOptions tunes liveness detection. The defaults implement §3.2a:
// a keepalive every 30 seconds, reconnect after three unanswered.
type ManagerOptions struct {
	KeepaliveInterval   time.Duration // 0 → 30s
	KeepaliveTimeout    time.Duration // 0 → 10s
	MaxMissedKeepalives int           // 0 → 3
	ReconnectMinBackoff time.Duration // 0 → 1s
	ReconnectMaxBackoff time.Duration // 0 → 30s
}

func (o ManagerOptions) withDefaults() ManagerOptions {
	if o.KeepaliveInterval <= 0 {
		o.KeepaliveInterval = 30 * time.Second
	}
	if o.KeepaliveTimeout <= 0 {
		o.KeepaliveTimeout = 10 * time.Second
	}
	if o.MaxMissedKeepalives <= 0 {
		o.MaxMissedKeepalives = 3
	}
	if o.ReconnectMinBackoff <= 0 {
		o.ReconnectMinBackoff = time.Second
	}
	if o.ReconnectMaxBackoff <= 0 {
		o.ReconnectMaxBackoff = 30 * time.Second
	}
	return o
}

// ErrNotConnected is returned when a host is addressed while it has no live
// connection — during a reconnect, for instance.
var ErrNotConnected = errors.New("sshcore: host is not connected")

// Manager owns every connection the app holds open, keyed by host ID.
//
// Callers address hosts by ID rather than by holding a *Conn, because a
// reconnect replaces the underlying connection. That also matches the shape of
// the Wails bindings, which are all ListDir(hostID, …) style (§3.2e).
type Manager struct {
	opts    ManagerOptions
	onState StateFunc

	// genSeq numbers connections for the whole manager rather than per host.
	// Per host would restart at 1 after a disconnect, and a caller comparing
	// "the generation I cached from" against "the generation now" would see the
	// same number for two different connections — the exact confusion the
	// counter exists to remove.
	genSeq atomic.Uint64

	mu    sync.RWMutex
	hosts map[string]*managedHost
}

// NewManager creates a manager. onState may be nil.
func NewManager(opts ManagerOptions, onState StateFunc) *Manager {
	return &Manager{
		opts:    opts.withDefaults(),
		onState: onState,
		hosts:   make(map[string]*managedHost),
	}
}

type managedHost struct {
	cfg     HostConfig
	mgr     *Manager
	obs     Observer
	cancel  context.CancelFunc
	stopped chan struct{}

	mu    sync.RWMutex
	conn  *Conn
	state State
	// gen is this connection's number from the manager's sequence. It exists so callers can
	// tell "the same connection as last time" from "a replacement that happens
	// to have the same host ID", which reconnect produces without anybody
	// asking. Anything derived from the server — process IDs, the port sshd
	// answers on, what the OS supports — is only true of the connection it was
	// read through.
	gen uint64
}

// Connect dials a host and begins watching it. Connecting an already-connected
// host ID is an error; disconnect first.
func (m *Manager) Connect(ctx context.Context, cfg HostConfig, obs Observer) error {
	if cfg.ID == "" {
		return errors.New("sshcore: HostConfig.ID is required")
	}

	m.mu.Lock()
	if _, exists := m.hosts[cfg.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("sshcore: host %q is already connected", cfg.ID)
	}
	h := &managedHost{cfg: cfg, mgr: m, obs: obs, stopped: make(chan struct{})}
	m.hosts[cfg.ID] = h
	m.mu.Unlock()

	h.setState(StateConnecting, nil)
	conn, err := Dial(ctx, cfg)
	if err != nil {
		m.mu.Lock()
		delete(m.hosts, cfg.ID)
		m.mu.Unlock()
		h.setState(StateDisconnected, err)
		return err
	}
	conn.SetObserver(obs)

	h.mu.Lock()
	h.conn = conn
	h.gen = m.genSeq.Add(1)
	h.mu.Unlock()
	h.setState(StateConnected, nil)

	// The watch loop outlives ctx: ctx bounds the initial dial, not the
	// connection's lifetime. Disconnect stops it.
	loopCtx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go h.watch(loopCtx)
	return nil
}

// Conn returns the host's current connection.
func (m *Manager) Conn(hostID string) (*Conn, error) {
	m.mu.RLock()
	h, ok := m.hosts[hostID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotConnected, hostID)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.conn == nil {
		return nil, fmt.Errorf("%w: %s (%s)", ErrNotConnected, hostID, h.state)
	}
	return h.conn, nil
}

// State reports the host's connection state.
func (m *Manager) State(hostID string) State {
	m.mu.RLock()
	h, ok := m.hosts[hostID]
	m.mu.RUnlock()
	if !ok {
		return StateDisconnected
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.state
}

// Generation identifies this host's current connection.
//
// Zero means the host is not connected. The number changes on every dial,
// including the ones reconnect makes on its own, and that is the point: a
// reconnect is transparent to a caller running a command, but it must not be
// transparent to anything holding a fact it read from the old connection. PIDs
// are the sharp case. "These processes are ours, do not kill them" is true of
// one connection and false of its replacement, and after a reboot the same
// numbers belong to somebody else's processes.
func (m *Manager) Generation(hostID string) uint64 {
	m.mu.RLock()
	h, ok := m.hosts[hostID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.gen
}

// Exec runs a command on a host, resolving the connection at call time so a
// reconnect in between is transparent to the caller.
func (m *Manager) Exec(ctx context.Context, hostID, cmd string, args ...string) (*Result, error) {
	c, err := m.Conn(hostID)
	if err != nil {
		return nil, err
	}
	return c.Exec(ctx, cmd, args...)
}

// SFTP returns the host's SFTP client.
func (m *Manager) SFTP(hostID string) (*sftp.Client, error) {
	c, err := m.Conn(hostID)
	if err != nil {
		return nil, err
	}
	return c.SFTP()
}

// Disconnect closes a host's connection and stops watching it.
func (m *Manager) Disconnect(hostID string) error {
	m.mu.Lock()
	h, ok := m.hosts[hostID]
	delete(m.hosts, hostID)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotConnected, hostID)
	}
	return h.shutdown()
}

// Close disconnects every host.
func (m *Manager) Close() error {
	m.mu.Lock()
	hosts := make([]*managedHost, 0, len(m.hosts))
	for _, h := range m.hosts {
		hosts = append(hosts, h)
	}
	m.hosts = make(map[string]*managedHost)
	m.mu.Unlock()

	var firstErr error
	for _, h := range hosts {
		if err := h.shutdown(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *managedHost) shutdown() error {
	if h.cancel != nil {
		h.cancel()
		<-h.stopped
	}
	h.mu.Lock()
	conn := h.conn
	h.conn = nil
	h.mu.Unlock()

	h.setState(StateDisconnected, nil)
	if conn == nil {
		return nil
	}
	return conn.Close()
}

func (h *managedHost) setState(s State, err error) {
	h.mu.Lock()
	h.state = s
	h.mu.Unlock()
	if h.mgr.onState != nil {
		h.mgr.onState(h.cfg.ID, s, err)
	}
}

// watch sends keepalives and reconnects when the server stops answering.
func (h *managedHost) watch(ctx context.Context) {
	defer close(h.stopped)

	opts := h.mgr.opts
	ticker := time.NewTicker(opts.KeepaliveInterval)
	defer ticker.Stop()

	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		h.mu.RLock()
		conn := h.conn
		h.mu.RUnlock()
		if conn == nil {
			continue
		}

		if err := ping(conn.client, opts.KeepaliveTimeout); err != nil {
			missed++
			if missed < opts.MaxMissedKeepalives {
				continue
			}
			missed = 0
			h.reconnect(ctx)
			continue
		}
		missed = 0
	}
}

var errKeepaliveTimeout = errors.New("sshcore: keepalive timed out")

// ping issues the OpenSSH keepalive global request. The reply's contents are
// irrelevant — only that one arrives.
func ping(c *ssh.Client, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errKeepaliveTimeout
	}
}

// reconnect replaces a dead connection, retrying with exponential backoff until
// it succeeds or the host is disconnected. It does not give up on its own: a
// rebooting server should reappear in the UI by itself.
func (h *managedHost) reconnect(ctx context.Context) {
	h.mu.Lock()
	old := h.conn
	h.conn = nil
	h.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	h.setState(StateReconnecting, nil)

	backoff := h.mgr.opts.ReconnectMinBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		dialCtx, cancel := context.WithTimeout(ctx, h.mgr.opts.ReconnectMaxBackoff)
		conn, err := Dial(dialCtx, h.cfg)
		cancel()

		if err == nil {
			conn.SetObserver(h.obs)
			h.mu.Lock()
			h.conn = conn
			h.gen = h.mgr.genSeq.Add(1)
			h.mu.Unlock()
			h.setState(StateConnected, nil)
			return
		}
		h.setState(StateReconnecting, err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, h.mgr.opts.ReconnectMaxBackoff)
	}
}
