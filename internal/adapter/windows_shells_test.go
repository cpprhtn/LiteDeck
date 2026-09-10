package adapter

import (
	"strings"
	"testing"
)

func TestWindowsShellsAlwaysOffersCmd(t *testing.T) {
	// A probe that failed, or a box with nothing else installed. cmd is what
	// sshd already handed out to run the probe, so it cannot be missing.
	got := ParseWindowsShells("")
	if len(got) != 1 || got[0].ID != "cmd" {
		t.Fatalf("got %+v, want just cmd", got)
	}
	if len(got[0].Argv) != 0 {
		t.Errorf("cmd argv = %q — it must stay empty so an admin who changed "+
			"DefaultShell on purpose still gets what they chose", got[0].Argv)
	}
}

func TestWindowsShellsParsesTheProbe(t *testing.T) {
	raw := "exe|powershell.exe\r\nexe|pwsh.exe\r\nwsl||Ubuntu\r\nwsl|*|Debian\r\n"
	got := ParseWindowsShells(raw)
	// Debian is the default, so it comes before Ubuntu: `wsl` with no argument
	// is what the person already types.
	want := []string{"cmd", "powershell", "pwsh", "wsl:Debian", "wsl:Ubuntu"}
	if len(got) != len(want) {
		t.Fatalf("got %d shells, want %d: %+v", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("[%d] = %q, want %q", i, got[i].ID, id)
		}
		if got[i].Label == "" {
			t.Errorf("[%d] %s has no label", i, id)
		}
	}
}

// wsl.exe ships with Windows 10 whether or not a distribution is installed.
// The machine this was written against has the executable and no distributions,
// and offering a WSL prompt there opens a terminal that dies immediately.
func TestWindowsShellsSkipsWSLWithNoDistributions(t *testing.T) {
	got := ParseWindowsShells("exe|powershell.exe\n")
	for _, s := range got {
		if strings.HasPrefix(s.ID, "wsl") {
			t.Fatalf("offered %q with no distribution in the probe output", s.ID)
		}
	}
}

// Not every distribution is Ubuntu, and the version is part of the name — a box
// can carry Ubuntu and Ubuntu-22.04 side by side and `wsl -d` wants the exact
// string. The name goes through untouched, whatever it says.
func TestWindowsShellsKeepsWhateverTheDistributionIsCalled(t *testing.T) {
	names := []string{
		"Ubuntu", "Ubuntu-22.04", "Ubuntu-24.04", "Debian", "kali-linux",
		"openSUSE-Leap-15.6", "Arch", "우분투",
	}
	var raw strings.Builder
	for _, n := range names {
		raw.WriteString("wsl||" + n + "\n")
	}
	got := ParseWindowsShells(raw.String())
	if len(got) != len(names)+1 { // +1 for cmd
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(names)+1, got)
	}
	for i, n := range names {
		if want := "wsl:" + n; got[i+1].ID != want {
			t.Errorf("[%d] = %q, want %q", i, got[i+1].ID, want)
		}
	}
	// The name is quoted on the command line, so it is one argument whatever
	// it says.
	if line := WindowsShellCommand(got[3], ""); !strings.HasPrefix(line, `wsl.exe -d "Ubuntu-24.04" -- `) {
		t.Errorf("got %q", line)
	}
}

// `wsl.exe -d X` on its own hangs under Windows OpenSSH's ConPTY and wedges
// LxssManager for the whole machine until it reboots. Measured on Windows 10
// 19045. It must always be asked to run something.
func TestWSLIsNeverStartedBare(t *testing.T) {
	shells := ParseWindowsShells("wsl||Ubuntu\n")
	for _, sh := range shells {
		if !strings.HasPrefix(sh.ID, "wsl:") {
			continue
		}
		for _, dir := range []string{"", "/etc", `C:\srv`} {
			line := WindowsShellCommand(sh, dir)
			if !strings.Contains(line, " -- ") {
				t.Errorf("dir %q → %q: no command, so wsl opens interactively and hangs", dir, line)
			}
			if dir != "" && strings.Index(line, "--cd") > strings.Index(line, " -- ") {
				t.Errorf("dir %q → %q: --cd must come before --", dir, line)
			}
		}
	}
}

