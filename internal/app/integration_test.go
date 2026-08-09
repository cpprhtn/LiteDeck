package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The whole flow a user performs, against a real server: connect, answer the
// host key dialog, type a password, detect the server, list services, restart
// one, and watch it come back (§1.6's scenario).
//
// Everything below the webview is exercised — bindings, prompt bridge, keychain
// path, adapter, Command Log. The container runs genuine systemd, so the JSON
// versus table decision is made on a real version rather than a guess.

const (
	systemdImage = "litedeck-test-systemd"
	sysUser      = "litedeck"
	sysPass      = "Tr0ub4dor-fixture-pw"
)

var (
	sysAddr string
	sysSkip string
	// sysContainer is the fixture's container id. Exposed so a test can create a
	// login from outside LiteDeck — the session tests need one that is genuinely
	// somebody else's, which cannot be made through the app's own connection.
	sysContainer string
)

func TestMain(m *testing.M) {
	// testing.Short() reads a flag, and flags are not parsed until this runs.
	flag.Parse()

	stop, err := startSystemd()
	if err != nil {
		sysSkip = err.Error()
	}
	stopDocker, derr := startDocker()
	if derr != nil {
		dockerSkip = derr.Error()
	}

	code := m.Run()

	if stop != nil {
		stop()
	}
	if stopDocker != nil {
		stopDocker()
	}
	os.Exit(code)
}

// waitPort resolves the host port a fixture container published for sshd.
func waitPort(id string) (string, error) {
	out, err := exec.Command("docker", "port", id, "22/tcp").Output()
	if err != nil {
		return "", fmt.Errorf("docker port: %w", err)
	}
	hostPort := strings.TrimSpace(firstLine(out))
	i := strings.LastIndex(hostPort, ":")
	if i < 0 {
		return "", fmt.Errorf("unexpected docker port output %q", hostPort)
	}
	return "127.0.0.1" + hostPort[i:], nil
}

func startSystemd() (func(), error) {
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

	ctxDir := filepath.Join("..", "..", "testdata", "systemd")
	if out, err := exec.Command("docker", "build", "-q", "-t", systemdImage, ctxDir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker build: %s", firstLine(out))
	}

	// systemd needs a writable cgroup hierarchy and privileges; without them
	// PID 1 exits immediately and nothing else in this file means anything.
	out, err := exec.Command("docker", "run", "-d",
		"--privileged", "--cgroupns=host",
		"-v", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"-P", systemdImage).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %s", firstLine(out))
	}
	id := strings.TrimSpace(string(out))
	sysContainer = id
	stop := func() { _ = exec.Command("docker", "rm", "-f", id).Run() }

	portOut, err := exec.Command("docker", "port", id, "22/tcp").Output()
	if err != nil {
		stop()
		return nil, fmt.Errorf("docker port: %w", err)
	}
	hostPort := strings.TrimSpace(firstLine(portOut))
	i := strings.LastIndex(hostPort, ":")
	if i < 0 {
		stop()
		return nil, fmt.Errorf("unexpected docker port output %q", hostPort)
	}
	sysAddr = "127.0.0.1" + hostPort[i:]

	if err := waitForSystemd(id, 90*time.Second); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

// waitForSystemd waits for the init system to finish booting. Connecting before
// then produces a half-populated service list and a flaky test.
func waitForSystemd(id string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "exec", id, "systemctl", "is-system-running").CombinedOutput()
		last = strings.TrimSpace(string(out))
		if last == "running" || last == "degraded" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("systemd never finished booting (last state %q)", last)
}

func firstLine(b []byte) string {
	return strings.TrimSpace(string(bytes.SplitN(bytes.TrimSpace(b), []byte("\n"), 2)[0]))
}

