package app

import "testing"

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
