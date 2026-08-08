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
	// prefixReplies matches on the start of the joined argv, for commands whose
	// tail is not predictable from here — the PowerShell probe ends in a base64
	// payload built from a script this package keeps private.
	prefixReplies map[string]sshcore.Result
	asked         []string
}

func (f *fakeRunner) run(cmd string, args ...string) (*sshcore.Result, error) {
	key := strings.Join(append([]string{cmd}, args...), " ")
	f.asked = append(f.asked, key)
	if r, ok := f.replies[key]; ok {
		return &r, nil
	}
	for prefix, r := range f.prefixReplies {
		if strings.HasPrefix(key, prefix) {
			return &r, nil
		}
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
	// Captured from a real Windows 10 Pro box (Korean install) over OpenSSH with
	// the default cmd.exe shell, not written from imagination — CONTRIBUTING rule
	// 4. Both stderr strings are the actual CP949 bytes that arrived.
	//
	// The second one is the finding: sshd wraps the remote command as
	// `cmd.exe /c "<command>"`, so a nested `cmd /c ver` has its quotes
	// redistributed and the inner cmd is handed the command name `ver"` — with the
	// trailing quote. That is why detection uses PowerShell -EncodedCommand
	// instead, whose payload is a single quote-free base64 token.
	const cp949NotRecognised = "\xc0\xba(\xb4\xc2) \xb3\xbb\xba\xce \xb6\xc7\xb4\xc2 \xbf\xdc\xba\xce \xb8\xed\xb7\xc9"

	r := &fakeRunner{
		replies: map[string]sshcore.Result{
			"uname -s": {ExitCode: 1, Stderr: []byte("'uname'" + cp949NotRecognised)},
			// Kept in the fixture even though nothing should ask for it, so the
			// assertion below proves the broken route is really gone rather than
			// merely untested.
			"cmd /c ver": {ExitCode: 1, Stderr: []byte(`'ver"'` + cp949NotRecognised)},
		},
		prefixReplies: map[string]sshcore.Result{
			"powershell -NoProfile -NonInteractive -EncodedCommand ": {
				ExitCode: 0,
				Stdout:   []byte("Microsoft Windows 10 Pro\t10.0.19045\t64비트\r\n"),
			},
		},
	}

	info, err := Detect(context.Background(), r)
	if err != nil {
		t.Fatalf("Detect returned an error for a reachable host: %v", err)
	}
	if info.Platform != PlatformWindows {
		t.Errorf("platform = %q, want %q", info.Platform, PlatformWindows)
	}
	if info.Platform.Supported() {
		t.Error("Windows reported as supported; no adapter can drive it")
	}

	// The edition, in PrettyName, where the header renders it — the same field the
	// Linux path fills with "Ubuntu 22.04.5 LTS". The vendor prefix is dropped and
	// the architecture folded in, because "Windows 10 Pro (64비트)" is what someone
	// would call this machine.
	if info.PrettyName != "Windows 10 Pro (64비트)" {
		t.Errorf("prettyName = %q, want %q", info.PrettyName, "Windows 10 Pro (64비트)")
	}
	if info.ID != "windows" {
		t.Errorf("id = %q, want windows", info.ID)
	}
	// Kernel must not carry prose. It briefly held "Windows (stderr가 UTF-8이
	// 아님)" — the reason we guessed — which the UI printed next to the real name.
	if info.Kernel != "" {
		t.Errorf("kernel = %q, want empty once PrettyName is known", info.Kernel)
	}

	// The nested-cmd route is broken through this transport and must not be used.
	for _, asked := range r.asked {
		if strings.HasPrefix(asked, "cmd /c") {
			t.Errorf("used %q — nested cmd /c loses its quotes through Windows OpenSSH", asked)
		}
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

		// POSIX emulation layers on Windows. uname answers on these, so the
		// Windows branch is never reached and the host was reported as an
		// unknown OS while its own kernel string said NT. This is what a box
		// with Git for Windows or MSYS2 on PATH actually replies.
		{"MINGW64_NT-10.0-26100", PlatformWindows},
		{"MSYS_NT-10.0-19045", PlatformWindows},
		{"CYGWIN_NT-10.0", PlatformWindows},
		{"Windows_NT", PlatformWindows},
	} {
		r := &fakeRunner{replies: map[string]sshcore.Result{
			"uname -s": {ExitCode: 0, Stdout: []byte(tc.uname + "\n")},
		}}
		got, kernel, _ := detectPlatform(context.Background(), r)
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
