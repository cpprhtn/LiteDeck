package app

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The container view against a real Docker daemon (§4.5).
//
// Docker-in-Docker rather than the host's socket: this test creates and
// destroys containers, and must not be able to touch anything the developer
// running it cares about.

const dockerImage = "litedeck-test-docker"

var (
	dockerAddr string
	dockerSkip string
	dindID     string
)

func startDocker() (func(), error) {
	if testing.Short() {
		return nil, errors.New("skipped by -short")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("docker not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker not running: %s", firstLine(out))
	}

	if out, err := exec.Command("docker", "build", "-q", "-t", dockerImage,
		"../../testdata/docker").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker build: %s", firstLine(out))
	}

	out, err := exec.Command("docker", "run", "-d", "--privileged", "-P", dockerImage).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker run: %s", firstLine(out))
	}
	dindID = strings.TrimSpace(string(out))
	stop := func() { _ = exec.Command("docker", "rm", "-f", dindID).Run() }

	if dockerAddr, err = waitPort(dindID); err != nil {
		stop()
		return nil, err
	}

	// The inner daemon takes a moment; connecting before it is up would give a
	// flaky "cannot connect to the Docker daemon".
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("docker", "exec", dindID, "docker", "info").Run(); err == nil {
			return stop, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	stop()
	return nil, errors.New("inner dockerd never became ready")
}

// dockerApp connects to the dind fixture.
func dockerApp(t *testing.T) *App {
	t.Helper()
	if dockerSkip != "" {
		t.Skipf("docker fixture unavailable: %s", dockerSkip)
	}

	dir := t.TempDir()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
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
		ID: "dind", Name: "docker fixture", Hostname: "127.0.0.1",
		User: sysUser, Auth: []config.AuthMethod{config.AuthPassword},
	}
	_, portStr, _ := strings.Cut(dockerAddr, ":")
	fmt.Sscanf(portStr, "%d", &host.Port)
	if err := store.Upsert(host); err != nil {
		t.Fatal(err)
	}

	stop := autoAnswerFor(t, a, rec, "dind")
	t.Cleanup(stop)
	if err := a.ConnectHost("dind"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	return a
}

// autoAnswerFor is autoAnswer with the fixture's password; the host ID differs
// but the credentials are the same across fixtures.
func autoAnswerFor(t *testing.T, a *App, rec *recorder, _ string) func() {
	return autoAnswer(t, a, rec, "always", true)
}

// runInDind executes a docker command inside the fixture, for arranging state
// the test then observes through LiteDeck.
func runInDind(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"exec", dindID, "docker"}, args...)
	if out, err := exec.Command("docker", full...).CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
}

func findContainer(cs []adapter.Container, name string) (adapter.Container, bool) {
	for _, c := range cs {
		if c.Name == name {
			return c, true
		}
	}
	return adapter.Container{}, false
}

