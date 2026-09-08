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
	units, ufw, jails := SplitSecurityOutput(
		"#units\nId=ufw.service\nLoadState=loaded\n#ufw\n#jails\n[sshd]\nenabled = true\n#end\n")
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
