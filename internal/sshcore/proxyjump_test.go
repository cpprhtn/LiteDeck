package sshcore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ProxyJump against two real containers (§4.1, §10).
//
// A fake bastion would prove nothing: the interesting part is that the target
// is reachable *only* through the hop, and that the target's own host key is
// still the one checked. Both need a target that genuinely has no route from
// this machine, which is a private Docker network and nothing else.

type jumpFixture struct {
	bastionAddr string // reachable from this machine
	targetAddr  string // reachable only from inside the network
}

func startJumpFixture(t *testing.T) jumpFixture {
	t.Helper()
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	net := "litedeck-jump-" + t.Name()
	run(t, "docker", "network", "create", net)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", net).Run() })

	// Host keys are baked into the image at build time, so two containers of it
	// are the same server as far as key verification can tell — and the whole
	// question here is whether the target's own key is the one checked.
	// Regenerating per container is also what two real machines look like.
	freshKeys := "rm -f /etc/ssh/ssh_host_* && ssh-keygen -A >/dev/null && exec /usr/sbin/sshd -D -e"

	// No published port. This is what makes the test mean something: if the
	// jump were not happening, there would be nowhere to connect to.
	target := run(t, "docker", "run", "-d", "--network", net,
		"--network-alias", "target", testImage, "sh", "-c", freshKeys)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", target).Run() })

	// Alpine's OpenSSH package ships AllowTcpForwarding off, which upstream
	// does not. Turned on here rather than in the shared image so the refusal
	// stays reachable for the test that asserts on it.
	bastion := run(t, "docker", "run", "-d", "--network", net, "-P", testImage,
		"sh", "-c", freshKeys+" -o AllowTcpForwarding=yes")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", bastion).Run() })

	portOut := run(t, "docker", "port", bastion, "22/tcp")
	line := firstLine([]byte(portOut))
	i := strings.LastIndex(line, ":")
	if i < 0 {
		t.Fatalf("unexpected docker port output %q", portOut)
	}
	f := jumpFixture{bastionAddr: "127.0.0.1" + line[i:], targetAddr: "target:22"}

	if err := waitForSSHD(f.bastionAddr, 45*time.Second); err != nil {
		t.Fatalf("bastion never came up: %v", err)
	}
	// The target has no published port, so readiness is asked for from inside.
	deadline := time.Now().Add(45 * time.Second)
	for {
		out, _ := exec.Command("docker", "exec", bastion,
			"sh", "-c", "nc -z target 22 && echo up").CombinedOutput()
		if strings.Contains(string(out), "up") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("target never came up: %s", out)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return f
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func jumpCfg(addr string, jump *HostConfig) HostConfig {
	return HostConfig{
		ID:              "jumped",
		Addr:            addr,
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
		Jump:            jump,
	}
}

func TestProxyJumpReachesAHostThisMachineCannot(t *testing.T) {
	f := startJumpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The premise, stated as a test rather than assumed: "target:22" is not a
	// thing this machine can dial. If this ever starts passing, the test below
	// has stopped proving anything.
	if c, err := net.DialTimeout("tcp", f.targetAddr, 3*time.Second); err == nil {
		c.Close()
		t.Fatal("the target is directly reachable, so this test cannot show that the jump did the work")
	}

	bastion := jumpCfg(f.bastionAddr, nil)
	bastion.ID = "bastion"
	conn, err := Dial(ctx, jumpCfg(f.targetAddr, &bastion))
	if err != nil {
		t.Fatalf("Dial through the bastion: %v", err)
	}
	defer conn.Close()

	// Whose shell is this? The bastion and the target run the same image, so
	// asking the hostname is the only way to tell them apart — and the answer
	// has to be the container we could not reach.
	res, err := conn.Exec(ctx, "hostname")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := strings.TrimSpace(string(res.Stdout))
	if got == "" {
		t.Fatal("hostname returned nothing")
	}

	direct, err := Dial(ctx, jumpCfg(f.bastionAddr, nil))
	if err != nil {
		t.Fatalf("Dial the bastion directly: %v", err)
	}
	defer direct.Close()
	res2, err := direct.Exec(ctx, "hostname")
	if err != nil {
		t.Fatalf("Exec on the bastion: %v", err)
	}
	if bastionName := strings.TrimSpace(string(res2.Stdout)); got == bastionName {
		t.Errorf("the session landed on the bastion (%s), not through it", bastionName)
	}
}

// TestProxyJumpVerifiesTheBastionsHostKey: the hop is worthless if anyone who
// answers on the bastion's port is trusted. A refusal there must abort the
// whole dial, not fall through to a direct attempt.
func TestProxyJumpVerifiesTheBastionsHostKey(t *testing.T) {
	f := startJumpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	refused := errors.New("host key refused")
	bastion := jumpCfg(f.bastionAddr, nil)
	bastion.ID = "bastion"
	bastion.HostKeyCallback = func(string, net.Addr, ssh.PublicKey) error { return refused }

	conn, err := Dial(ctx, jumpCfg(f.targetAddr, &bastion))
	if err == nil {
		conn.Close()
		t.Fatal("a bastion whose host key was rejected still produced a connection")
	}
	if !strings.Contains(err.Error(), refused.Error()) {
		t.Errorf("error = %v, want it to carry the host key refusal", err)
	}
	if !strings.Contains(err.Error(), "bastion") {
		t.Errorf("error = %v, want it to say the bastion leg failed", err)
	}
}

// TestProxyJumpChecksTheTargetsOwnHostKey: tunnelling must not weaken the
// check at the far end. The bastion is trusted to forward bytes, never to
// vouch for who is on the other side of them.
func TestProxyJumpChecksTheTargetsOwnHostKey(t *testing.T) {
	f := startJumpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bastion := jumpCfg(f.bastionAddr, nil)
	bastion.ID = "bastion"

	var seen ssh.PublicKey
	cfg := jumpCfg(f.targetAddr, &bastion)
	cfg.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		seen = key
		return fmt.Errorf("no")
	}
	if conn, err := Dial(ctx, cfg); err == nil {
		conn.Close()
		t.Fatal("the target's host key was rejected and the dial still succeeded")
	}
	if seen == nil {
		t.Fatal("the target's host key callback was never called — the tunnel skipped verification")
	}

	// And it is the target's key, not the bastion's.
	direct, err := Dial(ctx, jumpCfg(f.bastionAddr, nil))
	if err != nil {
		t.Fatalf("Dial the bastion: %v", err)
	}
	defer direct.Close()
	var bastionKey ssh.PublicKey
	probe := jumpCfg(f.bastionAddr, nil)
	probe.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		bastionKey = key
		return nil
	}
	c2, err := Dial(ctx, probe)
	if err != nil {
		t.Fatalf("Dial the bastion again: %v", err)
	}
	defer c2.Close()
	if string(seen.Marshal()) == string(bastionKey.Marshal()) {
		t.Error("the key checked for the target was the bastion's")
	}
}

