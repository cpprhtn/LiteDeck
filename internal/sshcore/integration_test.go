package sshcore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// These tests run against a throwaway sshd container (§10). They skip rather
// than fail when Docker is unavailable, so `go test ./...` still works on a
// machine without it — CI is where they are mandatory.

const (
	testImage = "litedeck-test-sshd"
	testUser  = "litedeck"
	testPass  = "litedeck"
)

var (
	sshdAddr string
	sshdSkip string // non-empty means "skip, for this reason"
)

func TestMain(m *testing.M) {
	// testing.Short() reads a flag, and flags are not parsed until this runs.
	flag.Parse()

	stop, err := startSSHD()
	if err != nil {
		sshdSkip = err.Error()
	}
	code := m.Run()
	if stop != nil {
		stop()
	}
	os.Exit(code)
}

// startSSHD builds and runs the fixture container, returning a teardown func.
func startSSHD() (func(), error) {
	// `go test -short` runs the unit tests alone. CI uses it to keep a Docker
	// outage from masking a genuine unit-test regression, and it makes the
	// local edit-test loop fast.
	if testing.Short() {
		return nil, errors.New("skipped by -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("docker not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker not running: %s", firstLine(out))
	}

	ctxDir := filepath.Join("..", "..", "testdata", "sshd")
	if out, err := exec.Command("docker", "build", "-q", "-t", testImage, ctxDir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker build: %s", firstLine(out))
	}

	out, err := exec.Command("docker", "run", "-d", "-P", testImage).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %s", firstLine(out))
	}
	id := strings.TrimSpace(string(out))
	stop := func() { _ = exec.Command("docker", "rm", "-f", id).Run() }

	portOut, err := exec.Command("docker", "port", id, "22/tcp").Output()
	if err != nil {
		stop()
		return nil, fmt.Errorf("docker port: %w", err)
	}
	// "0.0.0.0:32768" (and possibly an IPv6 line after it)
	hostPort := strings.TrimSpace(firstLine(portOut))
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		sshdAddr = "127.0.0.1" + hostPort[i:]
	} else {
		stop()
		return nil, fmt.Errorf("unexpected docker port output %q", hostPort)
	}

	if err := waitForSSHD(sshdAddr, 45*time.Second); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func waitForSSHD(addr string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		c, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
			User:            testUser,
			Auth:            []ssh.AuthMethod{ssh.Password(testPass)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), // readiness probe only
			Timeout:         2 * time.Second,
		})
		if err == nil {
			c.Close()
			return nil
		}
		last = err
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("sshd never became ready: %w", last)
}

func firstLine(b []byte) string {
	return strings.TrimSpace(string(bytes.SplitN(bytes.TrimSpace(b), []byte("\n"), 2)[0]))
}

