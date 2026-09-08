package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "security", name))
	if err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	return string(b)
}

// The three states a tool can be in, and why "not installed" is not "off".
//
// Read from a real server: iptables has no unit at all, nftables has one that
// is disabled, ufw has one that is enabled and active. Telling a user their
// firewall is "off" when the package was never installed sends them looking for
// a switch that does not exist.
func TestParseUnitsSeparatesMissingFromDisabled(t *testing.T) {
	got := ParseSecurityUnits(golden(t, "ubuntu-24.04-units.txt"))
	by := map[string]SecurityUnit{}
	for _, u := range got {
		by[u.Name] = u
	}
	if len(by) != 4 {
		t.Fatalf("유닛 %d개, 기대 4개: %+v", len(by), got)
	}
	for _, tc := range []struct {
		name      string
		installed bool
		enabled   bool
		active    bool
	}{
		{"ufw.service", true, true, true},
		{"nftables.service", true, false, false},
		{"iptables.service", false, false, false},
		{"fail2ban.service", true, true, true},
	} {
		u := by[tc.name]
		if u.Installed != tc.installed || u.Enabled != tc.enabled || u.Active != tc.active {
			t.Errorf("%s → 설치=%v 켜짐=%v 활성=%v, 기대 %v/%v/%v",
				tc.name, u.Installed, u.Enabled, u.Active, tc.installed, tc.enabled, tc.active)
		}
	}
}

// ufw is a oneshot: it loads rules at boot and exits. Reading "active" as
// "SubState == running" reports a working firewall as a stopped one.
func TestOneshotFirewallCountsAsActive(t *testing.T) {
	for _, u := range ParseSecurityUnits(golden(t, "ubuntu-24.04-units.txt")) {
		if u.Name != "ufw.service" {
			continue
		}
		if !u.Active {
			t.Error("SubState=exited 인 oneshot 을 비활성으로 읽었다")
		}
		if u.SubState != "exited" {
			t.Errorf("SubState %q, 기대 exited — 도구설명에 그대로 써야 한다", u.SubState)
		}
	}
}

// The whole reason this feature exists.
//
// On the server this was captured from, ufw.service was enabled and active
// while ufw itself was off. A screen that reads the unit alone puts a green
// light on a firewall that is not running.
func TestUfwConfIsWhatSaysWhetherItIsOn(t *testing.T) {
	on, found := ParseUfwConf(golden(t, "ubuntu-24.04-ufw.conf"))
	if !found {
		t.Fatal("ENABLED 줄을 못 찾았다")
	}
	if on {
		t.Error("ENABLED=no 인데 켜졌다고 읽었다")
	}

	yes, found := ParseUfwConf("ENABLED=yes\nLOGLEVEL=low\n")
	if !found || !yes {
		t.Error("ENABLED=yes 를 못 읽었다")
	}
	if _, found := ParseUfwConf("LOGLEVEL=low\n"); found {
		t.Error("ENABLED 줄이 없는데 찾았다고 했다")
	}
	// Commented-out lines are not settings.
	if _, found := ParseUfwConf("#ENABLED=yes\n"); found {
		t.Error("주석 처리된 줄을 설정으로 읽었다")
	}
}

// Which jails the file turns on. The effective set needs fail2ban-client and
// root; this is what the file declares, and the screen says so.
func TestParseJailsReadsEnabledSections(t *testing.T) {
	jails := ParseFail2banJails(golden(t, "ubuntu-24.04-jail.local"))
	if len(jails) != 1 || jails[0] != "sshd" {
		t.Errorf("jail %v, 기대 [sshd]", jails)
	}

	multi := ParseFail2banJails(`[DEFAULT]
enabled = true

[sshd]
enabled = true

[nginx-http-auth]
enabled = false

[postfix]
enabled = true
`)
	if len(multi) != 2 || multi[0] != "sshd" || multi[1] != "postfix" {
		t.Errorf("jail %v, 기대 [sshd postfix] — DEFAULT 는 jail 이 아니고 false 는 빠져야 한다", multi)
	}
}

// The script's sections, and what happens when a file is not there.
//
// A missing file writes nothing at all, so without the markers an absent
// ufw.conf and an empty one would look the same — and so would an absent
// ufw.conf and a jail.local that happened to follow it.
func TestSplitSecurityOutputHandlesMissingFiles(t *testing.T) {
	units, ufw, jails, mods := SplitSecurityOutput(
		"#units\nId=ufw.service\nLoadState=loaded\n#ufw\n#jails\n[sshd]\nenabled = true\n" +
			"#modules\nnf_tables 380928 814 - Live 0x0\n#end\n")
	if !strings.Contains(mods, "nf_tables") {
		t.Errorf("모듈 구역이 비었다: %q", mods)
	}
	if !strings.Contains(units, "Id=ufw.service") {
		t.Errorf("유닛 구역이 비었다: %q", units)
	}
	if strings.TrimSpace(ufw) != "" {
		t.Errorf("없는 파일인데 내용이 왔다: %q", ufw)
	}
	if !strings.Contains(jails, "[sshd]") {
		t.Errorf("jail 구역이 비었다: %q", jails)
	}
}

