package adapter

import (
	"strings"
	"testing"
	"time"
)

// Captured from Windows 10 19045, trimmed. Every value here is what the machine
// actually printed, including the 3389 rule nobody had noticed was on.
const windowsSecuritySample = `#profile|Domain|True|NotConfigured|False|0
#profile|Private|True|NotConfigured|False|1
#profile|Public|True|Allow|True|0
#rule|TCP|7680|Any|Delivery Optimization (TCP-In)
#rule|TCP|3389|Any|원격 데스크톱 - 사용자 모드(TCP-In)
#rule|TCP|22|Any|OpenSSH SSH Server (sshd)
#rulecount|56
#lockout|10|10|10
#defender|True|True|0
#logspan|1789046279
#fail|1789048800|456
#fail|1789045200|264
#atk|109.160.32.80|500
#atk|109.160.32.120|141
#atk|102.220.160.189|59
`

func TestParseWindowsSecurity(t *testing.T) {
	got := ParseWindowsSecurity(windowsSecuritySample)

	if !got.ProfilesRead || !got.RulesRead || !got.HasLog {
		t.Fatalf("a section was marked unreadable: %+v", got)
	}
	if len(got.Profiles) != 3 {
		t.Fatalf("got %d profiles, want 3", len(got.Profiles))
	}

	// NotConfigured is the stock state and means the Windows default, which is
	// block. Reporting it as "not blocking" would tell somebody their machine
	// is open when it is not.
	dom := got.Profiles[0]
	if !dom.InboundBlocked || dom.InboundExplicit {
		t.Errorf("Domain = %+v, want blocked by default and not explicit", dom)
	}
	// Private is the one the live network is in; the other two are on and not
	// deciding anything right now.
	if !got.Profiles[1].Active || got.Profiles[0].Active || got.Profiles[2].Active {
		t.Errorf("the active profile is wrong: %+v", got.Profiles)
	}
	// An explicit Allow is somebody's decision, not an untouched machine.
	pub := got.Profiles[2]
	if pub.InboundBlocked || !pub.InboundExplicit || !pub.LogBlocked {
		t.Errorf("Public = %+v, want an explicit allow with logging on", pub)
	}

	if len(got.Rules) != 3 || got.RuleTotal != 56 {
		t.Fatalf("rules = %d of %d, want 3 of 56", len(got.Rules), got.RuleTotal)
	}
	// The name survives the transport intact, which is the whole reason the
	// prelude sets UTF-8: without it Windows' own localised rule names arrive
	// as mojibake and nobody can tell which rule opened the port.
	if got.Rules[1].Name != "원격 데스크톱 - 사용자 모드(TCP-In)" || got.Rules[1].Port != "3389" {
		t.Errorf("rule = %+v", got.Rules[1])
	}

	if got.Lockout == nil || got.Lockout.Threshold != 10 || got.Lockout.Duration != 10 {
		t.Errorf("lockout = %+v", got.Lockout)
	}
	if got.Defender == nil || !got.Defender.RealTime || got.Defender.SignatureAge != 0 {
		t.Errorf("defender = %+v", got.Defender)
	}
	if got.LogSince == nil || got.LogSince.Unix() != 1789046279 {
		t.Errorf("logSince = %v", got.LogSince)
	}

	if got.Failed != 720 || len(got.Failures) != 2 {
		t.Errorf("failures = %d in %d buckets, want 720 in 2", got.Failed, len(got.Failures))
	}
	// A chart is drawn oldest first, and PowerShell hands back hashtable keys
	// in whatever order it hashed them.
	if !got.Failures[0].At.Before(got.Failures[1].At) {
		t.Error("failure buckets are not in time order")
	}
	if got.Failures[0].Count != 264 {
		t.Errorf("the oldest bucket is %d, want 264 — the sample arrives newest first "+
			"because that is the order PowerShell hands back hashtable keys", got.Failures[0].Count)
	}
	if len(got.Attackers) != 3 || got.Attackers[0].Address != "109.160.32.80" {
		t.Errorf("attackers = %+v, want the busiest first", got.Attackers)
	}
}

