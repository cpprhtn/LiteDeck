package adapter

import (
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// Which shells a Windows box can give you a prompt in (§4.6).
//
// # Why this exists
//
// On Linux the answer is "the account's login shell" and there is nothing to
// choose. On Windows, sshd hands out whatever `HKLM\SOFTWARE\OpenSSH\DefaultShell`
// says — cmd.exe on a default install — and a developer's machine has three or
// four things they actually work in. Getting cmd when you wanted PowerShell is
// not a preference problem; half the commands you know do not exist there.
//
// # Why the distributions come from the registry
//
// wsl.exe ships with Windows 10 whether or not a distribution is installed, so
// "the file is there" is not the question. The obvious next move — `wsl -l -q`
// and read the lines — is wrong: on a machine with no distribution installed
// that command prints its **help text** and exits 0. Measured on the Windows
// box this was written against: 98 lines, localised, every one of which a
// line-per-distribution parser turns into a menu entry. The first run of this
// feature offered "WSL · --online, -o".
//
// HKCU\...\Lxss is where the distributions are actually registered. It is
// structured, it is not localised, and on a box with none it simply is not
// there.
//
// pwsh.exe (PowerShell 7) is a separate install from powershell.exe (5.1) and
// most machines have only the second.

// Shell is one prompt a host can give, and how to start it.
type Shell struct {
	// ID is stable and safe to store — "cmd", "powershell", "wsl:Ubuntu".
	ID string `json:"id"`
	// Label is what the menu says.
	Label string `json:"label"`
	// Argv is the command line, already split. Empty means "whatever sshd
	// hands out", which is the only correct answer on a POSIX host.
	Argv []string `json:"argv,omitempty"`
}

// WindowsShellsScript asks one PowerShell session what is installed.
func WindowsShellsScript() string {
	return strings.Join([]string{
		`$ErrorActionPreference = 'SilentlyContinue'`,
		`foreach ($n in 'powershell.exe','pwsh.exe') {`,
		`  $c = Get-Command $n`,
		`  if ($c) { Write-Output ("exe|" + $n) }`,
		`}`,
		`$lxss = 'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss'`,
		`if ((Test-Path $lxss) -and (Get-Command wsl.exe)) {`,
		`  $def = (Get-ItemProperty $lxss).DefaultDistribution`,
		`  foreach ($d in (Get-ChildItem $lxss)) {`,
		`    $p = Get-ItemProperty $d.PSPath`,
		// State 1 is "installed and ready". A distribution part-way through
		// `wsl --install` is in the registry and cannot give anybody a prompt.
		`    if ($p.DistributionName -and $p.State -eq 1) {`,
		`      $mark = if ($d.PSChildName -eq $def) { "*" } else { "" }`,
		`      Write-Output ("wsl|" + $mark + "|" + $p.DistributionName)`,
		`    }`,
		`  }`,
		`}`,
	}, "\n")
}

// ParseWindowsShells turns the probe's output into the menu.
//
// cmd is added here rather than probed: it is what sshd already gives, so it is
// available by definition and a box where it is missing could not have answered.
func ParseWindowsShells(raw string) []Shell {
	out := []Shell{{
		ID:    "cmd",
		Label: i18n.T("명령 프롬프트"),
		// Empty argv: this is sshd's DefaultShell on a stock install, and
		// starting it by name would ignore an admin who changed it on purpose.
		Argv: nil,
	}}
	var wsl []Shell
	for _, line := range strings.Split(raw, "\n") {
		kind, rest, ok := strings.Cut(strings.TrimSpace(strings.TrimRight(line, "\r")), "|")
		if !ok || rest == "" {
			continue
		}
		switch kind {
		case "exe":
			switch rest {
			case "powershell.exe":
				out = append(out, Shell{ID: "powershell", Label: "PowerShell", Argv: []string{"powershell.exe", "-NoLogo"}})
			case "pwsh.exe":
				out = append(out, Shell{ID: "pwsh", Label: "PowerShell 7", Argv: []string{"pwsh.exe", "-NoLogo"}})
			}
		case "wsl":
			mark, name, ok := strings.Cut(rest, "|")
			if !ok || !plausibleDistro(name) {
				continue
			}
			// Docker Desktop registers its plumbing here. -data has no init and
			// no shell at all, so a terminal on it opens and dies; that is the
			// exact failure this whole probe exists to avoid.
			if strings.EqualFold(name, "docker-desktop-data") {
				continue
			}
			sh := Shell{
				ID:    "wsl:" + name,
				Label: "WSL · " + name,
				Argv:  []string{"wsl.exe", "-d", name},
			}
			if mark == "*" {
				// The default goes first, because `wsl` with no arguments is
				// what the person already types.
				wsl = append([]Shell{sh}, wsl...)
			} else {
				wsl = append(wsl, sh)
			}
		}
	}
	return append(out, wsl...)
}

// plausibleDistro rejects what cannot be a distribution name.
//
// Defence in depth for the help-text trap described above: the script reads the
// registry now, so this should never have to reject anything. It rejects by
// what a name *cannot* contain rather than by a character whitelist — a
// distribution can be imported under any name its owner likes, Korean included,
// and a whitelist would hide the real thing to catch a hypothetical one.
//
// Whitespace is the strongest of these and it was nearly given up on a guess:
// `wsl --import "My Distro" ...` looks like it should work. Measured on Windows
// 10 19045 it exits -1 and registers nothing — a distribution name cannot
// contain a space, so nothing legitimate is lost by refusing one, and refusing
// it is what rejects a sentence of help text.
//
// wsl parses a leading dash as a flag and uses a colon as a separator, so
// neither can appear either. The rest is the shape of prose: help text quotes
// commands and ends in a full stop.
func plausibleDistro(name string) bool {
	switch {
	case name == "" || len(name) > 64:
		return false
	case strings.HasPrefix(name, "-"):
		return false
	case strings.ContainsAny(name, " \t\r\n:'\""):
		return false
	case strings.HasSuffix(name, "."):
		return false
	}
	return true
}

// wslInteractive is what wsl is asked to run.
//
// Bare `wsl.exe -d Ubuntu` looks right and is not. Measured over Windows
// OpenSSH's ConPTY on Windows 10 19045: it produces no output, never returns,
// and leaves two wsl.exe processes and LxssManager in StopPending — WSL stays
// wedged for every other program on the box until it reboots. Asking it to run
// a command instead (`-- ...`) works, and an interactive shell is a command.
//
// bash rather than sh because that is the login shell in every distribution
// people actually install, but Alpine ships busybox and has no bash, so the
// test decides at runtime. `exec` so the wrapper is gone and the user's exit
// closes the terminal rather than dropping them into a shell they did not ask
// for.
const wslInteractive = `/bin/sh -c "if [ -x /bin/bash ]; then exec /bin/bash -li; else exec /bin/sh -l; fi"`

// WindowsShellCommand builds the line that starts one shell, in a directory if
// one was asked for.
//
// Each shell wants its own spelling. cmd needs `/K cd /d`, PowerShell needs
// `-NoExit -Command Set-Location`, and wsl has `--cd`. There is no line that
// works in all three, which is why the old POSIX one — `cd X && exec $SHELL` —
// produced "지정된 이름, 디렉터리 이름 또는 볼륨 레이블 구문이 잘못되었습니다"
// on every Windows host that used "open in terminal".
//
// Returns "" for "just start the default shell", which is what an empty argv
// and an empty directory mean together.
func WindowsShellCommand(sh Shell, dir string) string {
	dir = strings.TrimSpace(dir)
	// A Windows path cannot contain a quote, so refusing one costs nothing and
	// removes the only way the quoting below could be broken. `%` is legal in a
	// path but cmd expands it, so that one is refused for the cmd line only.
	if strings.ContainsAny(dir, `"`+"\n\r") {
		dir = ""
	}

	switch {
	case strings.HasPrefix(sh.ID, "wsl:"):
		// Quoted even though a name cannot contain a space (see plausibleDistro):
		// the quoting costs nothing and is what keeps this correct if that ever
		// changes.
		line := `wsl.exe -d "` + strings.TrimPrefix(sh.ID, "wsl:") + `"`
		if dir != "" {
			// --cd goes before --, and it takes a Windows path as happily as a
			// Linux one, which is what the file explorer on a Windows host has.
			line += ` --cd "` + dir + `"`
		}
		return line + " -- " + wslInteractive

	case sh.ID == "powershell" || sh.ID == "pwsh":
		line := strings.Join(sh.Argv, " ")
		if dir != "" {
			// Single quotes inside a PowerShell literal are escaped by doubling.
			line += ` -NoExit -Command "Set-Location -LiteralPath '` +
				strings.ReplaceAll(dir, "'", "''") + `'"`
		}
		return line

	default: // cmd, or an unknown id treated as cmd
		if dir == "" || strings.Contains(dir, "%") {
			return ""
		}
		return `cmd.exe /K cd /d "` + dir + `"`
	}
}
