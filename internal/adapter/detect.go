// Package adapter abstracts over server operating systems (§3.3).
//
// Detection runs once per connection and decides which adapter handles a host
// and which of its features are available. A capability the server lacks is not
// an error: the UI disables that tab instead.
package adapter

import (
	"context"
	"strings"

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

// ServerInfo is what one probe of a server yields.
type ServerInfo struct {
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
)

// Capabilities reports which tabs this server supports (§3.3).
func (i ServerInfo) Capabilities() map[Capability]bool {
	return map[Capability]bool{
		CapServices:   i.HasSystemd,
		CapProcesses:  true, // ps is on every POSIX host
		CapContainers: i.HasDocker || i.HasPodman,
		CapMetrics:    true, // /proc, df
	}
}

// Detect probes a freshly connected server.
//
// Every probe is independent and failure-tolerant: a server that hides
// /etc/os-release or has no sudo is still perfectly usable, so a failed probe
// records a warning and leaves the field zero rather than aborting.
func Detect(ctx context.Context, r Runner) (ServerInfo, error) {
	var info ServerInfo

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
