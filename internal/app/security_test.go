package app

import (
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/config"
)

// The unlock is the one password this app keeps in memory, so what ends it
// matters more than what starts it.
func TestUnlockDiesWithTheConnection(t *testing.T) {
	u := newSudoUnlock()
	u.put("h", 4, "hunter2")

	if got, ok := u.get("h", 4); !ok || got != "hunter2" {
		t.Error("같은 연결인데 잠금이 안 열렸다")
	}
	// A reconnect can be a different machine behind the same name. Handing it
	// a password given for the last one is the failure worth guarding.
	if _, ok := u.get("h", 5); ok {
		t.Error("다시 연결했는데 옛 연결의 비밀번호를 내줬다")
	}
	if _, ok := u.get("other", 4); ok {
		t.Error("다른 호스트에 비밀번호를 내줬다")
	}
	u.forget("h")
	if _, ok := u.get("h", 4); ok {
		t.Error("연결을 끊었는데 비밀번호가 남아 있다")
	}
}

// Disconnecting must *drop* it, not merely stop matching on it.
//
// The generation check alone makes a stale password unusable, and a test that
// only asked HostSecurity would pass without the forget — it did, when this was
// written the other way round. What forget adds is that a host left
// disconnected stops holding a sudo password in memory at all, and the only way
// to see that is to look.
func TestDisconnectDropsTheHeldPassword(t *testing.T) {
	a := connectedApp(t)
	a.unlocked.put("fixture", a.mgr.Generation("fixture"), "hunter2")

	if err := a.DisconnectHost("fixture"); err != nil {
		t.Fatalf("DisconnectHost: %v", err)
	}
	a.unlocked.mu.Lock()
	_, held := a.unlocked.byID["fixture"]
	a.unlocked.mu.Unlock()
	if held {
		t.Error("끊었는데 sudo 비밀번호가 메모리에 남아 있다")
	}
}

// The free half must survive a locked tab: a screen that shows nothing until a
// password arrives is a screen nobody opens, which is the whole shape of this
// feature.
func TestSecurityReadsTheFreeHalfWithoutElevation(t *testing.T) {
	a := connectedApp(t)

	view, err := a.HostSecurity("fixture", false)
	if err != nil {
		t.Fatalf("HostSecurity: %v", err)
	}
	if len(view.Units) == 0 {
		t.Error("유닛 상태를 하나도 못 읽었다 — 잠기지 않은 절반이 비어 있다")
	}
	if view.Unlocked {
		t.Error("잠금을 안 열었는데 열렸다고 한다")
	}
	// Never nil: the frontend maps over both.
	if view.Jails == nil {
		t.Error("Jails 가 nil 이다")
	}
	t.Logf("fixture: units=%d ufwFound=%v ufwEnabled=%v jails=%v canElevate=%v free=%v",
		len(view.Units), view.UfwConfFound, view.UfwEnabled, view.Jails,
		view.CanElevate, view.FreeElevation)
}

// Asking to unlock without a password held is refused rather than prompting
// from inside a read — the prompt belongs to UnlockSecurity, which the user
// invoked on purpose.
func TestElevatedReadWithoutUnlockSaysSoAndKeepsTheRest(t *testing.T) {
	a := connectedApp(t)
	info, err := a.DetectHost("fixture")
	if err != nil {
		t.Fatalf("DetectHost: %v", err)
	}
	if info.SudoNoPasswd {
		t.Skip("이 픽스처는 sudo 가 무암호라 잠금이 필요 없다")
	}

	view, err := a.HostSecurity("fixture", true)
	if err != nil {
		t.Fatalf("HostSecurity: %v", err)
	}
	if view.Unlocked {
		t.Error("비밀번호 없이 열렸다")
	}
	if view.RulesError == "" {
		t.Error("왜 못 읽었는지 말하지 않았다")
	}
	if len(view.Units) == 0 {
		t.Error("잠긴 절반이 실패하면서 열린 절반까지 가져갔다")
	}
}

