package sshcore

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// cutProxy forwards TCP to the sshd fixture and can sever every connection on
// demand. Cutting here rather than restarting the container isolates the thing
// under test — the server stays up, only the network goes away, which is the
// case keepalive exists to detect.
type cutProxy struct {
	ln     net.Listener
	target string

	mu      sync.Mutex
	live    []net.Conn
	blocked bool
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &cutProxy{ln: ln, target: target}
	go p.accept()
	t.Cleanup(p.Close)
	return p
}

func (p *cutProxy) Addr() string { return p.ln.Addr().String() }

func (p *cutProxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		blocked := p.blocked
		p.mu.Unlock()
		if blocked {
			c.Close()
			continue
		}
		go p.pipe(c)
	}
}

func (p *cutProxy) pipe(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		client.Close()
		return
	}
	p.mu.Lock()
	p.live = append(p.live, client, server)
	p.mu.Unlock()

	go func() {
		_, _ = io.Copy(server, client)
		server.Close()
	}()
	_, _ = io.Copy(client, server)
	client.Close()
}

// Cut drops every live connection and refuses new ones.
func (p *cutProxy) Cut() {
	p.mu.Lock()
	p.blocked = true
	live := p.live
	p.live = nil
	p.mu.Unlock()
	for _, c := range live {
		c.Close()
	}
}

// Restore starts accepting again.
func (p *cutProxy) Restore() {
	p.mu.Lock()
	p.blocked = false
	p.mu.Unlock()
}

func (p *cutProxy) Close() {
	p.Cut()
	p.ln.Close()
}

// fastManager tunes the timers down so a reconnect completes inside a test.
// The ratios match the production defaults in §3.2a.
func fastManager(states chan<- State) *Manager {
	return NewManager(ManagerOptions{
		KeepaliveInterval:   150 * time.Millisecond,
		KeepaliveTimeout:    400 * time.Millisecond,
		MaxMissedKeepalives: 2,
		ReconnectMinBackoff: 100 * time.Millisecond,
		ReconnectMaxBackoff: 2 * time.Second,
	}, func(_ string, s State, _ error) {
		select {
		case states <- s:
		default:
		}
	})
}

