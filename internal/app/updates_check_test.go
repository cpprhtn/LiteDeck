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

// "Could not ask" and "nothing newer" are different answers.
//
// Both leave Newer false, and a button that read them the same way would tell
// somebody they are up to date on the strength of a failed request.
func TestUnreachedIsNotTheSameAsUpToDate(t *testing.T) {
	var unreached UpdateInfo
	unreached.Checked = true
	if unreached.Reached {
		t.Error("묻지도 못했는데 답을 받았다고 한다")
	}
	if unreached.Newer {
		t.Error("실패가 「새 버전 없음」으로 굳었다")
	}
}