// liveApp builds an App pointed at a temporary config directory, with a fake
// frontend that answers prompts the way a user would.
func liveApp(t *testing.T) (*App, *recorder) {
	t.Helper()
	if sysSkip != "" {
		t.Skipf("systemd fixture unavailable: %s", sysSkip)
	}

	dir := t.TempDir()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatalf("config.Open: %v", err)
	}

	a := New()
	rec := newRecorder()
	a.emit = rec.emit
	a.secrets = newMemSecrets()
	a.configDir = dir
	a.hosts = store
	a.mgr = sshcore.NewManager(sshcore.ManagerOptions{}, a.emitConnectionState)
	t.Cleanup(func() { _ = a.mgr.Close() })

	host := config.Host{
		ID:       "fixture",
		Name:     "systemd fixture",
		Hostname: "127.0.0.1",
		User:     sysUser,
		Auth:     []config.AuthMethod{config.AuthPassword},
	}
	_, portStr, _ := strings.Cut(sysAddr, ":")
	fmt.Sscanf(portStr, "%d", &host.Port)
	if err := a.hosts.Upsert(host); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return a, rec
}

// autoAnswer plays the user: trust the key, type the password.
func autoAnswer(t *testing.T, a *App, rec *recorder, trust string, remember bool) func() {
	t.Helper()
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			select {
			case <-done:
				return
			case p := <-rec.hostKeys:
				if err := a.AnswerHostKey(p.ID, trust); err != nil {
					t.Errorf("AnswerHostKey: %v", err)
				}
			case p := <-rec.secrets:
				if err := a.AnswerSecret(p.ID, sysPass, remember); err != nil {
					t.Errorf("AnswerSecret: %v", err)
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func TestFullConnectFlow(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)
	defer stop()

	// 1. Connect, which walks the whole prompt bridge.
	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	if got := a.HostState("fixture"); got != "connected" {
		t.Fatalf("state = %q, want connected", got)
	}

	// Trusting "always" must have written known_hosts (§7.1).
	kh := config.KnownHostsPath(a.configDir)
	if data, err := os.ReadFile(kh); err != nil || len(data) == 0 {
		t.Errorf("known_hosts not written: %v", err)
	}
	// Remembering must have reached the keychain (§6).
	if v, err := a.secrets.Get("fixture", secret.KindPassword); err != nil || v != sysPass {
		t.Errorf("password not remembered: %q %v", v, err)
	}

	// 2. Detect: the version decides the output format, and getting that wrong
	// fails silently on older systemd (§3.4).
	info, err := a.DetectHost("fixture")
	if err != nil {
		t.Fatalf("DetectHost: %v", err)
	}
	if !info.HasSystemd || info.SystemdVersion == 0 {
		t.Fatalf("systemd not detected: %+v", info.ServerInfo)
	}
	if !strings.Contains(strings.ToLower(info.PrettyName), "ubuntu") {
		t.Errorf("PrettyName = %q", info.PrettyName)
	}
	if !info.Capabilities["services"] {
		t.Error("services capability not reported despite systemd")
	}
	if info.Capabilities["containers"] {
		t.Error("containers capability reported on a host with no docker")
	}

	// 3. The service table, merged from two commands.
	units, err := a.ListServices("fixture")
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(units) < 20 {
		t.Fatalf("only %d units; the merge looks wrong", len(units))
	}

	byName := map[string]int{}
	for i, u := range units {
		byName[u.Name] = i
	}
	worker, ok := byName["litedeck-worker.service"]
	if !ok {
		t.Fatal("litedeck-worker.service missing from the merged table")
	}
	if units[worker].Active != "active" || units[worker].Enabled != "enabled" {
		t.Errorf("worker = %+v, want runtime and install state combined", units[worker])
	}
	// A disabled unit is never loaded, so it exists only in list-unit-files.
	// Losing it would mean the user could not enable it — the reason they came.
	idle, ok := byName["litedeck-idle.service"]
	if !ok {
		t.Fatal("litedeck-idle.service missing; the merge dropped unloaded units")
	}
	if units[idle].Enabled != "disabled" {
		t.Errorf("idle = %+v, want disabled", units[idle])
	}

	// 4. Restart a service. As an ordinary user this is refused, and the app
	// must say so rather than fail — §7.2 forbids reaching for root uninvited.
	first := a.ServiceAction("fixture", "litedeck-worker.service", "restart", false)
	if first.OK {
		t.Log("restart succeeded unprivileged; server policy allows it")
	} else if !first.NeedsElevation {
		t.Fatalf("restart failed without offering elevation: %+v", first)
	} else {
		// The user presses "retry as administrator".
		elevated := a.ServiceAction("fixture", "litedeck-worker.service", "restart", true)
		if !elevated.OK {
			t.Fatalf("elevated restart failed: %+v", elevated)
		}
	}
	if err := waitForActive(a, "litedeck-worker.service", 20*time.Second); err != nil {
		t.Error(err)
	}

	// 5. Every command must have reached the Command Log (§4.6).
	log := a.CommandLog()
	if len(log) == 0 {
		t.Fatal("Command Log is empty")
	}
	// The unelevated attempt is expected to fail and be logged as such — that
	// honesty is the point of the panel. What must exist is a successful run.
	var sawRestart bool
	for _, e := range log {
		if strings.Contains(e.Line, "systemctl restart -- litedeck-worker.service") && e.Status == "ok" {
			sawRestart = true
		}
		// The sudo command line is expected to appear in full — that is the
		// transparency §4.6 exists for. What must never appear is the password,
		// which is why it travels on stdin and not in argv (§7.2). The check
		// below covers every line, sudo or not.
		if strings.Contains(e.Line, sysPass) {
			t.Errorf("password leaked into the Command Log: %q", e.Line)
		}
		if e.Status == "running" {
			t.Errorf("entry never finished: %q", e.Line)
		}
	}
	if !sawRestart {
		t.Error("no successful restart appeared in the Command Log")
	}

	// 6. Disconnect drops the detection cache so an upgraded server is re-probed.
	if err := a.DisconnectHost("fixture"); err != nil {
		t.Fatalf("DisconnectHost: %v", err)
	}
	if _, ok := a.detected.get("fixture"); ok {
		t.Error("detection cache survived a disconnect")
	}
	if _, err := a.ListServices("fixture"); err == nil {
		t.Error("ListServices succeeded after disconnect")
	}
}

func waitForActive(a *App, unit string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		units, err := a.ListServices("fixture")
		if err != nil {
			return err
		}
		for _, u := range units {
			if u.Name == unit && u.Active == "active" {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s did not become active within %s", unit, limit)
}

// TestSecondConnectSkipsPrompts is the return visit: a remembered key and
// password mean the user gets straight in. If this regresses, every connection
// nags twice.
func TestSecondConnectSkipsPrompts(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)

	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("first ConnectHost: %v", err)
	}
	if err := a.DisconnectHost("fixture"); err != nil {
		t.Fatal(err)
	}
	stop()

	// No auto-answer this time: any prompt now would block until the timeout,
	// so a prompt-free reconnect is what makes this finish at all.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case p := <-rec.hostKeys:
				t.Errorf("asked about a host key that was already trusted: %+v", p)
				_ = a.AnswerHostKey(p.ID, "reject")
			case p := <-rec.secrets:
				t.Errorf("asked for a secret that was already remembered: %+v", p)
				_ = a.CancelPrompt(p.ID)
			case <-time.After(3 * time.Second):
				return
			}
		}
	}()

	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("second ConnectHost: %v", err)
	}
	<-drained
}

