// Package adapter abstracts over server operating systems (§3.3).
//
// Detection runs once per connection and decides which adapter handles a host
// and which of its features are available. A capability the server lacks is not
// an error: the UI disables that tab instead.
package adapter

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// Runner is the subset of a connection that detection needs.
//
// Probe exists alongside Exec so a "does this binary exist?" check is not
// reported as a failed command. Nearly every detection step is a probe.
type Runner interface {
	Exec(ctx context.Context, cmd string, args ...string) (*sshcore.Result, error)
	Probe(ctx context.Context, cmd string, args ...string) (*sshcore.Result, error)
}

// Platform is the kind of host on the other end.
//
// This is checked before anything else because every other probe, and every
// adapter, assumes a POSIX shell. Against Windows OpenSSH — where the default
// shell is cmd.exe — `ps` and `sh -c` do not merely return nothing, they return
// a localised "not recognised as a command" in the console codepage. Without
// this gate the app treated such a host as connected and healthy and retried
// forever, filling the Command Log with mojibake and never saying why.
type Platform string

const (
	PlatformLinux   Platform = "linux"
	PlatformWindows Platform = "windows"
	PlatformDarwin  Platform = "darwin"
	PlatformBSD     Platform = "bsd"
	// PlatformUnknown is a host that answered the SSH handshake but neither
	// `uname -s` nor `cmd /c ver`. Reported as-is rather than guessed at.
	PlatformUnknown Platform = "unknown"
)

// Supported reports whether an adapter exists for this platform. Only Linux
// does today; the rest are identified so the UI can name what it found.
func (p Platform) Supported() bool { return p == PlatformLinux }

// ServerInfo is what one probe of a server yields.
type ServerInfo struct {
	// Platform decides whether any of the rest is meaningful. When it is not
	// PlatformLinux every other field is zero and no adapter ran.
	Platform Platform `json:"platform"`
	// Kernel is the raw `uname -s` output, or the `ver` line on Windows. Kept
	// verbatim for bug reports: "unknown" plus the actual string is far more
	// useful than "unknown" alone.
	Kernel string `json:"kernel,omitempty"`

	// Distribution, from /etc/os-release.
	PrettyName string `json:"prettyName"`
	ID         string `json:"id"`      // ubuntu, debian, rocky, alpine…
	VersionID  string `json:"version"` // 22.04

	// Init system. SystemdVersion is 0 when systemd is absent, and decides the
	// service listing format — below 246 there is no JSON output, and asking
	// for it fails silently (§3.4).
	HasSystemd     bool `json:"hasSystemd"`
	SystemdVersion int  `json:"systemdVersion"`
	SystemdJSON    bool `json:"systemdJson"`

	// Container runtimes.
	HasDocker bool `json:"hasDocker"`
	HasPodman bool `json:"hasPodman"`

	// Privilege escalation (§7.2).
	HasSudo      bool `json:"hasSudo"`
	SudoNoPasswd bool `json:"sudoNoPasswd"`

	// IsRoot and CanReadJournal decide whether the service log panel can show
	// anything. A user outside systemd-journal/adm sees only their *own*
	// messages, with no error — `journalctl -u nginx` simply comes back empty,
	// which reads as "this service never logged anything".
	IsRoot         bool     `json:"isRoot"`
	Groups         []string `json:"groups,omitempty"`
	CanReadJournal bool     `json:"canReadJournal"`

	// Errors that did not stop detection, for the bug report template (§11).
	Warnings []string `json:"warnings,omitempty"`
}

// Capability names a feature the UI can enable or grey out.
type Capability string

const (
	CapServices   Capability = "services"
	CapProcesses  Capability = "processes"
	CapContainers Capability = "containers"
	CapMetrics    Capability = "metrics"
	// CapNetwork needs iproute2 (`ip -j addr`, `ss -tulnp`). It was missing, so
	// the network tab was the one view with no gate at all and kept polling a
	// host that could not answer.
	CapNetwork Capability = "network"
)

// Capabilities reports which tabs this server supports (§3.3).
func (i ServerInfo) Capabilities() map[Capability]bool {
	// Nothing at all on a platform with no adapter. ps and /proc were listed
	// here as unconditionally available — "ps is on every POSIX host" — which is
	// true and was still wrong, because whether the host is POSIX was never
	// checked. On Windows that assumption became a poll loop that could not
	// succeed and never explained itself.
	if !i.Platform.Supported() {
		return map[Capability]bool{
			CapServices:   false,
			CapProcesses:  false,
			CapContainers: false,
			CapMetrics:    false,
			CapNetwork:    false,
		}
	}
	return map[Capability]bool{
		CapServices:   i.HasSystemd,
		CapProcesses:  true, // ps is on every POSIX host
		CapContainers: i.HasDocker || i.HasPodman,
		CapMetrics:    true, // /proc, df
		CapNetwork:    true, // iproute2; the tab degrades per-command if partial
	}
}

