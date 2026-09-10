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
		`  foreach ($d in (Get-ChildItem $lxss)) {`,
		`    $n = (Get-ItemProperty $d.PSPath).DistributionName`,
		`    if ($n) { Write-Output ("wsl|" + $n) }`,
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
			// A second line of defence for the help-text trap above.
			//
			// Shaped rather than whitelisted: a distribution can be imported
			// under any name its owner likes, including a Korean one, and a
			// character whitelist would hide it. What help text looks like is
			// prose (whitespace), flags (a leading dash) and headings (a colon,
			// which cannot appear in a distribution name because wsl uses it as
			// a separator).
			if strings.ContainsAny(rest, " \t:") || strings.HasPrefix(rest, "-") || len(rest) > 64 {
				continue
			}
			out = append(out, Shell{
				ID:    "wsl:" + rest,
				Label: "WSL · " + rest,
				Argv:  []string{"wsl.exe", "-d", rest},
			})
		}
	}
	return out
}

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
		line := strings.Join(sh.Argv, " ")
		if dir != "" {
			line += ` --cd "` + dir + `"`
		}
		return line

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