func waitContainerState(t *testing.T, a *App, name, want string) adapter.Container {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		cs, err := a.ListContainers("dind")
		if err != nil {
			t.Fatalf("ListContainers: %v", err)
		}
		if c, ok := findContainer(cs, name); ok {
			if c.State == want {
				return c
			}
			last = c.State
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s never reached state %q (last %q)", name, want, last)
	return adapter.Container{}
}

func TestContainerView(t *testing.T) {
	a := dockerApp(t)

	// Detection must light up the tab.
	info, err := a.DetectHost("dind")
	if err != nil {
		t.Fatalf("DetectHost: %v", err)
	}
	if !info.HasDocker {
		t.Fatalf("docker not detected: %+v", info.ServerInfo)
	}
	if !info.Capabilities["containers"] {
		t.Error("containers capability not reported despite docker")
	}

	// An empty daemon must list cleanly rather than erroring.
	cs, err := a.ListContainers("dind")
	if err != nil {
		t.Fatalf("ListContainers on an empty daemon: %v", err)
	}
	if len(cs) != 0 {
		t.Logf("fixture already had %d containers", len(cs))
	}

	// Arrange: one running, one that exited non-zero.
	runInDind(t, "pull", "-q", "alpine:3.20")
	runInDind(t, "run", "-d", "--name", "litedeck-live", "-p", "18099:80",
		"alpine:3.20", "sleep", "600")
	runInDind(t, "run", "-d", "--name", "litedeck-crashed",
		"alpine:3.20", "sh", "-c", "exit 7")

	live := waitContainerState(t, a, "litedeck-live", "running")
	if live.Image != "alpine:3.20" {
		t.Errorf("image = %q", live.Image)
	}
	if !strings.Contains(live.Command, "sleep") {
		t.Errorf("command = %q", live.Command)
	}
	if len(live.Ports) != 1 || live.Ports[0].HostPort != "18099" || live.Ports[0].Container != "80" {
		t.Errorf("ports = %+v", live.Ports)
	}
	if live.ExitCode != -1 {
		t.Errorf("running container reported exit code %d", live.ExitCode)
	}

	// The exit code is the first thing anyone wants when a container is down.
	crashed := waitContainerState(t, a, "litedeck-crashed", "exited")
	if crashed.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7 (status %q)", crashed.ExitCode, crashed.Status)
	}

	// Lifecycle: stop, start, restart.
	if res := a.ContainerAction("dind", live.ID, "stop", false); !res.OK {
		t.Fatalf("stop: %+v", res)
	}
	waitContainerState(t, a, "litedeck-live", "exited")

	if res := a.ContainerAction("dind", live.ID, "start", false); !res.OK {
		t.Fatalf("start: %+v", res)
	}
	waitContainerState(t, a, "litedeck-live", "running")

	if res := a.ContainerAction("dind", live.ID, "restart", false); !res.OK {
		t.Fatalf("restart: %+v", res)
	}
	waitContainerState(t, a, "litedeck-live", "running")

	// Verbs outside the allowlist must not reach the daemon. `exec` and `run`
	// would turn a button into arbitrary remote execution.
	for _, verb := range []string{"exec", "run", "cp", "rm", "kill", "--version"} {
		if res := a.ContainerAction("dind", live.ID, verb, false); res.OK {
			t.Errorf("container action %q was accepted", verb)
		}
	}

	// Logs. Container output usually goes to stderr, so both streams count.
	runInDind(t, "run", "-d", "--name", "litedeck-chatty",
		"alpine:3.20", "sh", "-c", "echo to-stdout; echo to-stderr >&2; sleep 60")
	waitContainerState(t, a, "litedeck-chatty", "running")

	logs, err := a.ContainerLogs("dind", "litedeck-chatty", 100)
	if err != nil {
		t.Fatalf("ContainerLogs: %v", err)
	}
	for _, want := range []string{"to-stdout", "to-stderr"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q; got %q", want, logs)
		}
	}

	// Removal: a running container needs force, and without it Docker refuses.
	if res := a.RemoveContainer("dind", live.ID, false, false); res.OK {
		t.Error("removing a running container succeeded without force")
	}
	if res := a.RemoveContainer("dind", live.ID, true, false); !res.OK {
		t.Fatalf("forced remove: %+v", res)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		cs, err := a.ListContainers("dind")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := findContainer(cs, "litedeck-live"); !ok {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Error("container still listed after removal")
}

// TestContainerViewAbsentRuntime: a host with no docker must report the absence
// clearly rather than surfacing a shell error, and the UI greys the tab out.
func TestContainerViewAbsentRuntime(t *testing.T) {
	a := connectedApp(t) // the systemd fixture, which has no docker

	info, err := a.DetectHost("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if info.HasDocker || info.HasPodman {
		t.Skip("the systemd fixture unexpectedly has a container runtime")
	}
	if info.Capabilities["containers"] {
		t.Error("containers capability reported without a runtime")
	}
	if _, err := a.ListContainers("fixture"); err == nil {
		t.Error("ListContainers succeeded on a host with no runtime")
	}
}

// TestImagesAndVolumes covers the v1.x storage view against a real daemon:
// what is big, and what is safe to delete.
func TestImagesAndVolumes(t *testing.T) {
	a := dockerApp(t)

	runInDind(t, "pull", "-q", "alpine:3.20")
	runInDind(t, "volume", "create", "litedeck-used")
	runInDind(t, "volume", "create", "litedeck-orphan")
	runInDind(t, "run", "-d", "--name", "litedeck-holder",
		"-v", "litedeck-used:/data", "alpine:3.20", "sleep", "300")

	imgs, err := a.ListImages("dind")
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	var alpine *adapter.Image
	for i := range imgs {
		if imgs[i].Repository == "alpine" {
			alpine = &imgs[i]
		}
	}
	if alpine == nil {
		t.Fatalf("alpine missing from %d images", len(imgs))
	}
	if alpine.SizeBytes <= 0 {
		t.Errorf("size not parsed: %+v", *alpine)
	}
	if alpine.Dangling {
		t.Errorf("a tagged image was marked dangling: %+v", *alpine)
	}
	// Largest first — the reason to open this list is disk space.
	for i := 1; i < len(imgs); i++ {
		if imgs[i-1].SizeBytes < imgs[i].SizeBytes {
			t.Errorf("images not sorted by size")
			break
		}
	}

	vols, err := a.ListVolumes("dind")
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	byName := map[string]adapter.Volume{}
	for _, v := range vols {
		byName[v.Name] = v
	}
	if !byName["litedeck-used"].InUse {
		t.Error("a mounted volume was reported unused")
	}
	if byName["litedeck-orphan"].InUse {
		t.Error("an unreferenced volume was reported in use")
	}

	// The daemon refuses to delete a volume in use, and LiteDeck offers no way
	// past that — the refusal *is* the guard.
	if res := a.RemoveVolume("dind", "litedeck-used", false); res.OK {
		t.Error("removing an in-use volume succeeded")
	}
	if res := a.RemoveVolume("dind", "litedeck-orphan", false); !res.OK {
		t.Errorf("removing an unused volume failed: %+v", res)
	}
	vols, _ = a.ListVolumes("dind")
	for _, v := range vols {
		if v.Name == "litedeck-orphan" {
			t.Error("volume still listed after removal")
		}
	}

	// An image a container still references cannot be removed either.
	if res := a.RemoveImage("dind", alpine.ID, false); res.OK {
		t.Error("removing an in-use image succeeded")
	}
}