// Detect probes a freshly connected server.
//
// Every probe is independent and failure-tolerant: a server that hides
// /etc/os-release or has no sudo is still perfectly usable, so a failed probe
// records a warning and leaves the field zero rather than aborting.
func Detect(ctx context.Context, r Runner) (ServerInfo, error) {
	var info ServerInfo

	// Platform first. Everything below assumes a POSIX shell, and running those
	// probes against cmd.exe produces a dozen console-codepage error strings
	// that say nothing a user can act on.
	info.Platform, info.Kernel = detectPlatform(ctx, r)
	if !info.Platform.Supported() {
		// Return successfully with an unsupported platform rather than an error:
		// the connection is fine and the host list should show it as connected.
		// It is the feature surface that is empty, and the UI says so.
		return info, nil
	}

	if res, err := r.Exec(ctx, "cat", "/etc/os-release"); err != nil {
		info.Warnings = append(info.Warnings, "read /etc/os-release: "+err.Error())
	} else if res.OK() {
		info.PrettyName = osReleaseField(string(res.Stdout), "PRETTY_NAME")
		info.ID = osReleaseField(string(res.Stdout), "ID")
		info.VersionID = osReleaseField(string(res.Stdout), "VERSION_ID")
	}

	if res, err := r.Probe(ctx, "systemctl", "--version"); err == nil && res.OK() {
		line, _, _ := strings.Cut(string(res.Stdout), "\n")
		if v, err := linuxsystemd.ParseSystemdVersion(line); err == nil {
			info.HasSystemd = true
			info.SystemdVersion = v
			info.SystemdJSON = linuxsystemd.SupportsJSONOutput(v)
		} else {
			info.Warnings = append(info.Warnings, "parse systemd version: "+err.Error())
		}
	}

	if res, err := r.Probe(ctx, "id", "-u"); err == nil && res.OK() {
		info.IsRoot = strings.TrimSpace(string(res.Stdout)) == "0"
	}
	if res, err := r.Probe(ctx, "id", "-nG"); err == nil && res.OK() {
		info.Groups = strings.Fields(string(res.Stdout))
	}
	// systemd-journal is the canonical group; adm and wheel are granted the
	// same read access by most distributions.
	info.CanReadJournal = info.IsRoot
	for _, g := range info.Groups {
		if g == "systemd-journal" || g == "adm" || g == "wheel" {
			info.CanReadJournal = true
		}
	}

	info.HasDocker = commandExists(ctx, r, "docker")
	info.HasPodman = commandExists(ctx, r, "podman")
	info.HasSudo = commandExists(ctx, r, "sudo")

	if info.HasSudo {
		// -n never prompts; a zero exit means sudo is already authorised, so
		// the UI can skip the password dialog entirely (§7.2).
		if res, err := r.Probe(ctx, "sudo", "-n", "true"); err == nil {
			info.SudoNoPasswd = res.OK()
		}
	}
	return info, nil
}

// detectPlatform identifies the host in at most two round trips.
//
// `uname -s` answers on every POSIX system. When it fails the host is either
// Windows or something unrecognised, and `cmd /c ver` distinguishes them — it
// works whether the configured SSH shell is cmd.exe or PowerShell, whereas the
// bare `ver` builtin only works under cmd.exe.
//
// Both are probes, not actions: "uname is missing" is a normal answer here, not
// a failure worth showing the user as a red error.
func detectPlatform(ctx context.Context, r Runner) (Platform, string) {
	if res, err := r.Probe(ctx, "uname", "-s"); err == nil && res.OK() {
		kernel := strings.TrimSpace(string(res.Stdout))
		switch {
		case strings.EqualFold(kernel, "linux"):
			return PlatformLinux, kernel
		case strings.EqualFold(kernel, "darwin"):
			return PlatformDarwin, kernel
		case strings.Contains(strings.ToUpper(kernel), "BSD"):
			return PlatformBSD, kernel
		case kernel != "":
			return PlatformUnknown, kernel
		}
	}

	if res, err := r.Probe(ctx, "cmd", "/c", "ver"); err == nil && res.OK() {
		line := strings.TrimSpace(string(res.Stdout))
		if strings.Contains(line, "Windows") {
			return PlatformWindows, line
		}
		// Windows console output arrives in the machine's OEM codepage (949 on a
		// Korean install), so a non-UTF-8 answer here is itself evidence of
		// Windows rather than a reason to give up.
		if !utf8.ValidString(line) {
			return PlatformWindows, "Windows (출력 인코딩이 UTF-8이 아님)"
		}
	}
	return PlatformUnknown, ""
}

func commandExists(ctx context.Context, r Runner, name string) bool {
	res, err := r.Probe(ctx, "command", "-v", name)
	return err == nil && res.OK()
}

// osReleaseField reads one KEY=value line from /etc/os-release.
func osReleaseField(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k == key {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}
