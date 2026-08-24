package sshcore

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshd's own wording for AllowTcpForwarding being off reads as a problem with
// the account, and people go looking in the wrong place. The bastion path
// already learned this the hard way; the forward path must not have to.
func TestForwardErrorNamesTheConfigLine(t *testing.T) {
	err := forwardError("127.0.0.1:3001",
		errors.New("ssh: rejected: administratively prohibited (open failed)"))

	if !errors.Is(err, ErrForwardingDenied) {
		t.Errorf("callers cannot tell this apart from a dead service: %v", err)
	}
	if !strings.Contains(err.Error(), "AllowTcpForwarding") {
		t.Errorf("should name the setting to change: %v", err)
	}
}

func TestForwardErrorKeepsEverythingElseVerbatim(t *testing.T) {
	orig := errors.New("connect failed: connection refused")
	err := forwardError("127.0.0.1:3001", orig)

	if errors.Is(err, ErrForwardingDenied) {
		t.Errorf("a refused connection is not a forwarding policy: %v", err)
	}
	// §8: the original text has to survive.
	if !errors.Is(err, orig) || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("original error was swallowed: %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:3001") {
		t.Errorf("should name the target: %v", err)
	}
}

func TestOpenTunnelRejectsABareHost(t *testing.T) {
	// A *Conn is not needed: the target is checked before anything is dialled,
	// which is the point — "3001" is a typo, not a network failure.
	c := &Conn{}
	if _, err := c.OpenTunnel("3001", 0); err == nil {
		t.Error("a target without a port was accepted")
	}
}

/* ----------------------------------------------------- against a real sshd */

// forwardingSSHD dials a fixture container that permits forwarding.
//
// A container of its own rather than a change to the shared image, for the
// reason the ProxyJump fixture already gives: Alpine's openssh package ships
// AllowTcpForwarding **off** where upstream ships it on, and the stock image
// has to keep refusing so the test that asserts on the refusal still has a
// server that produces it. Turning it on in the Dockerfile made that test fail
// with a timeout instead — which is how this comment came to be here.
func forwardingSSHD(t *testing.T) *Conn {
	t.Helper()
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	id := run(t, "docker", "run", "-d", "-P", testImage,
		"sh", "-c", "exec /usr/sbin/sshd -D -e -o AllowTcpForwarding=yes")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	line := firstLine([]byte(run(t, "docker", "port", id, "22/tcp")))
	i := strings.LastIndex(line, ":")
	if i < 0 {
		t.Fatalf("unexpected docker port output %q", line)
	}
	addr := "127.0.0.1" + line[i:]
	if err := waitForSSHD(addr, 45*time.Second); err != nil {
		t.Fatalf("fixture never came up: %v", err)
	}

	c, err := Dial(testCtx(t), HostConfig{
		ID:              "forwarding-fixture",
		Addr:            addr,
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // key behaviour is tested elsewhere
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// The stock fixture refuses forwarding, so it is also the server that proves
// the refusal is reported as a policy rather than as a dead service. Real sshd
// wording, not a hand-written error.
func TestOpenTunnelReportsARefusingServer(t *testing.T) {
	c := dialTest(t)

	_, err := c.OpenTunnel("127.0.0.1:22", 0)
	if err == nil {
		t.Fatal("a server with AllowTcpForwarding off still produced a tunnel")
	}
	if !errors.Is(err, ErrForwardingDenied) {
		t.Errorf("error = %v, want it recognised as a forwarding policy", err)
	}
	if !strings.Contains(err.Error(), "AllowTcpForwarding") {
		t.Errorf("error = %v, want it to name the setting", err)
	}
}

// The whole path, end to end: a listener on this machine whose bytes come out
// on the server.
//
// The far side is the fixture's own sshd on its loopback. That is deliberate —
// it needs nothing installed in the container, and an SSH banner is a reply
// that could not have come from anywhere else. Every part under test is real:
// the direct-tcpip channel, the local listener, and the copying between them.
func TestTunnelCarriesTrafficToTheServersLoopback(t *testing.T) {
	c := forwardingSSHD(t)

	tun, err := c.OpenTunnel("127.0.0.1:22", 0)
	if err != nil {
		t.Fatalf("OpenTunnel: %v", err)
	}
	defer tun.Close()

	// Loopback and nothing else. Binding a forward into a local-only service on
	// 0.0.0.0 would publish from this machine exactly what the server-side bind
	// was keeping off the network.
	host, _, err := net.SplitHostPort(tun.LocalAddr())
	if err != nil {
		t.Fatalf("LocalAddr %q: %v", tun.LocalAddr(), err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("tunnel bound %q, which is reachable from off this machine", host)
	}
	if tun.LocalPort() == 0 {
		t.Error("LocalPort did not report the bound port")
	}

	local, err := net.DialTimeout("tcp", tun.LocalAddr(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial the tunnel: %v", err)
	}
	defer local.Close()

	_ = local.SetReadDeadline(time.Now().Add(10 * time.Second))
	banner, err := bufio.NewReader(local).ReadString('\n')
	if err != nil {
		t.Fatalf("read from the far side: %v", err)
	}
	if !strings.HasPrefix(banner, "SSH-") {
		t.Fatalf("got %q from the tunnel, want the fixture's SSH banner", banner)
	}
}

// A tunnel that outlived its Close would be a port on the user's machine that
// accepts browsers and then hangs them.
func TestTunnelCloseStopsListening(t *testing.T) {
	c := forwardingSSHD(t)

	tun, err := c.OpenTunnel("127.0.0.1:22", 0)
	if err != nil {
		t.Fatalf("OpenTunnel: %v", err)
	}
	addr := tun.LocalAddr()

	// A connection still riding the tunnel, to prove Close does not wait for it.
	// A websocket is idle for minutes at a time, and a tunnel that only ends
	// once its last idle connection does is a tunnel that does not end.
	held, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial the tunnel: %v", err)
	}
	defer held.Close()

	// Read something first. Accepting is asynchronous, and until the far side
	// has answered there is no forwarded pair for Close to have to deal with —
	// without this the test passes whether or not Close waits, which is the one
	// thing it exists to check.
	_ = held.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := bufio.NewReader(held).ReadString('\n'); err != nil {
		t.Fatalf("the held connection never carried anything: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- tun.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a connection that was still open")
	}

	if _, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
		t.Error("the port still accepts connections after Close")
	}
	// Closing twice is what every cleanup path does; it must not be an error.
	if err := tun.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// DialRemote resolves against the *server*, not this machine. That is the whole
// reason the feature exists, and it is not visible from the type signature.
func TestDialRemoteIsTheServersLoopback(t *testing.T) {
	c := forwardingSSHD(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	nc, err := c.DialRemote(ctx, "127.0.0.1:22")
	if err != nil {
		t.Fatalf("DialRemote: %v", err)
	}
	defer nc.Close()

	_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	banner, err := bufio.NewReader(nc).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(banner, "SSH-") {
		t.Fatalf("got %q, want the fixture's SSH banner", banner)
	}
}
