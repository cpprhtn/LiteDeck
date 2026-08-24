package sshcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// Local port forwarding over the connection that is already open (§4.1).
//
// The service this exists for is one bound to 127.0.0.1 on the server on
// purpose — an admin UI whose author decided it should not answer from the
// internet. Reaching it normally means a second login (`ssh -L …`) beside the
// one the app is already holding. That second login is the whole friction, and
// there is no reason for it: the SSH connection in front of us can carry the
// bytes on a channel of its own.
//
// # This does not spend the session budget
//
// A forward is a `direct-tcpip` channel, and sshd's MaxSessions counts *shell,
// login and subsystem* channels — not these. So a tunnel costs nothing from
// the Exec or long-lived pools in conn.go, and the arithmetic there is
// unaffected. What it can hit instead is `AllowTcpForwarding no`, which is a
// different refusal with a different fix, and OpenTunnel finds that out at open
// time rather than leaving it to appear as a blank page in a browser.

// maxForwarded bounds concurrent forwarded connections on one tunnel.
//
// A browser opens several per origin and a websocket on top, so the number has
// to be comfortably above a handful. It is not a security control — the
// listener is on loopback — but an accept loop with no ceiling is a way to run
// a machine out of file descriptors by accident.
const maxForwarded = 64

// forwardDialTimeout bounds one dial to the service on the far side. The
// service is on the server's own loopback, so this is generous already; a
// longer wait is a service that is not answering rather than a slow network.
const forwardDialTimeout = 10 * time.Second

// ErrForwardingDenied reports that the server refuses to forward at all.
//
// sshd says "administratively prohibited" for this, which reads as a problem
// with the account. It is one line in sshd_config, and saying so is the
// difference between a two-minute fix and an afternoon.
var ErrForwardingDenied = errors.New("sshcore: the server does not allow TCP forwarding")

// DialRemote opens a connection to addr *as the server sees it*.
//
// "127.0.0.1:3001" therefore means the server's loopback, not this machine's —
// which is the entire point. Used both by Tunnel and by callers that want to
// speak to a local-only service without publishing a port at all.
func (c *Conn) DialRemote(ctx context.Context, addr string) (net.Conn, error) {
	nc, err := c.client.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, forwardError(addr, err)
	}
	return nc, nil
}

// forwardError names the one failure whose own wording sends people looking in
// the wrong place.
func forwardError(addr string, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "administratively prohibited") {
		return fmt.Errorf("%w: %s — %s", ErrForwardingDenied, addr,
			i18n.S("서버의 sshd_config 에서 AllowTcpForwarding 을 켜야 합니다"))
	}
	return fmt.Errorf("sshcore: forward to %s: %w", addr, err)
}

// Tunnel is a listener on this machine whose connections come out on the
// server. It stops when Close is called, and its connection dying kills it too
// — a tunnel outliving the session it rides on would be a port on the user's
// machine that answers and then hangs.
type Tunnel struct {
	conn   *Conn
	remote string // "127.0.0.1:3001", as the server sees it
	ln     net.Listener
	opened time.Time

	mu     sync.Mutex
	closed bool
	live   map[net.Conn]struct{}
	sem    chan struct{}
}

// OpenTunnel starts forwarding localPort on this machine to remote on the
// server. localPort 0 takes whatever the OS offers, which is the normal case:
// the address goes straight into a browser, so it never has to be memorable,
// and a fixed number is one more thing that can already be taken.
//
// The listener binds loopback and there is no option not to. Binding a
// forward into a 127.0.0.1-only service on 0.0.0.0 would publish, from this
// machine, exactly what its author kept off the network — with none of the
// authentication the server-side bind was standing in for.
func (c *Conn) OpenTunnel(remote string, localPort int) (*Tunnel, error) {
	if _, _, err := net.SplitHostPort(remote); err != nil {
		return nil, fmt.Errorf("sshcore: tunnel target %q is not host:port: %w", remote, err)
	}

	// Ask the server before binding anything locally. Otherwise the first
	// answer the user gets is a browser error page, which says nothing about
	// AllowTcpForwarding and nothing about the service being down either.
	ctx, cancel := context.WithTimeout(context.Background(), forwardDialTimeout)
	probe, err := c.DialRemote(ctx, remote)
	cancel()
	if err != nil {
		return nil, err
	}
	_ = probe.Close()

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", portString(localPort)))
	if err != nil {
		return nil, fmt.Errorf("sshcore: listen on loopback: %w", err)
	}

	t := &Tunnel{
		conn:   c,
		remote: remote,
		ln:     ln,
		opened: time.Now(),
		live:   make(map[net.Conn]struct{}),
		sem:    make(chan struct{}, maxForwarded),
	}
	go t.accept()
	return t, nil
}

func portString(p int) string {
	if p <= 0 {
		return "0"
	}
	return fmt.Sprintf("%d", p)
}

// LocalAddr is the address to point a browser at.
func (t *Tunnel) LocalAddr() string { return t.ln.Addr().String() }

// LocalPort is the bound port on this machine.
func (t *Tunnel) LocalPort() int {
	if a, ok := t.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// Remote is the address on the server that this forwards to.
func (t *Tunnel) Remote() string { return t.remote }

// Opened is when the tunnel started carrying traffic.
func (t *Tunnel) Opened() time.Time { return t.opened }

func (t *Tunnel) accept() {
	for {
		local, err := t.ln.Accept()
		if err != nil {
			return // closed, or the listener died; either way this loop is done
		}

		t.mu.Lock()
		closed := t.closed
		t.mu.Unlock()
		if closed {
			_ = local.Close()
			return
		}

		select {
		case t.sem <- struct{}{}:
		default:
			// At the ceiling. Refusing one connection is visible and
			// recoverable; queueing them would look like a hung page.
			_ = local.Close()
			continue
		}
		go func() {
			defer func() { <-t.sem }()
			t.forward(local)
		}()
	}
}

// forward carries one accepted connection to the server and back.
func (t *Tunnel) forward(local net.Conn) {
	defer local.Close()

	ctx, cancel := context.WithTimeout(context.Background(), forwardDialTimeout)
	remote, err := t.conn.DialRemote(ctx, t.remote)
	cancel()
	if err != nil {
		return
	}
	defer remote.Close()

	// Close may have run between the accept and here. Tracking blind would
	// leave this pair outside every cleanup path — a forwarded connection with
	// no tunnel to close it.
	if !t.track(local, remote) {
		return
	}
	defer t.untrack(local, remote)

	// Both directions, and the first to finish ends the pair. A half-closed
	// forward is what leaves a browser tab spinning on a request the server
	// already answered.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
}

// track registers a forwarded pair, or reports false if the tunnel has closed
// underneath it — in which case the caller must drop the connections itself.
func (t *Tunnel) track(cs ...net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	for _, c := range cs {
		t.live[c] = struct{}{}
	}
	return true
}

func (t *Tunnel) untrack(cs ...net.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range cs {
		delete(t.live, c)
	}
}

// Close stops the listener and drops every connection riding on it.
//
// Closing the sockets rather than waiting for the copies to finish is
// deliberate: a websocket carries no traffic for minutes at a time, and a
// tunnel that only ends once its last idle connection does is a tunnel that
// does not end. The io.Copy pairs unblock on the closed socket.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	live := make([]net.Conn, 0, len(t.live))
	for c := range t.live {
		live = append(live, c)
	}
	t.live = make(map[net.Conn]struct{})
	t.mu.Unlock()

	err := t.ln.Close()
	for _, c := range live {
		_ = c.Close()
	}
	return err
}
