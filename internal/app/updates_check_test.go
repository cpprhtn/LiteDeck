package app

import "testing"

// Version comparison, and what happens to anything it cannot read.
//
// Not a semver library on purpose. The versions compared here are the ones this
// repository publishes — three numbers — and anything else must come back as
// "not newer", which shows nothing rather than a wrong claim.
func TestIsNewerOnlyClaimsWhatItCanRead(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"1.9.0", "1.8.0", true},
		{"1.8.1", "1.8.0", true},
		{"2.0.0", "1.9.9", true},
		{"1.8.0", "1.8.0", false},
		{"1.7.9", "1.8.0", false},
		// Ten is greater than nine, which a string comparison would get wrong.
		{"1.10.0", "1.9.0", true},
		// Unreadable on either side is not an upgrade.
		{"1.9", "1.8.0", false},
		{"v1.9.0", "1.8.0", false},
		{"1.9.0-rc1", "1.8.0", false},
		{"", "1.8.0", false},
		{"1.9.0", "", false},
	} {
		if got := isNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, 기대 %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

// Off until asked for, and silent while off.
//
// This is the only request this app makes to anywhere the user did not name.
func TestUpdateCheckIsOffUntilSwitchedOn(t *testing.T) {
	a := &App{}
	if got := a.CheckForUpdate(); got.Checked {
		t.Error("설정도 없는데 확인했다고 한다")
	}
	if a.CheckUpdatesEnabled() {
		t.Error("기본값이 켜짐이다 — 밖으로 나가는 요청은 물어보고 해야 한다")
	}
}
