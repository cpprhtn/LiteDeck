// Package windowspowershell drives a Windows server over SSH.
//
// It exists because none of the assumptions the Linux adapter is built on hold
// here. There is no /proc, no systemd, and — the part that shapes this whole
// package — no POSIX shell. Windows OpenSSH runs the client's command through
// whatever DefaultShell is configured, usually cmd.exe, whose quoting rules are
// neither POSIX nor PowerShell's.
//
// So nothing is quoted. Commands are sent as -EncodedCommand: the script is
// UTF-16LE, base64'd, and handed over as a single argv element made of nothing
// but [A-Za-z0-9+/=]. There is no metacharacter left to escape, which makes this
// stricter than internal/shellquote rather than a relaxation of it — the rule
// there is that user input must never be able to become a command, and base64
// enforces that by construction instead of by careful escaping.
//
// It also removes the cmd.exe/PowerShell distinction: -EncodedCommand is parsed
// by powershell.exe itself, so the outer shell only ever sees one flat token.
package windowspowershell

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Prelude is prepended to every script.
//
// Without it the console encodes output in the machine's OEM codepage — 949 on a
// Korean install, 437 on a US one — and every non-ASCII service description
// arrives as mojibake that no amount of parsing recovers. This is not
// hypothetical: the failure that started this adapter was a wall of CP949 error
// text with no indication of what it said.
//
// ProgressPreference is silenced because progress records are written to the
// stream as ANSI escape sequences and would corrupt otherwise-valid JSON.
const Prelude = `$OutputEncoding=[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false);` +
	`$ProgressPreference='SilentlyContinue';` +
	`$ErrorActionPreference='Stop';`

// Executable is the interpreter. Windows PowerShell 5.1 ships with every
// supported Windows version; pwsh (7+) has to be installed. Targeting 5.1 means
// the adapter works on a stock box, at the cost of some cmdlets and a
// ConvertTo-Json whose default depth is only 2 — hence the explicit -Depth
// everywhere.
const Executable = "powershell"

// Args returns the argv for running script on the server.
//
// The result is safe to pass to sshcore unchanged: every element is either a
// fixed flag or base64, so shellquote has nothing to do and cmd.exe has nothing
// to misinterpret.
func Args(script string) []string {
	return []string{
		"-NoProfile",      // ignore the user's profile: it can print banners into stdout
		"-NonInteractive", // never prompt; a prompt over SSH is a hang, not a question
		"-EncodedCommand", Encode(Prelude + script),
	}
}

// Encode converts a script to the UTF-16LE base64 that -EncodedCommand expects.
//
// PowerShell requires little-endian UTF-16 without a BOM. Passing UTF-8 produces
// a script of interleaved NUL bytes that fails in a way whose error message
// names neither the encoding nor the command.
func Encode(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// Decode reverses Encode. Only the tests and the capture tooling need it, but it
// lives here so the two halves cannot drift apart.
func Decode(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("windowspowershell: decode base64: %w", err)
	}
	if len(raw)%2 != 0 {
		return "", fmt.Errorf("windowspowershell: %d bytes is not whole UTF-16 code units", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units)), nil
}

// JSON wraps a pipeline so its result is always a JSON array.
//
// ConvertTo-Json emits a bare object rather than a one-element array when the
// pipeline yields exactly one item, and emits nothing at all for zero. Both are
// silent shape changes that turn into a parse error on the one server that
// happens to have a single Docker service or none — the sort of case a fixture
// invented by hand never contains.
//
// The array subexpression @(...) forces a collection before conversion, and
// depth is explicit because 5.1 defaults to 2 and truncates deeper structures
// with a warning nobody reads.
func JSON(pipeline string, depth int) string {
	if depth < 1 {
		depth = 4
	}
	return fmt.Sprintf("ConvertTo-Json -InputObject @(%s) -Depth %d -Compress", pipeline, depth)
}

// SingleQuote wraps s as a PowerShell single-quoted string literal.
//
// -EncodedCommand makes the *outer* shell inert, but it does nothing for text
// interpolated into the script itself. A service name arrives from the server or
// from the user and ends up inside `Stop-Service -Name <here>`, so this is the
// same front line internal/shellquote holds on the POSIX side, and it is held
// the same way: one quoting style, applied always, with no safe-character
// shortcut. Real service names contain spaces and parentheses — "Intel(R)
// Platform License Manager Service" is on the test box — so an unquoted form
// would be broken long before it was dangerous.
//
// Single quotes rather than double: PowerShell performs no expansion inside them,
// so $(...), $var, backticks and & are all literal. The only escape needed is a
// single quote itself, written twice.
//
// A NUL is rejected rather than encoded. It cannot appear in a service or process
// name, and the failure mode of passing it through — the argument silently ending
// early — is the one shellquote refuses for the same reason.
func SingleQuote(s string) (string, error) {
	if strings.ContainsRune(s, 0) {
		return "", fmt.Errorf("windowspowershell: argument contains NUL: %q", s)
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}

// MustSingleQuote is SingleQuote for values already known not to contain NUL —
// compile-time constants and values that have been through a validating parser.
// It panics rather than returning a quoted NUL, because a caller that ignores the
// error here is building a command out of unchecked input.
func MustSingleQuote(s string) string {
	q, err := SingleQuote(s)
	if err != nil {
		panic(err)
	}
	return q
}

// IsMissingCmdlet reports whether stderr is PowerShell saying the command does
// not exist, as opposed to the command existing and failing.
//
// The distinction decides whether a capability is absent — a normal answer, and
// a tab that explains itself — or genuinely broken. The message is localised, so
// matching on its text would work only in English; CommandNotFoundException is
// the stable part and appears in the fully qualified error id regardless of
// display language.
func IsMissingCmdlet(stderr []byte) bool {
	return strings.Contains(string(stderr), "CommandNotFoundException")
}

// IsAccessDenied reports whether stderr is a privilege failure.
//
// Windows has no sudo: either the SSH account is an administrator or the
// operation cannot be retried at all, so the UI must not offer the "retry as
// administrator" button it offers on Linux.
func IsAccessDenied(stderr []byte) bool {
	s := string(stderr)
	return strings.Contains(s, "UnauthorizedAccessException") ||
		strings.Contains(s, "AccessDenied") ||
		strings.Contains(s, "PermissionDenied") ||
		strings.Contains(s, "Access is denied") ||
		// CIM/WMI surfaces the same condition as an HRESULT rather than a name.
		strings.Contains(s, "0x80070005")
}
