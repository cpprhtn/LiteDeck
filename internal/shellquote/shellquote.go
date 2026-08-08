// Package shellquote quotes arguments for safe interpolation into a POSIX
// shell command line.
//
// An SSH "exec" request hands the remote side a single command string, which
// the login shell then parses. Every argument LiteDeck sends to a server
// therefore passes through this package exactly once. Callers must never build
// a command string by concatenation — see the "argv 배열 기반, 셸 문자열 조립
// 금지" rule in the design doc (§3.2b).
//
// The quoting strategy is POSIX single-quoting: everything inside '...' is
// literal, with no expansion of any kind. The single quote itself is the one
// character that cannot appear inside such a run, so it is emitted by closing
// the quote, backslash-escaping the quote, and reopening:
//
//	foo'bar  ->  'foo'\''bar'
package shellquote

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNullByte is returned for arguments containing a NUL byte. A NUL cannot be
// carried through a shell command line at all: the remote shell would silently
// truncate the argument there, turning "safe\x00; rm -rf /" into something the
// caller never intended. Rejecting is the only correct behaviour.
var ErrNullByte = errors.New("shellquote: argument contains a NUL byte")

// safePunct lists the punctuation that needs no quoting in any shell context.
// Deliberately excluded: ~ (tilde expansion), ! (history expansion), # (comment
// introducer), and every metacharacter. When in doubt a character is left out —
// over-quoting is merely noisy, under-quoting is a vulnerability.
const safePunct = "@%+=:,./-_"

// Quote returns s as a single shell word that expands back to exactly s.
func Quote(s string) (string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return "", ErrNullByte
	}
	if s == "" {
		return "''", nil
	}
	if isSafe(s) {
		// Left bare so the Command Log (§4.6) stays readable and copy-pasteable.
		return s, nil
	}

	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b.WriteString(`'\''`)
			continue
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('\'')
	return b.String(), nil
}

// Join quotes each argument and joins them with single spaces, producing a
// command line ready to hand to an SSH exec request.
func Join(args ...string) (string, error) {
	parts := make([]string, len(args))
	for i, a := range args {
		q, err := Quote(a)
		if err != nil {
			return "", fmt.Errorf("arg %d: %w", i, err)
		}
		parts[i] = q
	}
	return strings.Join(parts, " "), nil
}

// isSafe reports whether s consists only of characters that carry no special
// meaning to a POSIX shell. Bytes >= 0x80 (UTF-8 continuation and lead bytes)
// are not in the safe set, so non-ASCII arguments get quoted — correct, if
// conservative, since a locale-dependent shell could otherwise reinterpret them.
func isSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			continue
		}
		if strings.IndexByte(safePunct, c) < 0 {
			return false
		}
	}
	return true
}
