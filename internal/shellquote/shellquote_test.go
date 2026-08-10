package shellquote

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// nastyCorpus is the shared seed set: inputs that have historically broken
// hand-rolled quoting. Reused as the fuzz seed corpus and as the input set for
// the real-shell verification below.
var nastyCorpus = []string{
	"",
	"simple",
	"with space",
	"it's",
	"'",
	"''",
	`'\''`,
	"$(rm -rf /)",
	"`whoami`",
	"${HOME}",
	"$HOME",
	"a;rm -rf /;b",
	"a && b",
	"a|b",
	"a\nb",
	"a\tb",
	"*",
	"?",
	"[a-z]",
	"~",
	"~root",
	"!!",
	"#comment",
	"--",
	"-rf",
	`\`,
	`a\b`,
	`"double"`,
	"한글 파일.txt",
	"file\nwith\nnewlines",
	"trailing ",
	" leading",
	strings.Repeat("'", 5),
	"a'\"b`c$d",
	"/etc/systemd/system/my-app.service",
}

func TestQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"simple", "simple"},
		{"/etc/os-release", "/etc/os-release"},
		{"a-b_c.d", "a-b_c.d"},
		{"with space", "'with space'"},
		{"it's", `'it'\''s'`},
		{"'", `''\'''`},
		{"$(x)", "'$(x)'"},
		{"~", "'~'"},
		{"!", "'!'"},
		{"#", "'#'"},
	}
	for _, c := range cases {
		got, err := Quote(c.in)
		if err != nil {
			t.Errorf("Quote(%q) returned error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteRejectsNullByte(t *testing.T) {
	if _, err := Quote("safe\x00; rm -rf /"); !errors.Is(err, ErrNullByte) {
		t.Errorf("Quote with NUL: err = %v, want ErrNullByte", err)
	}
	if _, err := Join("ok", "bad\x00"); !errors.Is(err, ErrNullByte) {
		t.Errorf("Join with NUL: err = %v, want ErrNullByte", err)
	}
}

func TestJoin(t *testing.T) {
	got, err := Join("systemctl", "restart", "--", "my app.service")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	const want = "systemctl restart -- 'my app.service'"
	if got != want {
		t.Errorf("Join = %q, want %q", got, want)
	}
}

// FuzzQuoteRoundTrip asserts that quoting is lossless: parsing the result with
// POSIX word rules must yield exactly the input back.
func FuzzQuoteRoundTrip(f *testing.F) {
	for _, s := range nastyCorpus {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, err := Quote(s)
		if err != nil {
			if !strings.ContainsRune(s, 0) {
				t.Fatalf("Quote(%q) errored without a NUL byte: %v", s, err)
			}
			return // NUL rejection is the documented contract.
		}
		got, err := unquoteWord(q)
		if err != nil {
			t.Fatalf("Quote(%q) = %q, which is not a single valid shell word: %v", s, q, err)
		}
		if got != s {
			t.Fatalf("round trip: Quote(%q) = %q, unquoted to %q", s, q, got)
		}
	})
}

// unquoteWord parses one POSIX shell word, written independently against the
// spec rather than by mirroring Quote. It errors on any unquoted metacharacter,
// so a quoting bug that lets a metacharacter escape is a test failure rather
// than a silent pass.
func unquoteWord(w string) (string, error) {
	// POSIX metacharacters, plus the characters special to pathname expansion
	// (*?[), comments (#) and tilde expansion (~). Not listed: = and % , which
	// are only special in assignment and job-control position respectively and
	// so are safe as bare arguments.
	const metachars = " \t\n|&;<>()$`\\\"'*?[#~"

	var b strings.Builder
	inSingle := false
	for i := 0; i < len(w); i++ {
		c := w[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
				continue
			}
			b.WriteByte(c)
			continue
		}
		switch {
		case c == '\'':
			inSingle = true
		case c == '\\':
			if i+1 >= len(w) {
				return "", errors.New("trailing backslash")
			}
			i++
			b.WriteByte(w[i])
		case strings.IndexByte(metachars, c) >= 0:
			return "", errors.New("unquoted metacharacter " + string(rune(c)))
		default:
			b.WriteByte(c)
		}
	}
	if inSingle {
		return "", errors.New("unterminated single quote")
	}
	return b.String(), nil
}

// TestQuoteAgainstRealShell is the ground truth. unquoteWord could in principle
// share a misconception with Quote; /bin/sh cannot.
func TestQuoteAgainstRealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}
	for _, in := range nastyCorpus {
		q, err := Quote(in)
		if err != nil {
			t.Errorf("Quote(%q): %v", in, err)
			continue
		}
		var out bytes.Buffer
		cmd := exec.Command(sh, "-c", "printf '%s' "+q)
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Errorf("sh rejected %q (from %q): %v", q, in, err)
			continue
		}
		if out.String() != in {
			t.Errorf("sh expanded %q (from %q) to %q", q, in, out.String())
		}
	}
}

// TestJoinArgvAgainstRealShell verifies that a joined command line splits into
// exactly the intended argv — the property that actually prevents injection.
// Restricted to newline-free inputs so printf output can be split on newlines.
func TestJoinArgvAgainstRealShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh available")
	}

	var args []string
	for _, s := range nastyCorpus {
		if s != "" && !strings.ContainsAny(s, "\n") {
			args = append(args, s)
		}
	}

	line, err := Join(append([]string{"printf", `%s\n`}, args...)...)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	var out bytes.Buffer
	cmd := exec.Command(sh, "-c", line)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("sh rejected %q: %v", line, err)
	}

	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(got) != len(args) {
		t.Fatalf("argv count = %d, want %d (got %q)", len(got), len(args), got)
	}
	for i := range args {
		if got[i] != args[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], args[i])
		}
	}
}