// dialTest opens a connection to the fixture, skipping if it is unavailable.
func dialTest(t *testing.T) *Conn {
	t.Helper()
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}
	c, err := Dial(testCtx(t), HostConfig{
		ID:   "fixture",
		Addr: sshdAddr,
		User: testUser,
		Auth: []ssh.AuthMethod{ssh.Password(testPass)},
		// Host key behaviour has dedicated tests below; these cases are about
		// exec and SFTP, so the fixture's ephemeral key is accepted outright.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestExec(t *testing.T) {
	c := dialTest(t)
	ctx := testCtx(t)

	t.Run("stdout", func(t *testing.T) {
		res, err := c.Exec(ctx, "echo", "hello world")
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if got := strings.TrimSpace(string(res.Stdout)); got != "hello world" {
			t.Errorf("stdout = %q, want %q", got, "hello world")
		}
		if !res.OK() {
			t.Errorf("ExitCode = %d, want 0", res.ExitCode)
		}
	})

	t.Run("nonzero exit is a value not an error", func(t *testing.T) {
		res, err := c.Exec(ctx, "sh", "-c", "echo oops >&2; exit 3")
		if err != nil {
			t.Fatalf("Exec returned error for a clean non-zero exit: %v", err)
		}
		if res.ExitCode != 3 {
			t.Errorf("ExitCode = %d, want 3", res.ExitCode)
		}
		if got := strings.TrimSpace(string(res.Stderr)); got != "oops" {
			t.Errorf("stderr = %q, want %q", got, "oops")
		}
		if res.Err() == nil {
			t.Error("Result.Err() = nil, want an error carrying stderr")
		}
	})

	t.Run("stdin", func(t *testing.T) {
		res, err := c.ExecOpts(ctx, ExecOptions{Stdin: strings.NewReader("piped")}, "cat")
		if err != nil {
			t.Fatalf("ExecOpts: %v", err)
		}
		if got := string(res.Stdout); got != "piped" {
			t.Errorf("stdout = %q, want %q", got, "piped")
		}
	})
}

// TestExecArgumentSafety is the test that matters most in this package: an
// argument containing shell syntax must arrive as data, never as code (§3.2b).
func TestExecArgumentSafety(t *testing.T) {
	c := dialTest(t)
	ctx := testCtx(t)

	const marker = "/tmp/litedeck-injection-canary"
	payloads := []string{
		"; touch " + marker + "; echo ",
		"$(touch " + marker + ")",
		"`touch " + marker + "`",
		"&& touch " + marker,
		"| touch " + marker,
		"'; touch " + marker + "; '",
	}

	for _, p := range payloads {
		res, err := c.Exec(ctx, "printf", "%s", p)
		if err != nil {
			t.Fatalf("Exec with payload %q: %v", p, err)
		}
		if got := string(res.Stdout); got != p {
			t.Errorf("payload %q came back as %q — it was interpreted, not passed through", p, got)
		}
	}

	check, err := c.Exec(ctx, "test", "-e", marker)
	if err != nil {
		t.Fatalf("canary check: %v", err)
	}
	if check.OK() {
		t.Fatalf("INJECTION: %s exists, so a payload executed", marker)
	}
}

// TestConcurrentSessions exercises the one-connection-many-channels design
// (§3.2a): parallel work must not serialise or corrupt each other.
func TestConcurrentSessions(t *testing.T) {
	c := dialTest(t)
	ctx := testCtx(t)

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	outs := make([]string, n)

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("session-%d", i)
			res, err := c.Exec(ctx, "echo", want)
			errs[i] = err
			if res != nil {
				outs[i] = strings.TrimSpace(string(res.Stdout))
			}
		}()
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("session %d: %v", i, errs[i])
			continue
		}
		if want := fmt.Sprintf("session-%d", i); outs[i] != want {
			t.Errorf("session %d returned %q, want %q", i, outs[i], want)
		}
	}
}

func TestSFTPRoundTrip(t *testing.T) {
	c := dialTest(t)
	client, err := c.SFTP()
	if err != nil {
		t.Fatalf("SFTP: %v", err)
	}

	// A name that would be catastrophic if it ever reached a shell. No embedded
	// slash: this is one file in /tmp, not a nested path.
	const name = "/tmp/litedeck test's file; rm -rf *.txt"
	const body = "리트덱\nround trip\x00binary-safe payload"

	f, err := client.Create(name)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	f.Close()

	entries, err := client.ReadDir("/tmp")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == filepath.Base(name) {
			found = true
			if e.Size() != int64(len(body)) {
				t.Errorf("size = %d, want %d", e.Size(), len(body))
			}
		}
	}
	if !found {
		t.Errorf("ReadDir did not list %q", filepath.Base(name))
	}

	got, err := readAll(client, name)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != body {
		t.Errorf("content = %q, want %q", got, body)
	}

	if err := client.Remove(name); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := client.Stat(name); err == nil {
		t.Error("file still present after Remove")
	}
}

func readAll(c *sftp.Client, name string) (string, error) {
	f, err := c.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	return string(b), err
}

func TestContextCancellation(t *testing.T) {
	c := dialTest(t)
	ctx, cancel := context.WithTimeout(testCtx(t), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Exec(ctx, "sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Exec returned nil error for a cancelled command")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %v — the session was not closed promptly", elapsed)
	}
}

// TestHostKeyMismatchIsFatal pins the §7.1 rule: a contradicting key blocks the
// connection and cannot be approved away.
func TestHostKeyMismatchIsFatal(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}

	// Record a key for this address that the server certainly does not hold.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{sshdAddr}, signer.PublicKey())
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	// A prompter that would say yes to anything — it must never be consulted.
	prompter := &countingPrompter{decision: TrustAlways}
	kh, err := NewKnownHosts(path, prompter)
	if err != nil {
		t.Fatalf("NewKnownHosts: %v", err)
	}

	_, err = Dial(testCtx(t), HostConfig{
		ID:              "fixture",
		Addr:            sshdAddr,
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPass)},
		HostKeyCallback: kh.Callback(),
	})
	if err == nil {
		t.Fatal("Dial succeeded despite a host key mismatch")
	}
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v, want *HostKeyMismatchError", err)
	}
	if prompter.calls != 0 {
		t.Errorf("prompter consulted %d times on a mismatch; it must never be asked", prompter.calls)
	}
}