// "No firewall" is a strong claim, and the first version of this screen made it
// on every ordinary Ubuntu server.
//
// nftables.service is a oneshot that Ubuntu ships disabled — ufw is the front
// end — so the unit is `dead` on a perfectly healthy machine. Reading that as
// "off" lit a red lamp on the normal case, and a panel that cries wolf stops
// being read. The kernel is what has the rules.
func TestVerdictDoesNotCryWolfOnAHealthyUbuntu(t *testing.T) {
	nftDead := adapter.SecurityUnit{
		Name: "nftables.service", Installed: true, Enabled: false, Active: false, SubState: "dead",
	}
	for _, tc := range []struct {
		name string
		view SecurityView
		want string
	}{
		{
			// The measured case: ufw on, its unit a spent oneshot, nftables.service
			// disabled, and the kernel doing the work underneath.
			name: "ufw 켜짐 · nftables 유닛은 죽어 있음",
			view: SecurityView{
				Units:        []adapter.SecurityUnit{nftDead},
				UfwConfFound: true, UfwEnabled: true,
				Kernel: adapter.KernelFirewall{NFTables: true, NFTablesRefs: 814},
			},
			want: VerdictOn,
		},
		{
			name: "ufw 꺼짐 · 커널도 놀고 있음",
			view: SecurityView{
				Units:        []adapter.SecurityUnit{nftDead},
				UfwConfFound: true, UfwEnabled: false,
				Kernel: adapter.KernelFirewall{NFTables: true},
			},
			want: VerdictNone,
		},
		{
			// Something is filtering and nothing names itself. Docker does this,
			// and so does a hand-written ruleset. Guessing either way is wrong.
			name: "전면부 없음 · 커널은 쓰이는 중",
			view: SecurityView{
				Kernel: adapter.KernelFirewall{NFTables: true, NFTablesRefs: 300},
			},
			want: VerdictUnknown,
		},
		{
			name: "firewalld",
			view: SecurityView{
				Units: []adapter.SecurityUnit{{Name: "firewalld.service", Installed: true, Active: true}},
			},
			want: VerdictOn,
		},
		{
			name: "아무것도 없음",
			view: SecurityView{},
			want: VerdictNone,
		},
	} {
		if got := firewallVerdict(tc.view); got != tc.want {
			t.Errorf("%s → %q, 기대 %q", tc.name, got, tc.want)
		}
	}
}

// An address is surprising exactly once, and reboots are not addresses.
func TestFirstSeenLoginsAreMarkedOnceAndNotByReboots(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)

	_, first, err := a.SecurityLogins("fixture")
	if err != nil {
		t.Fatalf("SecurityLogins: %v", err)
	}
	// `last` on the fixture may be empty; the marking logic is what is under
	// test and it is exercised through the store below either way.
	t.Logf("첫 조회에서 처음 보는 주소 %d개", len(first))

	a.RememberSecurityLogins("fixture", first)
	_, again, err := a.SecurityLogins("fixture")
	if err != nil {
		t.Fatalf("SecurityLogins 두 번째: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("한 번 보여준 주소가 다시 처음이라고 나왔다: %v", again)
	}

	// A boot record carries the kernel version where an address goes. Flagging
	// those would mark every reboot as a stranger logging in, which is the
	// fastest way to make the marker mean nothing.
	fresh := a.RememberSecurityLogins("fixture", []string{"203.0.113.77"})
	if len(fresh) != 1 {
		t.Errorf("새 주소를 못 알아봤다: %v", fresh)
	}
	if again := a.RememberSecurityLogins("fixture", []string{"203.0.113.77"}); len(again) != 0 {
		t.Errorf("이미 기억한 주소를 또 새것이라고 했다: %v", again)
	}
}

// "Could not read" and "nobody is knocking" are opposite answers, and an empty
// list reading as "safe" is the worst thing this feature can produce.
func TestAttackerAccessIsReportedApartFromTheList(t *testing.T) {
	a := connectedApp(t)
	view, err := a.HostSecurity("fixture", false)
	if err != nil {
		t.Fatalf("HostSecurity: %v", err)
	}
	if view.AttackersAccess == "" {
		t.Error("공격자 목록의 권한 상태를 말하지 않았다 — 빈 목록이 「안전함」으로 읽힌다")
	}
	info, err := a.DetectHost("fixture")
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case info.CanReadJournal:
		if view.AttackersAccess != EventAccessOK {
			t.Errorf("저널을 읽을 수 있는데 %q", view.AttackersAccess)
		}
	case info.HasSudo:
		if view.AttackersAccess != EventAccessNeedsSudo {
			t.Errorf("sudo 가 있는데 %q — 권한을 올릴 수 있다고 말해야 한다", view.AttackersAccess)
		}
		if len(view.Attackers) != 0 {
			t.Error("못 읽었는데 목록이 채워졌다")
		}
	}
	t.Logf("fixture: access=%q attackers=%d", view.AttackersAccess, len(view.Attackers))
}

// ufw's status and the kernel ruleset are not alternatives.
//
// They were chained with `||`, so on a host that has ufw the ruleset never ran
// — and the ruleset is where the blocked sets and the drop counters live. The
// result was a security tab that showed everything on a server without ufw and
// went half blank on one with it, which is the wrong way round: the box with a
// firewall is the one whose blocking there is something to say about.
//
// ufw status is a friendly summary of ufw's own rules. `nft list ruleset` is
// everything in the kernel, including the hand-made table and fail2ban's. A
// host with ufw needs both read.
func TestElevatedScriptReadsUfwAndTheRulesetBoth(t *testing.T) {
	script := securityRulesScript
	if !strings.Contains(script, "ufw status") {
		t.Error("ufw status 를 안 읽는다")
	}
	if !strings.Contains(script, "nft list ruleset") {
		t.Error("nft list ruleset 을 안 읽는다")
	}
	// The bug in one line: chaining them means only the first that works runs.
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "ufw status") && strings.Contains(line, "nft list ruleset") {
			t.Errorf("한 줄에 묶여 있다 — 둘 중 하나만 돈다: %q", line)
		}
	}
	for _, marker := range []string{"#rules", "#ruleset", "#bans"} {
		if !strings.Contains(script, marker) {
			t.Errorf("%s 구역이 없다", marker)
		}
	}
}
