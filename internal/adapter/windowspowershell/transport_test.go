package windowspowershell

import (
	"strings"
	"testing"
)

// TestEncodeMatchesPowerShell pins the encoding against fixed values, so a
// change in endianness or an accidental BOM fails here rather than on a server.
//
// The expected strings were produced by an implementation that shares no code
// with Encode — python3's `base64.b64encode(s.encode('utf-16-le'))`, which is
// also what [Text.Encoding]::Unicode.GetBytes gives PowerShell. Two of them were
// originally written from memory and were both wrong; the test caught it, which
// is the whole argument against inventing fixtures.
func TestEncodeMatchesPowerShell(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Get-Service", "RwBlAHQALQBTAGUAcgB2AGkAYwBlAA=="},
		{"1", "MQA="},
		{"", ""},
		// Non-BMP: an emoji is a surrogate pair in UTF-16 and a single rune in Go.
		// Encoding it per-rune rather than per-code-unit silently truncates.
		{"\U0001F600", "PdgA3g=="},
		// Hangul, because the box this adapter was written for is a Korean
		// install and its service descriptions are not ASCII.
		{"서비스", "HMFEvqTC"},
		{"서비스 설명", "HMFEvqTCIAAkwYW6"},
	} {
		if got := Encode(tc.in); got != tc.want {
			t.Errorf("Encode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for _, s := range []string{
		"Get-Service | ConvertTo-Json",
		"",
		"서비스 설명 with spaces\nand newlines\r\n",
		"\U0001F600 \U0001F4A9",
		`'single' "double" $var ` + "`backtick` & | > < ; %PATH%",
	} {
		got, err := Decode(Encode(s))
		if err != nil {
			t.Fatalf("Decode(Encode(%q)): %v", s, err)
		}
		if got != s {
			t.Errorf("round trip of %q gave %q", s, got)
		}
	}
}

// TestArgsAreInert is the security property this package exists for.
//
// The Linux side defends against injection by quoting correctly; here there is
// nothing to quote, because whatever the script contains comes out as base64.
// If this ever fails, a server-supplied or user-supplied string has found a way
// back into the command line.
func TestArgsAreInert(t *testing.T) {
	hostile := []string{
		`"; Remove-Item -Recurse C:\ ;"`,
		"' & del /f /s /q C:\\ & '",
		"$(Invoke-Expression 'calc')",
		"`n Stop-Computer",
		"%OS% %PATH% ^& echo pwned",
		"\x00truncated",
		strings.Repeat("A", 4096),
	}
	for _, h := range hostile {
		args := Args("Get-Service -Name " + h)
		if len(args) != 4 {
			t.Fatalf("Args returned %d elements, want 4: %q", len(args), args)
		}
		if args[0] != "-NoProfile" || args[1] != "-NonInteractive" || args[2] != "-EncodedCommand" {
			t.Fatalf("flags changed: %q", args[:3])
		}
		// The payload must be base64 and nothing else. Any character outside the
		// alphabet means something leaked through unencoded.
		for i, r := range args[3] {
			ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
				(r >= '0' && r <= '9') || r == '+' || r == '/' || r == '='
			if !ok {
				t.Fatalf("payload for %q has %q at %d — not base64", h, r, i)
			}
		}
		// And it must still be the script we meant to send.
		got, err := Decode(args[3])
		if err != nil {
			t.Fatalf("payload for %q does not decode: %v", h, err)
		}
		if !strings.Contains(got, h) {
			t.Errorf("payload for %q lost the argument", h)
		}
		if !strings.HasPrefix(got, Prelude) {
			t.Errorf("payload for %q is missing the encoding prelude", h)
		}
	}
}

// TestPreludeForcesUTF8 guards the fix for the mojibake that started this work.
// Dropping this line makes every non-ASCII string arrive in the OEM codepage,
// and the damage is not recoverable downstream.
func TestPreludeForcesUTF8(t *testing.T) {
	if !strings.Contains(Prelude, "UTF8Encoding") {
		t.Error("prelude no longer forces UTF-8; non-ASCII output will arrive as mojibake")
	}
	if !strings.Contains(Prelude, "SilentlyContinue") {
		t.Error("prelude no longer silences progress records; they corrupt JSON output")
	}
}

// TestJSONForcesArray covers the shape change that catches out every PowerShell
// JSON consumer once: one result is an object, zero results is empty output, and
// only two-or-more is the array the parser expects.
func TestJSONForcesArray(t *testing.T) {
	got := JSON("Get-Service", 3)
	if !strings.Contains(got, "@(Get-Service)") {
		t.Errorf("pipeline not wrapped in an array subexpression: %q", got)
	}
	if !strings.Contains(got, "-Depth 3") {
		t.Errorf("depth not set explicitly: %q — 5.1 defaults to 2 and truncates", got)
	}
	// Depth must never be left implicit, even when the caller passes nonsense.
	if d := JSON("X", 0); !strings.Contains(d, "-Depth 4") {
		t.Errorf("zero depth not defaulted: %q", d)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stderr  string
		missing bool
		denied  bool
	}{
		{
			name:    "missing cmdlet, English",
			stderr:  "Get-NoSuch : The term 'Get-NoSuch' is not recognized...\n+ FullyQualifiedErrorId : CommandNotFoundException",
			missing: true,
		},
		{
			// The display text is localised; the error id is not. Matching the
			// message would make this adapter work only on English installs.
			name:    "missing cmdlet, localised message",
			stderr:  "용어가 cmdlet 이름으로 인식되지 않습니다\n+ FullyQualifiedErrorId : CommandNotFoundException,Microsoft.PowerShell",
			missing: true,
		},
		{
			name:   "access denied",
			stderr: "Get-WinEvent : Attempted to perform an unauthorized operation.\n+ CategoryInfo : UnauthorizedAccessException",
			denied: true,
		},
		{
			name:   "CIM HRESULT",
			stderr: "Exception from HRESULT: 0x80070005",
			denied: true,
		},
		{
			name:   "ordinary failure is neither",
			stderr: "Stop-Service : Service 'nosuch' cannot be found.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMissingCmdlet([]byte(tc.stderr)); got != tc.missing {
				t.Errorf("IsMissingCmdlet = %v, want %v", got, tc.missing)
			}
			if got := IsAccessDenied([]byte(tc.stderr)); got != tc.denied {
				t.Errorf("IsAccessDenied = %v, want %v", got, tc.denied)
			}
		})
	}
}
