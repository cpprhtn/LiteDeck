package windowspowershell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenPath resolves a capture from testdata/windows/golden.
//
// These are real captures from a Windows 10 Pro box (Korean install) taken over
// SSH through the same -EncodedCommand transport the adapter uses. Nothing here
// was written by hand: CLIXML, the _x000D__x000A_ escapes and the localised
// messages are all things this package only learned about by looking.
func goldenPath(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "windows", "golden"))
	if err != nil {
		t.Fatalf("resolve golden dir: %v", err)
	}
	p := filepath.Join(root, name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("golden file missing: %v", err)
	}
	return p
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath(t, name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return b
}

func TestDecodeCLIXMLMissingCmdlet(t *testing.T) {
	raw := readGolden(t, "missing-cmdlet.err")

	if !IsCLIXML(raw) {
		t.Fatal("real PowerShell stderr not recognised as CLIXML")
	}
	got := ErrorText(raw)

	// The sentence a user needs, in the display language of the server.
	if !strings.Contains(got, "Get-NoSuchCmdlet") {
		t.Errorf("decoded text lost the command name:\n%s", got)
	}
	if !strings.Contains(got, "인식되지 않습니다") {
		t.Errorf("decoded text lost the localised message:\n%s", got)
	}
	// The envelope must be gone. Showing any of this to a user is worse than
	// showing nothing, because it reads as a crash in LiteDeck rather than a
	// missing command on the server.
	for _, junk := range []string{
		"#< CLIXML", "<Objs", "<S S=", "</S>", "_x000D_", "_x000A_",
		"schemas.microsoft.com",
	} {
		if strings.Contains(got, junk) {
			t.Errorf("decoded text still contains %q:\n%s", junk, got)
		}
	}
	// The caret ruler and the script offset point into a command line the user
	// never wrote — ours, with the prelude prepended.
	if strings.Contains(got, "위치 줄:") {
		t.Errorf("kept the generated-script offset:\n%s", got)
	}
	if strings.Contains(got, "~~~~") {
		t.Errorf("kept the caret ruler:\n%s", got)
	}

	// And it must still classify.
	if !IsMissingCmdlet(raw) {
		t.Error("IsMissingCmdlet false for a real CommandNotFoundException")
	}
	if IsAccessDenied(raw) {
		t.Error("a missing cmdlet classified as a permission failure")
	}
}

func TestDecodeCLIXMLAccessFailure(t *testing.T) {
	raw := readGolden(t, "access-denied.err")

	got := ErrorText(raw)
	if got == "" {
		t.Fatal("decoded to nothing; the user would see a silent failure")
	}
	if strings.Contains(got, "<S S=") || strings.Contains(got, "_x000D_") {
		t.Errorf("envelope survived:\n%s", got)
	}
	// Reading SAM fails as a sharing violation rather than an ACL denial, which
	// is worth pinning: it is why this capture cannot be used as the fixture for
	// IsAccessDenied. Detecting the two apart needs a real ACL failure, and
	// nothing on a box where the SSH account is an administrator produces one.
	if !strings.Contains(got, "SAM") {
		t.Errorf("decoded text lost the path:\n%s", got)
	}
	if IsMissingCmdlet(raw) {
		t.Error("an IO failure classified as a missing cmdlet")
	}
}

// TestDecodeCLIXMLNonUTF8 covers the docker captures, which were taken without
// the encoding prelude. Their CLIXML carries progress records in the OEM
// codepage, so the document is not valid UTF-8 and cannot be parsed.
//
// The bytes are returned unchanged rather than dropped. Undecodable output is
// exactly when the raw form is worth showing — and this fixture is also the
// evidence that the prelude is load-bearing, since the same box through the same
// transport produces clean UTF-8 with it.
func TestDecodeCLIXMLNonUTF8(t *testing.T) {
	raw := readGolden(t, "docker-version.err")

	if !IsCLIXML(raw) {
		t.Fatal("expected a CLIXML envelope")
	}
	got := DecodeCLIXML(raw)
	if got != string(raw) {
		t.Errorf("non-UTF-8 CLIXML was altered; raw bytes should survive\ngot %d bytes, want %d",
			len(got), len(raw))
	}
}

func TestDecodeCLIXMLPassesThroughPlainText(t *testing.T) {
	for _, s := range []string{
		"",
		"Stop-Service : Service 'nosuch' cannot be found.",
		"ordinary\nmulti-line\ntext",
	} {
		if got := DecodeCLIXML([]byte(s)); got != s {
			t.Errorf("plain text %q became %q", s, got)
		}
	}
}

func TestUnescapeCLIXML(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"line_x000D__x000A_", "line\r\n"},
		{"tab_x0009_here", "tab\there"},
		{"no escapes", "no escapes"},
		// Four hex digits are required. Neither of these is an escape, so both
		// must survive untouched — a decoder that accepts a short form would
		// corrupt any service description that happens to contain _x41_.
		{"_xZZZZ_", "_xZZZZ_"},
		{"_x41_", "_x41_"},
	} {
		if got := unescapeCLIXML(tc.in); got != tc.want {
			t.Errorf("unescapeCLIXML(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGoldenFilesAreAnonymised is a release guard, not a parser test.
//
// The captures come from a personal machine and live in a public repository. The
// capture script anonymises them; this fails if a future capture lands with the
// identities still in it.
func TestGoldenFilesAreAnonymised(t *testing.T) {
	dir := filepath.Dir(goldenPath(t, "provenance.txt"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read golden dir: %v", err)
	}

	// The placeholders the script substitutes. Anything still shaped like a real
	// machine name or a private address means it did not run.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		s := string(b)
		for _, leak := range []string{"192.168.", "10.0.0.", "172.16."} {
			if strings.Contains(s, `"`+leak) {
				t.Errorf("%s contains a private address starting %q — re-run capture.sh",
					e.Name(), leak)
			}
		}
	}

	// And the substitution must actually be present, so an empty capture cannot
	// pass this test by having nothing in it.
	who := string(readGolden(t, "whoami.out"))
	if !strings.Contains(who, "DESKTOP-EXAMPLE") || !strings.Contains(who, "TESTUSER") {
		t.Error("whoami.out is missing the anonymised placeholders; capture.sh did not sanitise")
	}
}