func testHostConfig(id, addr string) HostConfig {
	return HostConfig{
		ID:              id,
		Addr:            addr,
		User:            testUser,
		Auth:            []ssh.AuthMethod{Password(func() (string, error) { return testPass, nil })},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
}

func waitForState(t *testing.T, ch <-chan State, want State, limit time.Duration) {
	t.Helper()
	deadline := time.After(limit)
	for {
		select {
		case s := <-ch:
			if s == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out after %v waiting for state %v", limit, want)
		}
	}
}

// TestReconnectAfterNetworkDrop is the §3.2a keepalive requirement end to end:
// unanswered keepalives must be noticed and the connection rebuilt by itself.
func TestReconnectAfterNetworkDrop(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	proxy := newCutProxy(t, sshdAddr)
	states := make(chan State, 64)
	m := fastManager(states)
	t.Cleanup(func() { m.Close() })

	if err := m.Connect(testCtx(t), testHostConfig("h1", proxy.Addr()), nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := m.State("h1"); got != StateConnected {
		t.Fatalf("State = %v, want connected", got)
	}

	res, err := m.Exec(testCtx(t), "h1", "echo", "before")
	if err != nil {
		t.Fatalf("Exec before cut: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "before" {
		t.Fatalf("stdout = %q, want %q", got, "before")
	}

	proxy.Cut()
	waitForState(t, states, StateReconnecting, 5*time.Second)

	proxy.Restore()
	waitForState(t, states, StateConnected, 15*time.Second)

	// The manager resolves the connection per call, so the caller never learns
	// that the underlying *Conn was replaced.
	res, err = m.Exec(testCtx(t), "h1", "echo", "after")
	if err != nil {
		t.Fatalf("Exec after reconnect: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "after" {
		t.Errorf("stdout = %q, want %q", got, "after")
	}
}

// TestKeepaliveDoesNotFalsePositive guards the other direction: an idle but
// healthy connection must never be torn down. A flapping host list would be
// worse than no status indicator at all.
func TestKeepaliveDoesNotFalsePositive(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	states := make(chan State, 64)
	m := fastManager(states)
	t.Cleanup(func() { m.Close() })

	if err := m.Connect(testCtx(t), testHostConfig("idle", sshdAddr), nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	drain(states)

	// ~13 keepalive rounds at the fast interval.
	time.Sleep(2 * time.Second)

	for {
		select {
		case s := <-states:
			if s != StateConnected {
				t.Fatalf("idle connection reported %v; keepalive false-positived", s)
			}
		default:
			if got := m.State("idle"); got != StateConnected {
				t.Fatalf("State = %v, want connected", got)
			}
			return
		}
	}
}

func TestManagerLifecycle(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	states := make(chan State, 64)
	m := fastManager(states)
	t.Cleanup(func() { m.Close() })

	cfg := testHostConfig("lc", sshdAddr)
	if err := m.Connect(testCtx(t), cfg, nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := m.Connect(testCtx(t), cfg, nil); err == nil {
		t.Error("connecting an already-connected host ID succeeded; want an error")
	}

	if _, err := m.SFTP("lc"); err != nil {
		t.Errorf("SFTP: %v", err)
	}

	if err := m.Disconnect("lc"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if got := m.State("lc"); got != StateDisconnected {
		t.Errorf("State after Disconnect = %v, want disconnected", got)
	}
	if _, err := m.Exec(testCtx(t), "lc", "echo", "x"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Exec after Disconnect: err = %v, want ErrNotConnected", err)
	}
	if err := m.Disconnect("lc"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("double Disconnect: err = %v, want ErrNotConnected", err)
	}

	// Reconnecting the same ID afterwards must work.
	if err := m.Connect(testCtx(t), cfg, nil); err != nil {
		t.Errorf("reconnect after disconnect: %v", err)
	}
}

func drain(ch <-chan State) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// TestGenerationChangesWhenTheConnectionDoes is the half of the stale-cache fix
// that only a real drop can show.
//
// Callers cache facts read from the server — process IDs above all — and those
// are true of one connection, not of a host. Reconnect replaces the connection
// silently, which is correct for anyone running a command and wrong for anyone
// holding such a fact. Generation is how they tell the difference, so it has to
// move when the connection is replaced without anybody asking.
func TestGenerationChangesWhenTheConnectionDoes(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	proxy := newCutProxy(t, sshdAddr)
	states := make(chan State, 64)
	m := fastManager(states)
	t.Cleanup(func() { m.Close() })

	if got := m.Generation("h1"); got != 0 {
		t.Fatalf("Generation before connect = %d, want 0", got)
	}
	if err := m.Connect(testCtx(t), testHostConfig("h1", proxy.Addr()), nil); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	first := m.Generation("h1")
	if first == 0 {
		t.Fatal("Generation is still 0 on a connected host")
	}
	// Stable while the connection lives, or every caller re-probes constantly.
	if again := m.Generation("h1"); again != first {
		t.Fatalf("Generation moved without a reconnect: %d then %d", first, again)
	}

	proxy.Cut()
	waitForState(t, states, StateReconnecting, 5*time.Second)
	proxy.Restore()
	waitForState(t, states, StateConnected, 15*time.Second)

	second := m.Generation("h1")
	if second == first {
		t.Fatalf("Generation is still %d after the connection was replaced; "+
			"anything cached from the old one would go on answering for the new one", first)
	}

	// And it does not restart when the same host ID connects again, which a
	// per-host counter would do — handing the second connection the first one's
	// number and defeating the check entirely.
	if err := m.Disconnect("h1"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := m.Connect(testCtx(t), testHostConfig("h1", proxy.Addr()), nil); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if third := m.Generation("h1"); third == first || third == second {
		t.Fatalf("Generation reused %d after disconnect and connect", third)
	}
}