// A rule that names an address is the nearest thing Windows has to a ban, and
// an attacker already blocked must drop off the list — a list of addresses
// somebody can do nothing about is one they stop reading.
func TestWindowsSecurityDropsBlockedAttackers(t *testing.T) {
	raw := strings.Join([]string{
		"#blocked|109.160.32.80",
		"#blocked|203.0.113.0/24",
		"#atk|109.160.32.80|500",
		"#atk|203.0.113.9|40",
		"#atk|198.51.100.7|12",
	}, "\n")
	got := ParseWindowsSecurity(raw)
	if len(got.BlockedRemote) != 2 {
		t.Fatalf("blocked = %v", got.BlockedRemote)
	}
	if len(got.Attackers) != 1 || got.Attackers[0].Address != "198.51.100.7" {
		t.Errorf("attackers = %+v, want only the one nothing blocks", got.Attackers)
	}
}

// "Could not read the firewall" and "the firewall has nothing on it" are
// opposite messages, and Windows has no sudo to retry with — so the distinction
// has to survive the parse or it is gone for good.
func TestWindowsSecurityKeepsUnreadableApart(t *testing.T) {
	denied := ParseWindowsSecurity("#fwdenied\n#ruledenied\n#nolog\n")
	if denied.ProfilesRead || denied.RulesRead || denied.HasLog {
		t.Errorf("an unreadable section came back as read: %+v", denied)
	}
	if denied.Lockout != nil || denied.Defender != nil {
		t.Error("invented a policy nothing reported")
	}

	// Nothing at all is also unreadable, not "quiet".
	empty := ParseWindowsSecurity("")
	if len(empty.Profiles) != 0 || empty.RuleTotal != 0 {
		t.Errorf("read something out of nothing: %+v", empty)
	}
}

func TestWindowsSecurityScriptStaysOffTheSlowPaths(t *testing.T) {
	s := WindowsSecurityScript()

	// `$r | Get-NetFirewallPortFilter` per rule is a round trip through the
	// policy store each time: measured at 4.6 s against 2.2 s for one bulk read
	// into a hashtable.
	if strings.Contains(s, "$r | Get-NetFirewallPortFilter") {
		t.Error("joins the port filter per rule, which doubles the read")
	}
	if !strings.Contains(s, "$ports[$f.InstanceID] = $f") {
		t.Error("does not build the port lookup in one pass")
	}

	// net accounts is localised: a parser keyed on its labels reads a Korean
	// machine as having no lockout policy at all.
	if strings.Contains(s, "net.exe accounts") || strings.Contains(s, "net accounts") {
		t.Error("reads the lockout policy from localised output")
	}
	for _, want := range []string{"LockoutBadCount", "secedit.exe /export"} {
		if !strings.Contains(s, want) {
			t.Errorf("script has no %q", want)
		}
	}

	// Windows uses RPC, RPC-EPMap and IPHTTPS as symbolic LocalPort values, and
	// every ICMPv6 rule carries one. They are not open ports.
	if !strings.Contains(s, `$lp -notmatch '^[0-9][0-9,\-]*$'`) {
		t.Error("does not filter out the symbolic port names")
	}
	// The temp file secedit writes is the only channel it has, and leaving one
	// per read on somebody's server is not acceptable.
	if !strings.Contains(s, "Remove-Item $tmp -Force") {
		t.Error("leaves the secedit export behind")
	}
}

// The OpenSSH log is circular and small. The screen must be able to say what it
// actually covers rather than labelling an hour of data as a day.
func TestWindowsSecurityCarriesItsOwnWindow(t *testing.T) {
	got := ParseWindowsSecurity("#logspan|1789046279\n#fail|1789045200|10\n")
	if got.LogSince == nil {
		t.Fatal("no window reported")
	}
	if !got.LogSince.Equal(time.Unix(1789046279, 0)) {
		t.Errorf("logSince = %v", got.LogSince)
	}
	none := ParseWindowsSecurity("#nolog\n")
	if none.LogSince != nil {
		t.Errorf("invented a window for a log that said nothing: %v", none.LogSince)
	}
}
