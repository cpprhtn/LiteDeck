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
	raw := "exe|powershell.exe\r\nexe|pwsh.exe\r\nwsl|Ubuntu\r\nwsl|Debian\r\n"
	got := ParseWindowsShells(raw)
	want := []string{"cmd", "powershell", "pwsh", "wsl:Ubuntu", "wsl:Debian"}
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

// The first version of this read `wsl -l -q`, which on a box with no
// distribution prints its help — 98 localised lines on the machine this was
// measured on — and exits 0. The script asks the registry now, but the parser
// keeps its own guard: a distribution name is one token.
func TestWindowsShellsRefusesHelpTextAsDistributions(t *testing.T) {
	help := strings.Join([]string{
		"wsl|옵션:",
		"wsl|--online, -o",
		"wsl|'wsl --install'로 설치할 수 있는 배포판 목록을 표시합니다.",
		"wsl|--status",
		"wsl|Ubuntu",
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
	shells := ParseWindowsShells("exe|powershell.exe\nwsl|Ubuntu\n")
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
		{"wsl, nowhere", "wsl:Ubuntu", "", "wsl.exe -d Ubuntu"},
		{"wsl, somewhere", "wsl:Ubuntu", "/home/deploy", `wsl.exe -d Ubuntu --cd "/home/deploy"`},
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
