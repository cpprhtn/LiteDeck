package windowspowershell

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSingleQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"nginx", "'nginx'"},
		{"", "''"},
		// Real names from the test box: spaces and parentheses are ordinary here.
		{"Intel(R) Platform License Manager Service", "'Intel(R) Platform License Manager Service'"},
		{"AarSvc_30c48f", "'AarSvc_30c48f'"},
		// The only escape single-quoting needs.
		{"it's", "'it''s'"},
		{"''", "''''''"},
		// Everything below is inert inside single quotes and must survive verbatim.
		{"$var", "'$var'"},
		{"$(calc)", "'$(calc)'"},
		{"`n", "'`n'"},
		{"a & b | c > d", "'a & b | c > d'"},
		{"%PATH%", "'%PATH%'"},
		{`C:\Windows\System32`, `'C:\Windows\System32'`},
		{"서비스", "'서비스'"},
	} {
		got, err := SingleQuote(tc.in)
		if err != nil {
			t.Errorf("SingleQuote(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("SingleQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSingleQuoteRejectsNUL mirrors the rule internal/shellquote holds: a NUL is
// refused, not encoded, because passing it through truncates the argument
// silently and a silently shortened argument is a vulnerability rather than a
// cosmetic problem.
func TestSingleQuoteRejectsNUL(t *testing.T) {
	if _, err := SingleQuote("svc\x00rest"); err == nil {
		t.Error("SingleQuote accepted a NUL")
	}
	defer func() {
		if recover() == nil {
			t.Error("MustSingleQuote did not panic on a NUL")
		}
	}()
	MustSingleQuote("svc\x00rest")
}

// hostileArgs is what a quoting bug would let through. Kept in one place so the
// unit test and the live PowerShell test below cover the same set.
var hostileArgs = []string{
	`'; Stop-Computer -Force; '`,
	`'; Remove-Item -Recurse -Force C:\; '`,
	`' ; Get-Content C:\Windows\System32\config\SAM ; '`,
	"$(Invoke-Expression 'calc')",
	"$(1+1)",
	"`n Stop-Service sshd",
	"'+$(1)+'",
	`"; echo pwned; "`,
	"a'b\"c`d$e%f&g|h",
	"%OS% ^& echo pwned",
	"'''''''",
	strings.Repeat("'", 64),
	"서비스'; echo 침입; '",
	"\t\r\n mixed whitespace ",
	strings.Repeat("A", 2000),
}

// TestSingleQuoteAgainstRealPowerShell is the check that matters.
//
// Reasoning about a quoting rule is how quoting bugs are written; the POSIX side
// of this project is verified against a real /bin/sh for the same reason. Each
// hostile string is quoted, embedded in a script, and sent to a real Windows box,
// which must echo it back byte for byte. Anything that executes instead of
// echoing shows up as a mismatch.
//
// Skipped unless LITEDECK_TEST_WIN_SSH names a reachable host, because it needs a
// Windows server and CI has none. Set it to an ssh alias:
//
//	LITEDECK_TEST_WIN_SSH=litedeck-win go test ./internal/adapter/windowspowershell/ -run RealPowerShell -v
func TestSingleQuoteAgainstRealPowerShell(t *testing.T) {
	host := os.Getenv("LITEDECK_TEST_WIN_SSH")
	if host == "" {
		t.Skip("LITEDECK_TEST_WIN_SSH not set; needs a real Windows server")
	}
	if testing.Short() {
		t.Skip("needs a remote host")
	}

	for _, arg := range hostileArgs {
		quoted, err := SingleQuote(arg)
		if err != nil {
			t.Errorf("SingleQuote(%q): %v", arg, err)
			continue
		}

		// A delimiter around the value so trailing whitespace is visible in the
		// comparison rather than being eaten by the transport.
		script := "Write-Output ('<' + " + quoted + " + '>')"
		args := append([]string{host, Executable}, Args(script)...)

		out, err := exec.Command("ssh", args...).Output()
		if err != nil {
			t.Errorf("ssh for %q: %v", arg, err)
			continue
		}

		// PowerShell terminates lines with CRLF; the payload is what sits between
		// the delimiters.
		got := strings.TrimRight(string(out), "\r\n")
		start, end := strings.Index(got, "<"), strings.LastIndex(got, ">")
		if start < 0 || end < start {
			t.Errorf("no delimiters in reply for %q: %q", arg, got)
			continue
		}
		got = got[start+1 : end]

		// The transport normalises the line endings inside a here-string, so
		// compare with CR removed on both sides rather than pretending the
		// round trip is byte-exact about newlines.
		wantNorm := strings.ReplaceAll(arg, "\r\n", "\n")
		gotNorm := strings.ReplaceAll(got, "\r\n", "\n")
		if gotNorm != wantNorm {
			t.Errorf("round trip changed the value\n  sent: %q\n  got:  %q", arg, got)
		}
	}
}
