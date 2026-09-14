package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What a non-English server actually sends back.
//
// Review item 1-31 was marked "conjecture": nobody had a server in another
// language to try. These fixtures are that server — Ubuntu 24.04 with
// language-pack-ko and language-pack-de installed, which is what a Korean or
// German install has. testdata/golden/locale/provenance.txt records how.
//
// The point is not that the parsers should cope with Korean. It is that they
// cannot, which is why every script pins LC_ALL=C. These tests hold that in
// place from both ends: the hazard is real, and the pin is where it has to be.
func localeGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "locale", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return string(b)
}

// ufw is translated in full, and the failure it produces is the forbidden kind.
//
//	Status: active  →  상태: 활성  →  Status: Aktiv
//
// ParseUfwStatus keys on the literal "Status:", so the Korean and German
// readings come back Active=false — a live firewall reported as switched off.
// Not "could not read", which the screen knows how to say. Off.
func TestUfwStatusIsTranslated(t *testing.T) {
	if got := ParseUfwStatus(localeGolden(t, "ufw-status-c.txt")); !got.Active {
		t.Fatal("the English capture does not even read as active — the fixture is wrong")
	}
	for _, name := range []string{"ufw-status-ko.txt", "ufw-status-de.txt"} {
		raw := localeGolden(t, name)
		if strings.Contains(raw, "Status: active") {
			t.Errorf("%s is not actually translated; it cannot stand for the hazard", name)
		}
		if got := ParseUfwStatus(raw); got.Active {
			t.Errorf("%s now reads as active — if ParseUfwStatus learned other "+
				"languages, this test should be inverted, not deleted", name)
		}
	}
}

// Every script that parses English output has to say so itself.
//
// sshcore asks for LC_ALL over the SSH protocol as well, but that only works
// where sshd lists the variable in AcceptEnv, which the stock config does not.
// The script is the part that always arrives.
func TestEveryParsingScriptPinsTheLocale(t *testing.T) {
	for name, script := range map[string]string{
		"SecurityScript":  SecurityScript,
		"AttackersScript": AttackersScript,
		"FailuresScript":  FailuresScript,
		"DigestScript":    DigestScript,
		"LoginsScript":    LoginsScript,
	} {
		if !strings.HasPrefix(script, "LC_ALL=C; export LC_ALL\n") {
			t.Errorf("%s does not open by pinning the locale", name)
		}
	}
}

// `last -F` is the one the review got wrong, and it is worth a test rather than
// a note: the two captures are byte-identical.
//
// util-linux prints this format through ctime, which POSIX pins to the C locale,
// so the weekday and month never translate. What does translate is the "wtmp
// begins" trailer at the bottom, and ParseLast does not read it. If a future
// util-linux switches the data lines to strftime this test is how anybody finds
// out.
func TestLastFullTimeIsNotTranslated(t *testing.T) {
	c := localeGolden(t, "last-F-c.txt")
	ko := localeGolden(t, "last-F-ko.txt")
	if c != ko {
		t.Errorf("`last -F` now differs between locales:\n--- C\n%s\n--- ko_KR\n%s", c, ko)
	}
	got := ParseLast(ko, time.UTC)
	if len(got) == 0 {
		t.Fatal("ParseLast read nothing out of the Korean-locale capture")
	}
	users := map[string]bool{}
	for _, l := range got {
		users[l.User] = true
	}
	for _, want := range []string{"litedeck", "deploy"} {
		if !users[want] {
			t.Errorf("%s is missing from %v", want, users)
		}
	}
}