// TestHostKeyTOFU covers the first-contact path: prompt, then remember.
func TestHostKeyTOFU(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	prompter := &countingPrompter{decision: TrustAlways}
	kh, err := NewKnownHosts(path, prompter)
	if err != nil {
		t.Fatalf("NewKnownHosts: %v", err)
	}
	cfg := HostConfig{
		ID:              "fixture",
		Addr:            sshdAddr,
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPass)},
		HostKeyCallback: kh.Callback(),
	}

	c1, err := Dial(testCtx(t), cfg)
	if err != nil {
		t.Fatalf("first Dial: %v", err)
	}
	c1.Close()
	if prompter.calls != 1 {
		t.Errorf("first connect prompted %d times, want 1", prompter.calls)
	}
	if !strings.HasPrefix(prompter.last.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want SHA256: form", prompter.last.Fingerprint)
	}

	// Second connect must be silent: the key is now on record.
	c2, err := Dial(testCtx(t), cfg)
	if err != nil {
		t.Fatalf("second Dial: %v", err)
	}
	c2.Close()
	if prompter.calls != 1 {
		t.Errorf("second connect prompted again (%d total); known_hosts was not honoured", prompter.calls)
	}
}

func TestHostKeyRejection(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}
	path := filepath.Join(t.TempDir(), "known_hosts")
	kh, err := NewKnownHosts(path, &countingPrompter{decision: TrustReject})
	if err != nil {
		t.Fatalf("NewKnownHosts: %v", err)
	}
	_, err = Dial(testCtx(t), HostConfig{
		ID: "fixture", Addr: sshdAddr, User: testUser,
		Auth:            []ssh.AuthMethod{ssh.Password(testPass)},
		HostKeyCallback: kh.Callback(),
	})
	if !errors.Is(err, ErrHostKeyRejected) {
		t.Errorf("error = %v, want ErrHostKeyRejected", err)
	}
}

// TestObserverSeesEveryCommand backs the Command Log (§4.6).
func TestObserverSeesEveryCommand(t *testing.T) {
	c := dialTest(t)
	obs := &recordingObserver{}
	c.SetObserver(obs)

	if _, err := c.Exec(testCtx(t), "echo", "with space"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(obs.started) != 1 || obs.started[0] != "echo 'with space'" {
		t.Errorf("started = %v, want [echo 'with space']", obs.started)
	}
	if len(obs.finished) != 1 {
		t.Fatalf("finished = %v, want one entry", obs.finished)
	}

	// A command whose text must not be recorded verbatim (§7.2).
	obs.reset()
	_, err := c.ExecOpts(testCtx(t),
		ExecOptions{Stdin: strings.NewReader("hunter2\n"), LogLine: "sudo -S -p '' -- id"},
		"sh", "-c", "read x; echo redacted")
	if err != nil {
		t.Fatalf("ExecOpts: %v", err)
	}
	if len(obs.started) != 1 || obs.started[0] != "sudo -S -p '' -- id" {
		t.Errorf("started = %v, want the redacted line", obs.started)
	}
	for _, line := range append(obs.started, obs.finished...) {
		if strings.Contains(line, "hunter2") {
			t.Errorf("secret leaked into the Command Log: %q", line)
		}
	}
}

type countingPrompter struct {
	decision TrustDecision
	calls    int
	last     KeyInfo
}

func (p *countingPrompter) ConfirmNewHost(k KeyInfo) (TrustDecision, error) {
	p.calls++
	p.last = k
	return p.decision, nil
}

type recordingObserver struct {
	mu       sync.Mutex
	started  []string
	finished []string
}

func (o *recordingObserver) CommandStarted(info CommandInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, info.Line)
}

func (o *recordingObserver) CommandFinished(info CommandInfo, _ *Result, _ error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, info.Line)
}

func (o *recordingObserver) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started, o.finished = nil, nil
}
