package adapter

import (
	"strings"
	"testing"
	"time"
)

// Shaped after the log on Windows 10 19045: one account logging in repeatedly
// from one address, and a password attack from another.
const windowsLoginsSample = `#boot|1788960000
#span|1789043000
#ok|1789048817|ktj|121.151.244.121|64535
#end|1789048900|121.151.244.121|64535
#ok|1789047557|ktj|121.151.244.121|64530
#end|1789047600|121.151.244.121|64530
#ok|1789043100|ktj|10.0.0.5|51204
total 734 47
ip 730 109.160.32.80
ip 4 45.9.148.2
user 700 root
user 34 admin
`

func TestParseWindowsLogins(t *testing.T) {
	got := ParseWindowsLogins(windowsLoginsSample)

	if !got.HasLog {
		t.Error("HasLog is false for a log that answered")
	}
	if want := time.Unix(1789043000, 0); !got.Since.Equal(want) {
		t.Errorf("Since = %v, want %v — the screen labels the window with this", got.Since, want)
	}

	// Three logins and the boot record, newest first.
	if len(got.Logins) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(got.Logins), got.Logins)
	}
	for i := 1; i < len(got.Logins); i++ {
		if got.Logins[i].At.After(got.Logins[i-1].At) {
			t.Fatalf("row %d is newer than the one above it", i)
		}
	}
	if !got.Logins[3].Boot {
		t.Errorf("the boot record is not the oldest row: %+v", got.Logins)
	}

	newest := got.Logins[0]
	if newest.User != "ktj" || newest.From != "121.151.244.121" {
		t.Errorf("newest = %+v", newest)
	}
	if newest.Until == nil || newest.Until.Unix() != 1789048900 {
		t.Errorf("newest did not take its disconnect: until=%v", newest.Until)
	}

	// A login with no close record gets no duration and no claim either way:
	// Windows writes nothing on the way out, and the app's own connections were
	// measured with an Accepted record and no close record long after they had
	// gone. "아직 열려 있음" there would be an invention.
	unknown := got.Logins[2]
	if unknown.Open || unknown.Until != nil {
		t.Errorf("claimed to know how the unclosed login ended: %+v", unknown)
	}

	if got.Auth.Failed != 734 || got.Auth.Accepted != 47 {
		t.Errorf("auth = %d failed / %d accepted, want 734/47", got.Auth.Failed, got.Auth.Accepted)
	}
	if got.Auth.DistinctSources != 2 || got.Auth.DistinctUsers != 2 {
		t.Errorf("distinct = %d sources / %d users", got.Auth.DistinctSources, got.Auth.DistinctUsers)
	}
	if len(got.Auth.Sources) == 0 || got.Auth.Sources[0].Name != "109.160.32.80" {
		t.Errorf("sources = %+v, want the busiest first", got.Auth.Sources)
	}
}

// One person logged in twice keeps two separate sessions, and the one that
// leaves first is the one that gets closed.
//
// The disconnect here is for the *older* login, so matching on the address alone
// closes the wrong row — it would take the newest login that started before the
// disconnect, which is the one still sitting at a prompt.
func TestWindowsLoginsPairsByPortNotAddress(t *testing.T) {
	raw := strings.Join([]string{
		"#span|1789000000",
		"#ok|1789000100|ktj|203.0.113.7|40001",
		"#ok|1789000200|ktj|203.0.113.7|40002",
		"#end|1789000300|203.0.113.7|40001",
		"total 0 2",
	}, "\n")
	got := ParseWindowsLogins(raw)
	if len(got.Logins) != 2 {
		t.Fatalf("got %d", len(got.Logins))
	}
	byStart := map[int64]Login{}
	for _, l := range got.Logins {
		byStart[l.At.Unix()] = l
	}
	if l := byStart[1789000100]; l.Until == nil || l.Until.Unix() != 1789000300 {
		t.Errorf("the login on port 40001 did not take its own disconnect: %+v", l)
	}
	if l := byStart[1789000200]; l.Until != nil {
		t.Errorf("the login on port 40002 was closed by the other one's disconnect: %+v", l)
	}
}

// A client port comes round again. The second login must not be closed by the
// first one's disconnect, which happened before it started.
func TestWindowsLoginsDoesNotCloseALoginWithAnEarlierDisconnect(t *testing.T) {
	raw := strings.Join([]string{
		"#span|1789000000",
		"#ok|1789000100|ktj|203.0.113.7|40001",
		"#end|1789000150|203.0.113.7|40001",
		"#ok|1789000900|ktj|203.0.113.7|40001",
		"total 0 2",
	}, "\n")
	got := ParseWindowsLogins(raw)
	for _, l := range got.Logins {
		switch l.At.Unix() {
		case 1789000100:
			if l.Until == nil {
				t.Error("the first login did not take its own disconnect")
			}
		case 1789000900:
			if l.Until != nil {
				t.Errorf("the second login took a disconnect from before it started: %+v", l)
			}
		}
	}
}

// An empty log and "nobody has logged in" need opposite things said about them.
func TestWindowsLoginsMarksAnUnreadableLog(t *testing.T) {
	got := ParseWindowsLogins("#boot|1788960000\n#nolog\n")
	if got.HasLog {
		t.Error("HasLog is true for a log that said it had nothing")
	}
	if !got.Since.IsZero() {
		t.Errorf("invented a window: %v", got.Since)
	}
	// The boot record still stands: it does not come from the event log.
	if len(got.Logins) != 1 || !got.Logins[0].Boot {
		t.Errorf("lost the boot record: %+v", got.Logins)
	}
}

// The same attempt is written twice — "Failed password for invalid user root"
// and "Invalid user root" — and counting both doubles every number on screen.
func TestWindowsLoginsScriptCountsAnAttemptOnce(t *testing.T) {
	s := WindowsLoginsScript()
	if strings.Contains(s, `'Invalid user`) || strings.Contains(s, `^sshd: Invalid`) {
		t.Error("counts the 'Invalid user' line, which is the second record of one attempt")
	}
	if !strings.Contains(s, `Failed \S+ for (?:invalid user )?(\S+) from (\S+) port`) {
		t.Error("the failure pattern is not the one measured against the server")
	}
	// Every distinct source and account, because the screen says "8 of 36".
	for _, want := range []string{`foreach ($k in $ips.Keys)`, `foreach ($k in $users.Keys)`, `#span|`} {
		if !strings.Contains(s, want) {
			t.Errorf("script has no %q", want)
		}
	}
}