// The script goes to `sh -c` whole, so nothing in it may come from a caller.
func TestSecurityScriptIsAConstantWithNoHoles(t *testing.T) {
	for _, bad := range []string{"%s", "%v", "$1", "${"} {
		if strings.Contains(SecurityScript, bad) {
			t.Errorf("스크립트에 %q 가 있다 — 무엇이든 끼워 넣을 자리가 생기면 안 된다", bad)
		}
	}
	for _, u := range SecurityUnits {
		if !strings.Contains(SecurityScript, u) {
			t.Errorf("%s 를 묻지 않는다", u)
		}
	}
}

// The rule list a real server produced, and the three things a reader has to
// work out for themselves when it is printed raw.
func TestParseUfwStatus(t *testing.T) {
	s := ParseUfwStatus(golden(t, "ubuntu-24.04-ufw-status.txt"))
	if !s.Active {
		t.Error("Status: active 인데 비활성으로 읽었다")
	}
	if s.Incoming != "deny" || s.Outgoing != "allow" {
		t.Errorf("기본 정책 in=%q out=%q, 기대 deny/allow", s.Incoming, s.Outgoing)
	}
	// Fourteen printed lines, seven rules: v4 and v6 say the same thing twice.
	if len(s.Rules) != 7 {
		t.Fatalf("규칙 %d개, 기대 7개 — v6 중복이 합쳐지지 않았다: %+v", len(s.Rules), s.Rules)
	}
	first := s.Rules[0]
	if first.To != "22/tcp" || first.Action != "ALLOW IN" || first.From != "Anywhere" {
		t.Errorf("첫 규칙 %+v", first)
	}
	if !first.V4 || !first.V6 {
		t.Error("22/tcp 는 v4·v6 둘 다인데 한쪽만으로 읽었다")
	}
	// A rule can open several ports at once, and the cross-reference below
	// needs each of them, not the string.
	var nginx FirewallRule
	for _, r := range s.Rules {
		if strings.Contains(r.To, ",") {
			nginx = r
		}
	}
	if len(nginx.Ports) != 2 || nginx.Ports[0] != "80" || nginx.Ports[1] != "443" {
		t.Errorf("80,443 을 포트 목록으로 못 갈랐다: %+v", nginx)
	}
	if nginx.Comment != "Nginx Full" {
		t.Errorf("프로필 이름 %q, 기대 %q", nginx.Comment, "Nginx Full")
	}
}

// An inactive firewall prints one line and no table.
func TestParseUfwStatusInactive(t *testing.T) {
	s := ParseUfwStatus("Status: inactive\n")
	if s.Active {
		t.Error("inactive 를 활성으로 읽었다")
	}
	if len(s.Rules) != 0 {
		t.Errorf("규칙이 없는데 %d개 나왔다", len(s.Rules))
	}
}

// What the kernel is running, read from /proc/modules without root.
//
// The unit state does not answer this. nftables.service is Type=oneshot: it
// loads /etc/nftables.conf at boot and exits, so Ubuntu ships it disabled and
// uses ufw as the front end. A screen that reads `dead` as "no firewall" puts a
// red light on nearly every Ubuntu server — and a panel that cries wolf on a
// healthy machine is worse than no panel, because the colour stops meaning
// anything after the third time.
func TestFirewallModulesReadTheKernelNotTheUnit(t *testing.T) {
	k := ParseFirewallModules(golden(t, "ubuntu-24.04-modules.txt"))
	if !k.NFTables {
		t.Error("nf_tables 가 올라와 있는데 못 봤다")
	}
	if k.NFTablesRefs != 814 {
		t.Errorf("nf_tables 참조 %d, 기대 814", k.NFTablesRefs)
	}
	// Loaded with nobody using it. Legacy iptables is present on this box and
	// doing nothing, which is not the same as being in use.
	if !k.IPTables {
		t.Error("ip_tables 모듈이 있는데 없다고 했다")
	}
	if k.IPTablesRefs != 0 {
		t.Errorf("ip_tables 참조 %d, 기대 0 — 올라와 있는 것과 쓰이는 것은 다르다", k.IPTablesRefs)
	}
	if !k.InUse() {
		t.Error("참조 814 인데 안 쓰인다고 했다")
	}
}

func TestFirewallModulesOnAKernelWithNone(t *testing.T) {
	k := ParseFirewallModules("overlay 212992 0 - Live 0x0\nbtrfs 2056192 0 - Live 0x0\n")
	if k.NFTables || k.IPTables || k.InUse() {
		t.Errorf("netfilter 모듈이 없는데 있다고 했다: %+v", k)
	}
}

// Loaded but idle is not "in use". A box where the modules came up with the
// kernel and nothing ever added a rule must not read as protected.
func TestLoadedButUnusedIsNotInUse(t *testing.T) {
	k := ParseFirewallModules("nf_tables 380928 0 - Live 0x0\nip_tables 32768 0 - Live 0x0\n")
	if !k.NFTables {
		t.Error("모듈은 올라와 있다")
	}
	if k.InUse() {
		t.Error("참조가 0인데 쓰인다고 했다")
	}
}
