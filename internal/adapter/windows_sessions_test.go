package adapter

import (
	"strings"
	"testing"
	"time"
)

// Captured from Windows 10 19045 with two logins from one account and one
// connection that had not authenticated yet.
const windowsSessionsSample = `#proc|2908|724|SYSTEM|1789000000
#proc|4628|2908|SYSTEM|1789003600
#proc|6460|4628|KTJ|1789003601
#proc|3876|2908|SYSTEM|1789003700
#proc|4652|3876|sshd_3876|1789003700
#proc|6392|2908|SYSTEM|1789003000
#proc|6036|6392|KTJ|1789003002
#self|6036
#auth|1789003601|ktj|121.151.244.121
#auth|1789003003|ktj|10.0.0.5
`

func sessionAt(t *testing.T, unix int64) time.Time {
	t.Helper()
	return time.Unix(unix, 0)
}

func TestParseWindowsSessions(t *testing.T) {
	got := ParseWindowsSessions(windowsSessionsSample, sessionAt(t, 1789003700))
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (the two KTJ logins): %+v", len(got), got)
	}

	byPID := map[int]SSHSession{}
	for _, s := range got {
		byPID[s.PID] = s
	}

	a, ok := byPID[6460]
	if !ok {
		t.Fatal("the newer login is missing")
	}
	if a.User != "KTJ" || a.From != "121.151.244.121" || a.Elapsed != 99 || a.Self {
		t.Errorf("6460 = %+v", a)
	}

	b, ok := byPID[6036]
	if !ok {
		t.Fatal("the older login is missing")
	}
	if !b.Self {
		t.Error("6036 is the session this app is using and is not marked so")
	}
	// Two logins by the same account must not share one address: that is the
	// case the tab exists for.
	if b.From != "10.0.0.5" {
		t.Errorf("6036 From = %q, want 10.0.0.5 — matched the wrong login", b.From)
	}
}

// The listener, the per-connection parent and the pre-authentication helper are
// not people. Counting sshd_NNNN would put every stranger knocking on the door
// on screen as a session.
func TestWindowsSessionsSkipsMachineAccounts(t *testing.T) {
	got := ParseWindowsSessions(windowsSessionsSample, sessionAt(t, 1789003700))
	for _, s := range got {
		if !realAccount(s.User) {
			t.Errorf("listed %q, which is not somebody logging in", s.User)
		}
	}
	for _, bad := range []string{"SYSTEM", "system", "sshd_3876", "NETWORK SERVICE", ""} {
		if realAccount(bad) {
			t.Errorf("realAccount(%q) = true", bad)
		}
	}
	for _, good := range []string{"KTJ", "deploy", "관리자"} {
		if !realAccount(good) {
			t.Errorf("realAccount(%q) = false", good)
		}
	}
}

// A session older than the log window keeps its user and its uptime and loses
// only the address. Dropping the row instead would hide somebody who is on the
// machine right now.
func TestWindowsSessionsKeepsLoginsWithNoLogRecord(t *testing.T) {
	raw := "#proc|1000|900|deploy|1789000000\n#self|1000\n"
	got := ParseWindowsSessions(raw, sessionAt(t, 1789003600))
	if len(got) != 1 {
		t.Fatalf("got %d, want 1: %+v", len(got), got)
	}
	if got[0].From != "" {
		t.Errorf("invented an address: %q", got[0].From)
	}
	if got[0].Elapsed != 3600 {
		t.Errorf("elapsed = %d, want 3600", got[0].Elapsed)
	}
}

// An address is only borrowed from a login that happened at the same moment.
func TestWindowsSessionsDoesNotBorrowAStaleAddress(t *testing.T) {
	raw := "#proc|1000|900|deploy|1789003600\n#auth|1789000000|deploy|203.0.113.9\n"
	got := ParseWindowsSessions(raw, sessionAt(t, 1789003700))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].From != "" {
		t.Errorf("took an address from a login an hour earlier: %q", got[0].From)
	}
}

func TestWindowsSessionsScriptIsBounded(t *testing.T) {
	s := WindowsSessionsScript()
	// The parent of process 0 is process 0. An unbounded walk up the tree from
	// there never returns, which is how the first version of this probe hung.
	if !strings.Contains(s, "$i -lt 8") {
		t.Error("the walk up the process tree has no bound")
	}
	if !strings.Contains(s, "ParentProcessId -le 4") {
		t.Error("the walk does not stop at the kernel processes")
	}
	for _, want := range []string{"#proc|", "#self|", "#auth|", "OpenSSH/Operational"} {
		if !strings.Contains(s, want) {
			t.Errorf("script never emits %q", want)
		}
	}
}

// Stop-Process ends one process; a session is a tree. Measured on Windows 10
// 19045: killing the session's sshd on its own left cmd.exe and powershell.exe
// running and the client still connected.
func TestWindowsEndSessionKillsTheTree(t *testing.T) {
	s := WindowsEndSessionScript(6332)
	if strings.Contains(s, "Stop-Process") {
		t.Error("uses Stop-Process, which leaves the shell and the connection alive")
	}
	if !strings.Contains(s, "taskkill.exe /PID 6332 /F /T") {
		t.Errorf("does not end the tree:\n%s", s)
	}
	// taskkill exits 128 for "already gone", which is success here, and its
	// stderr is localised. The process table answers in every locale.
	if !strings.Contains(s, `Get-CimInstance Win32_Process -Filter "ProcessId=6332"`) {
		t.Errorf("does not check whether the session actually went:\n%s", s)
	}
}

func TestParseWindowsEndSession(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"END=gone\r\n", true},
		{"END=alive\r\n", false},
		// No output means the probe never got as far as looking.
		{"", false},
		{"오류: 프로세스를 찾을 수 없습니다.", false},
	} {
		if got := ParseWindowsEndSession(tc.raw); got != tc.want {
			t.Errorf("%q → %v, want %v", tc.raw, got, tc.want)
		}
	}
}
