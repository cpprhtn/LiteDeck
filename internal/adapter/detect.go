// Package adapter abstracts over server operating systems (§3.3).
//
// Detection runs once per connection and decides which adapter handles a host
// and which of its features are available. A capability the server lacks is not
// an error: the UI disables that tab instead.
package adapter

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
	"github.com/cpprhtn/LiteDeck/internal/adapter/windowspowershell"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
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
	// PlatformUnknown is a host that answered the SSH handshake but identified
	// itself to none of the probes. Reported as-is rather than guessed at.
	PlatformUnknown Platform = "unknown"
)

// Supported reports whether an adapter exists for this platform. macOS and BSD
// are identified so the UI can name what it found rather than saying "unknown".
func (p Platform) Supported() bool {
	return p == PlatformLinux || p == PlatformWindows
}

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
	// HasCompose reports that `<runtime> compose` answers — the v2 plugin.
	// Probed here, once per host, rather than before each action.
	HasCompose bool `json:"hasCompose"`

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
	// CapSessions needs ps, which every POSIX host has. Windows has SSH logins
	// too, but sshd there does not produce the "sshd: user@pts/N" process the
	// parser reads, so it stays off until something reads Get-CimInstance
	// Win32_LogonSession instead.
	CapSessions Capability = "sessions"
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
			CapSessions:   false,
		}
	}
	if i.Platform == PlatformWindows {
		return map[Capability]bool{
			CapServices:  true, // Win32_Service
			CapProcesses: true, // Win32_Process + Get-Process
			CapMetrics:   true, // performance counters, Win32_OperatingSystem
			// Docker Desktop speaks the same CLI, so the existing container
			// parser applies unchanged when the binary is present.
			CapContainers: i.HasDocker,
			CapNetwork:    true, // Get-NetIPAddress, Get-NetAdapter, Get-Net{TCP,UDP}
			CapSessions:   false,
		}
	}
	return map[Capability]bool{
		CapServices:   i.HasSystemd,
		CapProcesses:  true, // ps is on every POSIX host
		CapContainers: i.HasDocker || i.HasPodman,
		CapMetrics:    true, // /proc, df
		CapNetwork:    true, // iproute2; the tab degrades per-command if partial
		CapSessions:   true, // ps; w/ss/loginctl only enrich
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
	var notes []string
	info.Platform, info.Kernel, notes = detectPlatform(ctx, r)

	if info.Platform == PlatformWindows {
		// Identification already produced the friendly name, so put it where the
		// Linux path puts its own: PrettyName is what the header renders, and
		// Kernel is for the raw self-description. Asking WMI a second time just
		// to fill a different field would be a round trip for nothing.
		if info.Kernel != "" {
			info.PrettyName = info.Kernel
			info.ID = "windows"
			info.Kernel = ""
		}
		detectWindows(ctx, r, &info)
		info.Warnings = append(info.Warnings, notes...)
		return info, nil
	}

	if !info.Platform.Supported() {
		// Keep the transcript for every unsupported host, not just the ones
		// detection gave up on. Restricting it to PlatformUnknown threw away the
		// evidence in exactly the case that most needs it: a host identified only
		// by a fallback heuristic, where something upstream clearly did not answer
		// the way it should have. It is behind a disclosure triangle, so the cost
		// of keeping it is nothing.
		info.Warnings = append(info.Warnings, notes...)
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

	// Compose v2 is a CLI plugin, not a binary, so `command -v` cannot see it —
	// the subcommand has to be asked directly. It answers from the client alone
	// and never contacts the daemon, which keeps this as cheap as the probes
	// above. Only one runtime is asked, the one that will be used.
	if runtime := "docker"; info.HasDocker || info.HasPodman {
		if !info.HasDocker {
			runtime = "podman"
		}
		if res, err := r.Probe(ctx, runtime, "compose", "version"); err == nil {
			info.HasCompose = res.OK()
		}
	}

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
func detectPlatform(ctx context.Context, r Runner) (Platform, string, []string) {
	// Diagnostics, not decoration. "unknown OS" with nothing behind it leaves
	// nobody — user or maintainer — able to say which probe misbehaved, and the
	// only way to find out is to guess and rebuild. Each attempt records what it
	// actually saw so the answer is on screen.
	var notes []string
	record := func(what string, res *sshcore.Result, err error) {
		switch {
		case err != nil:
			notes = append(notes, what+": "+err.Error())
		case res == nil:
			notes = append(notes, what+": no result")
		default:
			notes = append(notes, fmt.Sprintf("%s: exit=%d out=%q err=%q",
				what, res.ExitCode, firstLine(res.Stdout), firstLine(res.Stderr)))
		}
	}

	res, err := r.Probe(ctx, "uname", "-s")
	record("uname -s", res, err)
	if err == nil && res.OK() {
		kernel := strings.TrimSpace(string(res.Stdout))
		upper := strings.ToUpper(kernel)
		switch {
		case strings.EqualFold(kernel, "linux"):
			return PlatformLinux, kernel, notes
		case strings.EqualFold(kernel, "darwin"):
			return PlatformDarwin, kernel, notes
		case strings.Contains(upper, "BSD"):
			return PlatformBSD, kernel, notes
		// A POSIX emulation layer on Windows — Git for Windows, MSYS2, Cygwin.
		// uname answers there, so the Windows branch below is never reached and
		// the host was reported as an unknown OS despite plainly saying NT.
		case strings.HasPrefix(upper, "MINGW"), strings.HasPrefix(upper, "MSYS"),
			strings.HasPrefix(upper, "CYGWIN"), strings.Contains(upper, "WINDOWS"),
			strings.Contains(upper, "_NT-"):
			return PlatformWindows, kernel, notes
		case kernel != "":
			return PlatformUnknown, kernel, notes
		}
	}

	// PowerShell, not `cmd /c ver`.
	//
	// The cmd route was tried first and does not work through Windows OpenSSH:
	// sshd already wraps the remote command as `cmd.exe /c "<command>"`, and a
	// nested `cmd /c ver` inside that has its quotes redistributed, so the inner
	// cmd receives the command name as `ver"` — trailing quote included — and
	// reports it missing. The failure is invisible unless you read the error text
	// closely, which is what the probe transcript is for.
	//
	// -EncodedCommand has no such problem, and for the reason this adapter uses it
	// everywhere: the payload is one flat base64 token with no quotes in it, so
	// there is nothing for the outer shell to redistribute. It works identically
	// whether DefaultShell is cmd.exe or PowerShell, and it answers with the
	// edition name rather than a build number, so identification and description
	// are the same round trip instead of two.
	if caption, version, arch, ok := windowsIdentity(ctx, r, &notes); ok {
		if caption != "" {
			info := strings.TrimPrefix(caption, "Microsoft ")
			if arch != "" {
				info += " (" + arch + ")"
			}
			return PlatformWindows, info, append(notes, "version: "+version)
		}
		return PlatformWindows, "", notes
	}

	// Last resort, and only reachable if PowerShell itself is unavailable. Bare
	// `ver` rather than `cmd /c ver`: the outer shell is already cmd.exe when
	// DefaultShell is left at its default, so the builtin runs directly and the
	// nesting that broke the other form never happens.
	res, err = r.Probe(ctx, "ver")
	record("ver", res, err)
	if err == nil && res != nil {
		line := strings.TrimSpace(string(res.Stdout))
		if strings.Contains(line, "Windows") || strings.Contains(line, "Microsoft") {
			return PlatformWindows, line, notes
		}
		// Console output arrives in the machine's OEM codepage — 949 on a Korean
		// install — so a reply that is not valid UTF-8 is itself evidence of
		// Windows rather than a reason to give up.
		//
		// Kernel stays empty here. It holds what the server called itself, and
		// filling it with the reason we guessed instead put "Windows (stderr가
		// UTF-8이 아님)" on screen where an OS name belongs.
		if (line != "" && !utf8.ValidString(line)) || (len(res.Stderr) > 0 && !utf8.Valid(res.Stderr)) {
			notes = append(notes, i18n.S("판정 근거: ver 의 출력이 UTF-8 이 아님 (OEM 코드페이지)"))
			return PlatformWindows, "", notes
		}
	}
	return PlatformUnknown, "", notes
}

// windowsIdentity asks WMI who this machine is, and doubles as the Windows test.
//
// A successful answer is proof of Windows on its own: nothing else responds to
// Get-CimInstance Win32_OperatingSystem. Caption is the name people actually use
// for the machine — "Windows 10 Pro" — where `ver` gives only 10.0.19045.
func windowsIdentity(ctx context.Context, r Runner, notes *[]string) (caption, version, arch string, ok bool) {
	const script = `(Get-CimInstance Win32_OperatingSystem | ` +
		"ForEach-Object { \"$($_.Caption)`t$($_.Version)`t$($_.OSArchitecture)\" })"

	res, err := r.Probe(ctx, windowspowershell.Executable, windowspowershell.Args(script)...)
	switch {
	case err != nil:
		*notes = append(*notes, "powershell Win32_OperatingSystem: "+err.Error())
		return "", "", "", false
	case res == nil:
		return "", "", "", false
	case !res.OK():
		*notes = append(*notes, fmt.Sprintf("powershell Win32_OperatingSystem: exit=%d err=%q",
			res.ExitCode, firstLine(res.Stderr)))
		// A missing interpreter says nothing about the platform; a cmdlet that
		// ran and failed says this is Windows with WMI locked down.
		if !windowspowershell.IsMissingCmdlet(res.Stderr) && len(res.Stderr) > 0 {
			return "", "", "", false
		}
		return "", "", "", false
	}

	fields := strings.Split(strings.TrimSpace(string(res.Stdout)), "\t")
	*notes = append(*notes, fmt.Sprintf("powershell Win32_OperatingSystem: %q", firstLine(res.Stdout)))
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	switch len(fields) {
	case 0:
		return "", "", "", false
	case 1:
		return fields[0], "", "", fields[0] != ""
	case 2:
		return fields[0], fields[1], "", true
	default:
		return fields[0], fields[1], fields[2], true
	}
}

// detectWindows fills in what the Windows adapter needs beyond the OS name.
//
// Deliberately short: there is no init-system version to gate on, no sudo, and no
// journal group. Privilege is binary here — either the SSH account is in the
// Administrators group or the operation simply cannot be retried, which is why the
// UI must not offer the "retry as administrator" button it offers on Linux.
func detectWindows(ctx context.Context, r Runner, info *ServerInfo) {
	const script = `$id=[Security.Principal.WindowsIdentity]::GetCurrent();` +
		`$p=[Security.Principal.WindowsPrincipal]$id;` +
		"\"$($p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))`t" +
		"$(if (Get-Command docker -ErrorAction SilentlyContinue) {'1'} else {'0'})\""

	res, err := r.Probe(ctx, windowspowershell.Executable, windowspowershell.Args(script)...)
	if err != nil || res == nil || !res.OK() {
		if res != nil && len(res.Stderr) > 0 {
			info.Warnings = append(info.Warnings,
				"windows capabilities: "+firstLine([]byte(windowspowershell.ErrorText(res.Stderr))))
		}
		return
	}

	fields := strings.Split(strings.TrimSpace(string(res.Stdout)), "\t")
	if len(fields) > 0 && strings.EqualFold(strings.TrimSpace(fields[0]), "True") {
		// The nearest thing to root. Recorded under the same name the Linux path
		// uses so the UI does not need a second concept.
		info.IsRoot = true
	}
	if len(fields) > 1 && strings.TrimSpace(fields[1]) == "1" {
		info.HasDocker = true
	}
	// HasSudo stays false: there is no elevation command to run. An action that
	// needs administrator on Windows needs a different login, not a prefix.
}

// firstLine trims output to something that fits in a warning line. Detection
// notes are read on screen, and a 200-service dump helps nobody.
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
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