// TestUnsupportedServiceActionRejected keeps the systemctl verb allowlist
// honest: a frontend bug must not become an arbitrary subcommand.
func TestUnsupportedServiceActionRejected(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", false)
	defer stop()

	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	for _, verb := range []string{"mask", "cat", "poweroff", "--version"} {
		if res := a.ServiceAction("fixture", "litedeck-worker.service", verb, false); res.OK {
			t.Errorf("action %q was accepted", verb)
		}
	}
}

// TestProcessView covers §4.4 end to end: list, tree, signal, and the guards
// that stop the GUI from doing something unrecoverable (§7.4).
func TestProcessView(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)
	defer stop()

	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}

	procs, err := a.ListProcesses("fixture", false)
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	if len(procs) < 3 {
		t.Fatalf("only %d processes; the parse looks wrong", len(procs))
	}

	byPID := map[int]int{}
	for i, p := range procs {
		byPID[p.PID] = i
	}
	init, ok := byPID[1]
	if !ok {
		t.Fatal("PID 1 missing from the listing")
	}
	if procs[init].Command != "systemd" || procs[init].User != "root" {
		t.Errorf("PID 1 = %+v", procs[init])
	}

	// Tree order must keep every row and start at init.
	tree, err := a.ListProcesses("fixture", true)
	if err != nil {
		t.Fatalf("ListProcesses(tree): %v", err)
	}
	if len(tree) != len(procs) {
		t.Errorf("tree has %d rows, flat had %d", len(tree), len(procs))
	}
	if tree[0].PID != 1 || tree[0].Depth != 0 {
		t.Errorf("tree does not start at init: %+v", tree[0])
	}

	// §7.4: signalling init would take the server down. No dialog makes that
	// something a file manager should offer, so it is refused outright.
	if res := a.KillProcess("fixture", 1, "TERM", false); res.OK {
		t.Error("signalling PID 1 was allowed")
	}
	// Signals outside the allowlist must not reach the server.
	for _, sig := range []string{"9", "STOP", "SEGV", "; rm -rf /"} {
		if res := a.KillProcess("fixture", 2, sig, false); res.OK {
			t.Errorf("signal %q was accepted", sig)
		}
	}
	// Nice values outside POSIX range are rejected before any command runs.
	for _, n := range []int{-21, 20, 1000} {
		if res := a.Renice("fixture", 2, n, false); res.OK {
			t.Errorf("nice %d was accepted", n)
		}
	}

	// Start something disposable and kill it, so the happy path is covered too.
	conn, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(testCtx(t), "sh", "-c", "nohup sleep 300 >/dev/null 2>&1 & echo $!"); err != nil {
		t.Fatalf("spawn victim: %v", err)
	}

	var victim int
	for _, p := range mustList(t, a) {
		if p.Command == "sleep" && p.Args == "sleep 300" {
			victim = p.PID
			break
		}
	}
	if victim == 0 {
		t.Fatal("could not find the process just started")
	}

	if exists, err := a.ProcessExists("fixture", victim); err != nil || !exists {
		t.Fatalf("ProcessExists(%d) = %v, %v; want true", victim, exists, err)
	}
	if res := a.KillProcess("fixture", victim, "TERM", false); !res.OK {
		t.Fatalf("TERM failed: %+v", res)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		exists, err := a.ProcessExists("fixture", victim)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("PID %d survived a TERM", victim)
}

