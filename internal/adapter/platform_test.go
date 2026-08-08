package adapter

import (
	"context"
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// fakeRunner answers a fixed script of commands and records what was asked.
//
// Keyed on the whole argv joined by spaces so a test can distinguish
// `uname -s` from `cmd /c ver` — the two probes the platform gate depends on.
type fakeRunner struct {
	replies map[string]sshcore.Result
	asked   []string
}

func (f *fakeRunner) run(cmd string, args ...string) (*sshcore.Result, error) {
	key := strings.Join(append([]string{cmd}, args...), " ")
	f.asked = append(f.asked, key)
	if r, ok := f.replies[key]; ok {
		return &r, nil
	}
	// Unlisted commands behave like a shell that cannot find them, which is what
	// cmd.exe does to every POSIX tool.
	return &sshcore.Result{ExitCode: 9009, Stderr: []byte("not recognized")}, nil
}

func (f *fakeRunner) Exec(_ context.Context, cmd string, args ...string) (*sshcore.Result, error) {
	return f.run(cmd, args...)
}
func (f *fakeRunner) Probe(_ context.Context, cmd string, args ...string) (*sshcore.Result, error) {
	return f.run(cmd, args...)
}

// TestDetectWindowsServer covers the failure a user actually hit: a Windows
// mini PC running OpenSSH with cmd.exe as the login shell.
//
// Before the platform gate existed, Detect ran every POSIX probe against that
// host, returned a near-empty ServerInfo with a nil error, and the app treated
// it as a healthy Linux box — polling `ps` and `sh -c` every couple of seconds
// forever and filling the Command Log with console-codepage error text. The
// connection was fine, so nothing surfaced as a connection failure either.
func TestDetectWindowsServer(t *testing.T) {
	r := &fakeRunner{replies: map[string]sshcore.Result{
		// cmd.exe reports 9009 for an unknown command, and the message arrives in
		// the machine's OEM codepage rather than UTF-8.
		"uname -s": {ExitCode: 9009, Stderr: []byte("'uname'\xc0\xba(\xb4\xc2) \xb3\xbb\xba\xce")},
		"cmd /c ver": {ExitCode: 0, Stdout: []byte(
			"\r\nMicrosoft Windows [Version 10.0.26100.4061]\r\n")},
	}}

	info, err := Detect(context.Background(), r)
	if err != nil {
		t.Fatalf("Detect returned an error for a reachable host: %v", err)
	}
	if info.Platform != PlatformWindows {
		t.Errorf("platform = %q, want %q", info.Platform, PlatformWindows)
	}
	if !strings.Contains(info.Kernel, "Windows") {
		t.Errorf("kernel = %q, want it to name Windows for the bug report", info.Kernel)
	}
	if info.Platform.Supported() {
		t.Error("Windows reported as supported; no adapter can drive it")
	}

	// Every capability off. This is the assertion that stops the poll loop: the
	// UI and the Go guards both read it, and `ps` used to be hardcoded true on
	// the grounds that "ps is on every POSIX host" — true, but nothing checked
	// that the host was POSIX.
	for cap, on := range info.Capabilities() {
		if on {
			t.Errorf("capability %q enabled on a Windows host", cap)
		}
	}

	// Detection must not have gone on to run the Linux probes; each one would be
	// another unusable command in the log.
	for _, asked := range r.asked {
		if asked == "cat /etc/os-release" || strings.HasPrefix(asked, "systemctl") {
			t.Errorf("ran POSIX probe %q after identifying Windows", asked)
		}
	}
}

// TestDetectPlatformNames checks the branches that are not Linux or Windows.
// They are equally unsupported, but naming them beats reporting "unknown" —
// someone pointing LiteDeck at a Mac deserves to be told it is a Mac.
func TestDetectPlatformNames(t *testing.T) {
	for _, tc := range []struct {
		uname string
		want  Platform
	}{
		{"Linux", PlatformLinux},
		{"linux", PlatformLinux}, // uname casing is not guaranteed
		{"Darwin", PlatformDarwin},
		{"FreeBSD", PlatformBSD},
		{"OpenBSD", PlatformBSD},
		{"Plan9", PlatformUnknown},
	} {
		r := &fakeRunner{replies: map[string]sshcore.Result{
			"uname -s": {ExitCode: 0, Stdout: []byte(tc.uname + "\n")},
		}}
		got, kernel := detectPlatform(context.Background(), r)
		if got != tc.want {
			t.Errorf("uname -s = %q: platform = %q, want %q", tc.uname, got, tc.want)
		}
		if kernel != tc.uname {
			t.Errorf("uname -s = %q: kernel recorded as %q", tc.uname, kernel)
		}
	}
}

// TestDetectSilentHost is the case where neither probe answers — a host that
// completed the SSH handshake but runs something unrecognised, or a forced
// command that swallows everything.
func TestDetectSilentHost(t *testing.T) {
	r := &fakeRunner{replies: map[string]sshcore.Result{}}
	info, err := Detect(context.Background(), r)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Platform != PlatformUnknown {
		t.Errorf("platform = %q, want %q", info.Platform, PlatformUnknown)
	}
	if info.Platform.Supported() {
		t.Error("unknown platform reported as supported")
	}
}

// TestDetectLinuxStillWorks guards against the gate breaking the supported
// path — the probes below it have to still run.
func TestDetectLinuxStillWorks(t *testing.T) {
	r := &fakeRunner{replies: map[string]sshcore.Result{
		"uname -s": {ExitCode: 0, Stdout: []byte("Linux\n")},
		"cat /etc/os-release": {ExitCode: 0, Stdout: []byte(
			"PRETTY_NAME=\"Ubuntu 22.04.5 LTS\"\nID=ubuntu\nVERSION_ID=\"22.04\"\n")},
		"systemctl --version": {ExitCode: 0, Stdout: []byte("systemd 249 (249.11-0ubuntu3.12)\n")},
		"id -u":               {ExitCode: 0, Stdout: []byte("1000\n")},
		"id -nG":              {ExitCode: 0, Stdout: []byte("litedeck adm sudo\n")},
		"command -v docker":   {ExitCode: 0, Stdout: []byte("/usr/bin/docker\n")},
	}}

	info, err := Detect(context.Background(), r)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Platform != PlatformLinux {
		t.Fatalf("platform = %q, want linux", info.Platform)
	}
	if info.PrettyName != "Ubuntu 22.04.5 LTS" || info.SystemdVersion != 249 || !info.SystemdJSON {
		t.Errorf("linux probes did not run: %+v", info)
	}
	if !info.HasDocker || info.IsRoot || !info.CanReadJournal {
		t.Errorf("capability probes wrong: docker=%v root=%v journal=%v",
			info.HasDocker, info.IsRoot, info.CanReadJournal)
	}
	caps := info.Capabilities()
	for _, c := range []Capability{CapServices, CapProcesses, CapContainers, CapMetrics} {
		if !caps[c] {
			t.Errorf("capability %q off on a healthy Linux host", c)
		}
	}
	// Windows is never asked about once uname answers.
	for _, asked := range r.asked {
		if strings.HasPrefix(asked, "cmd ") {
			t.Errorf("probed %q on a host that answered uname", asked)
		}
	}
}