// `wsl --import "My Distro" ...` looks like it should work. On Windows 10 19045
// it exits -1 and registers nothing, so a name with a space cannot come out of
// the registry — and refusing one is what rejects a sentence of help text.
func TestWindowsShellsRefusesASpacedName(t *testing.T) {
	got := ParseWindowsShells("wsl||My Distro\nwsl||Ubuntu-24.04\n")
	for _, s := range got {
		if s.ID == "wsl:My Distro" {
			t.Fatal("accepted a name Windows will not register")
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want cmd + Ubuntu-24.04: %+v", len(got), got)
	}
}

// Docker Desktop registers its own plumbing under Lxss. -data has no init and
// no shell, so a terminal on it opens and dies — the exact failure this probe
// exists to prevent.
func TestWindowsShellsSkipsDockerDesktopData(t *testing.T) {
	got := ParseWindowsShells("wsl||docker-desktop\nwsl||docker-desktop-data\nwsl||Ubuntu\n")
	for _, s := range got {
		if strings.EqualFold(s.ID, "wsl:docker-desktop-data") {
			t.Fatal("offered docker-desktop-data, which cannot give a prompt")
		}
	}
	if len(got) != 3 { // cmd + docker-desktop + Ubuntu
		t.Errorf("got %d entries: %+v", len(got), got)
	}
}

// The first version of this read `wsl -l -q`, which on a box with no
// distribution prints its help — 98 localised lines on the machine this was
// measured on — and exits 0. The script asks the registry now, but the parser
// keeps its own guard: a distribution name is one token.
func TestWindowsShellsRefusesHelpTextAsDistributions(t *testing.T) {
	help := strings.Join([]string{
		"wsl||옵션:",
		"wsl||--online, -o",
		"wsl||'wsl --install'로 설치할 수 있는 배포판 목록을 표시합니다.",
		"wsl||--status",
		"wsl||Linux용 Windows 하위 시스템의 상태를 표시합니다.",
		"wsl||Ubuntu",
	}, "\n")
	got := ParseWindowsShells(help)
	var wsl []string
	for _, s := range got {
		if strings.HasPrefix(s.ID, "wsl:") {
			wsl = append(wsl, s.ID)
		}
	}
	if len(wsl) != 1 || wsl[0] != "wsl:Ubuntu" {
		t.Errorf("got %q, want only wsl:Ubuntu — the rest is help text", wsl)
	}
}

func TestWindowsShellCommand(t *testing.T) {
	shells := ParseWindowsShells("exe|powershell.exe\nwsl||Ubuntu\n")
	byID := map[string]Shell{}
	for _, s := range shells {
		byID[s.ID] = s
	}

	for _, tc := range []struct {
		name, id, dir, want string
	}{
		// No directory and the default shell: let sshd answer.
		{"cmd, nowhere", "cmd", "", ""},
		{"cmd, somewhere", "cmd", `C:\srv\api`, `cmd.exe /K cd /d "C:\srv\api"`},
		{"powershell, nowhere", "powershell", "", "powershell.exe -NoLogo"},
		{"powershell, somewhere", "powershell", `C:\srv\api`,
			`powershell.exe -NoLogo -NoExit -Command "Set-Location -LiteralPath 'C:\srv\api'"`},
		{"wsl, nowhere", "wsl:Ubuntu", "", `wsl.exe -d "Ubuntu" -- ` + wslInteractive},
		{"wsl, somewhere", "wsl:Ubuntu", "/home/deploy", `wsl.exe -d "Ubuntu" --cd "/home/deploy" -- ` + wslInteractive},
	} {
		if got := WindowsShellCommand(byID[tc.id], tc.dir); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// A Windows path cannot contain a quote and cmd expands %VAR%. Neither should
// be able to turn "start here" into "run this".
func TestWindowsShellCommandRefusesInjection(t *testing.T) {
	shells := ParseWindowsShells("exe|powershell.exe\n")
	byID := map[string]Shell{}
	for _, s := range shells {
		byID[s.ID] = s
	}

	for _, dir := range []string{
		`C:\a" & calc.exe & "`,
		"C:\\a\nnet user",
		`C:\%USERPROFILE%\x`,
	} {
		got := WindowsShellCommand(byID["cmd"], dir)
		if got != "" {
			t.Errorf("cmd accepted %q → %q", dir, got)
		}
	}
	// PowerShell quotes with a literal, so a quote in the path is dropped
	// rather than closing the string.
	if got := WindowsShellCommand(byID["powershell"], `C:\a" ; calc`); strings.Contains(got, `calc`) {
		t.Errorf("powershell carried the injection through: %q", got)
	}
}

// The probe is inline, not Start-Process.
//
// Start-Process + WaitForExit never returns for wsl.exe on Windows 10 19045 —
// measured, both with a new window and with the console inherited — and killing
// the hung child is itself what wedges LxssManager. The inline call is the one
// that comes back.
func TestWSLProbeIsInline(t *testing.T) {
	script := WSLProbeScript("Ubuntu-24.04", 45)
	if strings.Contains(script, "Start-Process") {
		t.Errorf("uses Start-Process, which does not return for wsl.exe:\n%s", script)
	}
	if strings.Contains(script, "Kill()") || strings.Contains(script, "Stop-Process") {
		t.Errorf("kills the child, which is what wedges the service:\n%s", script)
	}
	for _, want := range []string{`wsl.exe -d 'Ubuntu-24.04' -- true`, "WSL=ready", "WSL=broken"} {
		if !strings.Contains(script, want) {
			t.Errorf("script has no %q:\n%s", want, script)
		}
	}
}

// The bug that cost the user their WSL: one abandoned wake-up per press of the
// button. Three were caught running on the server at once, fourteen seconds
// apart, with LxssManager stuck behind them.
//
// The script must look before it starts one, and wait for whatever is already
// in flight rather than adding to it.
func TestWSLProbeNeverStacks(t *testing.T) {
	script := WSLProbeScript("Ubuntu-24.04", 45)

	// It has to find the ones already running before it starts its own.
	probes := strings.Index(script, "Win32_Process")
	start := strings.Index(script, "wsl.exe -d 'Ubuntu-24.04' -- true")
	if probes < 0 || start < 0 || probes > start {
		t.Fatalf("starts a wake-up before looking for one:\n%s", script)
	}
	if !strings.Contains(script, "WSL=starting") {
		t.Error("no way to say a wake-up is already in flight, so the caller reports a failure and the user presses again")
	}
	if !strings.Contains(script, "(Probes).Count -gt 0") {
		t.Error("does not wait for the wake-up already running")
	}

	// Only wake-ups count, not terminals. Our own WSL terminals are long-lived
	// wsl.exe processes by design; counting those would report "still starting"
	// forever on a healthy machine.
	if !strings.Contains(script, `$_.CommandLine -like '*-- true'`) {
		t.Errorf("counts every wsl.exe, including the terminals people have open:\n%s", script)
	}
}

// A service already in StopPending cannot be helped by one more client, and the
// three orphans that put it there arrived exactly that way.
func TestWSLProbeRefusesToTouchAWedgedService(t *testing.T) {
	script := WSLProbeScript("Ubuntu-24.04", 45)
	svc := strings.Index(script, "StopPending")
	start := strings.Index(script, "wsl.exe -d 'Ubuntu-24.04' -- true")
	if svc < 0 {
		t.Fatalf("never looks at LxssManager:\n%s", script)
	}
	if svc > start {
		t.Error("starts a wake-up before checking whether the service is stuck")
	}
	// Slow and stuck need different words: one asks for patience, the other for
	// a reboot.
	if !strings.Contains(script, "TotalSeconds -gt 180") {
		t.Error("a wake-up running for minutes is still reported as merely slow")
	}
}

func TestParseWSLProbe(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want WSLProbeState
	}{
		{"WSL=ready\r\n", WSLReady},
		{"WSL=starting\r\n", WSLStarting},
		{"WSL=hung\r\n", WSLHung},
		{"WSL=broken\r\n", WSLBroken},
		{"", WSLBroken},
		{"something else entirely", WSLBroken},
	} {
		if got := ParseWSLProbe(tc.raw); got != tc.want {
			t.Errorf("%q → %v, want %v", tc.raw, got, tc.want)
		}
	}
}