func mustList(t *testing.T, a *App) []adapter.ProcessInfo {
	t.Helper()
	procs, err := a.ListProcesses("fixture", false)
	if err != nil {
		t.Fatalf("ListProcesses: %v", err)
	}
	return procs
}

// TestTerminal covers §4.6 end to end: open a PTY, run a command through it,
// see the output come back, resize, and close.
func TestTerminal(t *testing.T) {
	a := connectedApp(t)

	var mu sync.Mutex
	var out []byte
	exited := make(chan string, 1)

	// Intercept the terminal events the frontend would receive.
	base := a.emit
	a.emit = func(event string, payload any) {
		switch {
		case strings.HasPrefix(event, "term:data:"):
			if s, ok := payload.(string); ok {
				chunk, err := base64.StdEncoding.DecodeString(s)
				if err != nil {
					t.Errorf("terminal payload was not base64: %v", err)
					return
				}
				mu.Lock()
				out = append(out, chunk...)
				mu.Unlock()
			}
		case strings.HasPrefix(event, "term:exit:"):
			msg, _ := payload.(string)
			select {
			case exited <- msg:
			default:
			}
		default:
			base(event, payload)
		}
	}

	info, err := a.OpenTerminal("fixture", TerminalOptions{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	if info.ID == "" {
		t.Fatal("no terminal id returned")
	}

	waitFor := func(want string, limit time.Duration) bool {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := string(out)
			mu.Unlock()
			if strings.Contains(got, want) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}

	// A shell prompt has to arrive before anything else means much.
	if !waitFor("$", 15*time.Second) {
		mu.Lock()
		t.Fatalf("no shell prompt; got %q", out)
		mu.Unlock()
	}

	// Round-trip a command through the PTY.
	send := func(s string) {
		if err := a.WriteTerminal(info.ID, base64.StdEncoding.EncodeToString([]byte(s))); err != nil {
			t.Fatalf("WriteTerminal: %v", err)
		}
	}
	send("echo litedeck-pty-marker\n")
	if !waitFor("litedeck-pty-marker", 15*time.Second) {
		mu.Lock()
		t.Errorf("command output never arrived; got %q", out)
		mu.Unlock()
	}

	// The remote side must know the window size, or full-screen programs draw
	// at 80x24 no matter how big the window is.
	if err := a.ResizeTerminal(info.ID, 120, 40); err != nil {
		t.Errorf("ResizeTerminal: %v", err)
	}
	mu.Lock()
	out = nil
	mu.Unlock()
	send("tput cols\n")
	if !waitFor("120", 15*time.Second) {
		mu.Lock()
		t.Errorf("resize did not reach the server; got %q", out)
		mu.Unlock()
	}

	if err := a.CloseTerminal(info.ID); err != nil {
		t.Errorf("CloseTerminal: %v", err)
	}
	// Writing to a closed terminal is an error, not a silent no-op.
	if err := a.WriteTerminal(info.ID, base64.StdEncoding.EncodeToString([]byte("x"))); err == nil {
		t.Error("writing to a closed terminal succeeded")
	}
	// Closing twice is fine — the reader goroutine and the UI both close.
	if err := a.CloseTerminal(info.ID); err != nil {
		t.Errorf("second CloseTerminal: %v", err)
	}
}

// TestTerminalBudget pins the two-pool channel split: terminals must not be
// able to starve command execution, which is what a single pool would allow.
func TestTerminalBudget(t *testing.T) {
	a := connectedApp(t)

	// Take an SFTP channel first, as the file explorer would.
	if _, err := a.mgr.SFTP("fixture"); err != nil {
		t.Fatalf("SFTP: %v", err)
	}

	var opened []string
	defer func() {
		for _, id := range opened {
			_ = a.CloseTerminal(id)
		}
	}()

	// Fill the long-lived budget. The exact count is an implementation detail;
	// what matters is that it is bounded and that exceeding it fails cleanly.
	for i := 0; i < sshcore.DefaultMaxLongLived+2; i++ {
		info, err := a.OpenTerminal("fixture", TerminalOptions{Cols: 80, Rows: 24})
		if err != nil {
			break // budget reached, which is the expected outcome
		}
		opened = append(opened, info.ID)
	}
	if len(opened) >= sshcore.DefaultMaxLongLived+2 {
		t.Errorf("opened %d terminals with no bound", len(opened))
	}

	// The point of the split: commands still work with the terminal budget full.
	if _, err := a.ListProcesses("fixture", false); err != nil {
		t.Errorf("commands broke while terminals held the long-lived budget: %v", err)
	}
	if _, err := a.ListServices("fixture"); err != nil {
		t.Errorf("service listing broke while terminals were open: %v", err)
	}
}

// TestHostMetrics covers §4.7: one round trip yields CPU, memory, disk and
// load, and CPU only becomes meaningful on the second sample.
func TestHostMetrics(t *testing.T) {
	a := connectedApp(t)

	first, err := a.HostMetrics("fixture")
	if err != nil {
		t.Fatalf("HostMetrics: %v", err)
	}

	// /proc/stat holds totals since boot, so one reading cannot yield a rate.
	// Reporting 0 here would draw a false trough on the sparkline.
	if first.CPU != -1 {
		t.Errorf("CPU on the first sample = %v, want -1", first.CPU)
	}
	if first.MemTotal <= 0 || first.MemUsed <= 0 {
		t.Errorf("memory not read: %+v", first.Metrics)
	}
	if first.MemPercent <= 0 || first.MemPercent > 100 {
		t.Errorf("MemPercent = %v", first.MemPercent)
	}
	if first.UptimeSeconds <= 0 {
		t.Errorf("uptime = %d", first.UptimeSeconds)
	}
	if len(first.Disks) == 0 {
		t.Error("no filesystems survived the filter; the real disk is gone too")
	}
	for _, d := range first.Disks {
		if d.Size <= 0 || d.Percent < 0 || d.Percent > 100 {
			t.Errorf("implausible filesystem: %+v", d)
		}
	}

	// Second sample: now there is a delta to divide.
	time.Sleep(1200 * time.Millisecond)
	second, err := a.HostMetrics("fixture")
	if err != nil {
		t.Fatalf("second HostMetrics: %v", err)
	}
	if second.CPU < 0 || second.CPU > 100 {
		t.Errorf("CPU on the second sample = %v, want 0..100", second.CPU)
	}
	if second.CPUTimes.Total <= first.CPUTimes.Total {
		t.Errorf("CPU counters did not advance: %d then %d",
			first.CPUTimes.Total, second.CPUTimes.Total)
	}

	// Disconnecting must drop the baseline: /proc/stat resets on reboot, and a
	// stale sample would draw a wild first reading on reconnect.
	if err := a.DisconnectHost("fixture"); err != nil {
		t.Fatal(err)
	}
	if got := a.cpu.previous("fixture"); got.Total != 0 {
		t.Errorf("CPU baseline survived a disconnect: %+v", got)
	}
}

// TestFollowServiceLog covers the live journal follow (§4.3): lines arrive as
// they are written, not on a poll.
func TestFollowServiceLog(t *testing.T) {
	a := connectedApp(t)

	var mu sync.Mutex
	var lines []string
	ended := make(chan string, 1)

	base := a.emit
	a.emit = func(event string, payload any) {
		switch {
		case strings.HasPrefix(event, "log:data:"):
			if l, ok := payload.(LogLine); ok {
				mu.Lock()
				lines = append(lines, l.Text)
				mu.Unlock()
			}
		case strings.HasPrefix(event, "log:exit:"):
			msg, _ := payload.(string)
			select {
			case ended <- msg:
			default:
			}
		default:
			base(event, payload)
		}
	}

	// The fixture user is not in systemd-journal, which is the common real-world
	// case: journalctl does not fail, it just shows nothing from the system.
	// The app has to say so rather than present an empty panel.
	if _, err := a.FollowServiceLog("fixture", "ssh.service", 100, false); err == nil {
		t.Error("an unprivileged follow was allowed on a host where it shows nothing")
	} else if !errors.Is(err, ErrJournalUnreadable) {
		t.Errorf("err = %v, want ErrJournalUnreadable", err)
	}

	// A unit of our own, so a line written after the follow starts is
	// unambiguously attributable. `systemd-cat -t ssh` was tried first and does
	// not work: it sets the syslog identifier, not _SYSTEMD_UNIT, so the message
	// never matches `journalctl -u ssh.service` — and the test passed anyway
	// because systemd-cat was absent and the live half silently never ran.
	conn0, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}
	const marker = "litedeck-follow-marker"
	setup := `cat > /etc/systemd/system/it-log.service <<'EOF'
[Unit]
Description=Integration log emitter
[Service]
Type=oneshot
ExecStart=/bin/echo ` + marker + `
EOF
systemctl daemon-reload`
	if res, err := a.execMaybeElevated(testCtx(t), conn0, "fixture", true, "sh", "-c", setup); err != nil || !res.OK() {
		t.Fatalf("arrange log unit: %v %v", err, res)
	}

	stream, err := a.FollowServiceLog("fixture", "it-log.service", 100, true)
	if err != nil {
		t.Fatalf("elevated FollowServiceLog: %v", err)
	}
	if stream.ID == "" || stream.Title != "it-log.service" {
		t.Errorf("stream = %+v", stream)
	}

	waitFor := func(want string, limit time.Duration) bool {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := strings.Join(lines, "\n")
			mu.Unlock()
			if strings.Contains(got, want) {
				return true
			}
			time.Sleep(150 * time.Millisecond)
		}
		return false
	}

	// The follow has to be running before the unit is started, otherwise a
	// backlog read would satisfy the assertion and prove nothing about
	// following.
	time.Sleep(1500 * time.Millisecond)

	if res, err := a.execMaybeElevated(testCtx(t), conn0, "fixture", true,
		"systemctl", "start", "--", "it-log.service"); err != nil || !res.OK() {
		t.Fatalf("start log unit: %v %v", err, res)
	}

	// A line written *after* the follow started must arrive with no poll.
	if !waitFor(marker, 25*time.Second) {
		mu.Lock()
		t.Errorf("a line written after the follow started never arrived (%d lines): %q",
			len(lines), lines)
		mu.Unlock()
	}

	// The "you are not seeing messages from other users" notice journalctl
	// prints into the output stream must never reach the panel (-q).
	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()
	if strings.Contains(joined, "not seeing messages") {
		t.Error("journalctl's hint leaked into the log stream; -q is missing")
	}

	if err := a.StopLogStream(stream.ID); err != nil {
		t.Errorf("StopLogStream: %v", err)
	}
	// Stopping twice is fine: the reader goroutine and the UI both close.
	if err := a.StopLogStream(stream.ID); err != nil {
		t.Errorf("second StopLogStream: %v", err)
	}
}