// TestProxyJumpSaysWhichLegFailedWhenForwardingIsOff: a bastion with
// AllowTcpForwarding off answers "administratively prohibited", which reads as
// a problem with the account. The error has to name the bastion, or the user
// goes looking on the wrong machine.
func TestProxyJumpSaysWhichLegFailedWhenForwardingIsOff(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}
	// The stock fixture, whose Alpine sshd forbids forwarding.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	bastion := jumpCfg(sshdAddr, nil)
	bastion.ID = "bastion"
	conn, err := Dial(ctx, jumpCfg("192.0.2.1:22", &bastion))
	if err == nil {
		conn.Close()
		t.Fatal("a bastion that refuses forwarding still produced a connection")
	}
	if !strings.Contains(err.Error(), sshdAddr) {
		t.Errorf("error = %v, want it to name the bastion %s", err, sshdAddr)
	}
	if !strings.Contains(err.Error(), "administratively prohibited") {
		t.Errorf("error = %v, want sshd's own refusal preserved", err)
	}
}

// TestProxyJumpClosesTheBastionWithTheSession: an orphaned bastion connection
// is an idle login left sitting on somebody else's jump host.
func TestProxyJumpClosesTheBastionWithTheSession(t *testing.T) {
	f := startJumpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bastion := jumpCfg(f.bastionAddr, nil)
	bastion.ID = "bastion"
	conn, err := Dial(ctx, jumpCfg(f.targetAddr, &bastion))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if conn.jump == nil {
		t.Fatal("the connection did not keep hold of its bastion")
	}
	jumped := conn.jump
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := jumped.Exec(ctx, "true"); err == nil {
		t.Error("the bastion connection is still usable after the session it carried was closed")
	}
}