// TestLogStreamBudget: follows draw from the same long-lived pool as terminals,
// and exhausting it must not break command execution.
func TestLogStreamBudget(t *testing.T) {
	a := connectedApp(t)

	var opened []string
	defer func() {
		for _, id := range opened {
			_ = a.StopLogStream(id)
		}
	}()

	for i := 0; i < sshcore.DefaultMaxLongLived+2; i++ {
		s, err := a.FollowServiceLog("fixture", "ssh.service", 10, true)
		if err != nil {
			break // budget reached, as expected
		}
		opened = append(opened, s.ID)
	}
	if len(opened) >= sshcore.DefaultMaxLongLived+2 {
		t.Errorf("opened %d follows with no bound", len(opened))
	}
	if _, err := a.ListProcesses("fixture", false); err != nil {
		t.Errorf("commands broke while follows held the long-lived budget: %v", err)
	}
}

// TestListTimers covers the scheduled-jobs view (v1.x). The fixture installs
// two timers, one of which has fired and one of which has not.
func TestListTimers(t *testing.T) {
	a := connectedApp(t)
	conn, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)

	// Arrange via sudo: writing unit files needs root, and the elevated path is
	// already covered elsewhere.
	setup := `cat > /etc/systemd/system/it-often.service <<'EOF'
[Unit]
Description=Integration frequent job
[Service]
Type=oneshot
ExecStart=/bin/true
EOF
cat > /etc/systemd/system/it-often.timer <<'EOF'
[Timer]
OnBootSec=2s
OnUnitActiveSec=20s
[Install]
WantedBy=timers.target
EOF
cat > /etc/systemd/system/it-later.service <<'EOF'
[Unit]
Description=Integration nightly job
[Service]
Type=oneshot
ExecStart=/bin/true
EOF
cat > /etc/systemd/system/it-later.timer <<'EOF'
[Timer]
OnCalendar=daily
[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now it-often.timer it-later.timer`

	res, err := a.execMaybeElevated(ctx, conn, "fixture", true, "sh", "-c", setup)
	if err != nil {
		t.Fatalf("arrange timers: %v", err)
	}
	if !res.OK() {
		t.Fatalf("arrange timers: %s", res.Stderr)
	}
	time.Sleep(4 * time.Second) // let the frequent one fire at least once

	timers, err := a.ListTimers("fixture")
	if err != nil {
		t.Fatalf("ListTimers: %v", err)
	}
	byUnit := map[string]adapter.Timer{}
	for _, x := range timers {
		byUnit[x.Unit] = x
	}

	often, ok := byUnit["it-often.timer"]
	if !ok {
		t.Fatalf("it-often.timer missing from %d timers", len(timers))
	}
	if often.Activates != "it-often.service" {
		t.Errorf("activates = %q", often.Activates)
	}
	// systemd reports microseconds; a value read as seconds would be tens of
	// thousands of years out.
	if often.Next < 1_700_000_000 || often.Next > 2_000_000_000 {
		t.Errorf("Next = %d — not a plausible unix second", often.Next)
	}
	if often.NeverRun() {
		t.Errorf("a timer that has fired reports NeverRun: %+v", often)
	}
	// The description is joined in from the service listing, because
	// list-timers does not carry one and a bare unit name says little.
	if often.Description != "Integration frequent job" {
		t.Errorf("description not joined: %q", often.Description)
	}

	later, ok := byUnit["it-later.timer"]
	if !ok {
		t.Fatal("it-later.timer missing")
	}
	if !later.NeverRun() {
		t.Errorf("a daily timer installed seconds ago reports having run: %+v", later)
	}
	if later.Next <= 0 {
		t.Errorf("scheduled timer has no next run: %+v", later)
	}
}
