package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The translator, its two local aliases — used where a component already has a
// variable called `t` — and k(), the no-op that marks a Korean string a label
// table stores now and a render site translates later.
const translatorCall = `\b(?:t|tr|t2|k)\(\s*'((?:[^'\\]|\\.)*)'`

// Every Korean string the UI shows must have an English translation (§8).
//
// The catalogue is keyed by the Korean source text, which makes wrapping cheap
// but means editing the Korean silently orphans its translation — the English
// build would quietly fall back to Korean and nobody would notice until a user
// reported it. This reads the frontend source, pulls out every key actually
// passed to the translator, and fails on one the catalogue does not carry.
func TestEveryUIStringIsTranslated(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "frontend", "src"))
	if err != nil {
		t.Fatalf("resolve frontend/src: %v", err)
	}

	catalogue, err := os.ReadFile(filepath.Join(root, "locale-en.ts"))
	if err != nil {
		t.Fatalf("read locale-en.ts: %v", err)
	}
	have := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^  '((?:[^'\\]|\\.)*)':`).FindAllStringSubmatch(string(catalogue), -1) {
		have[m[1]] = true
	}
	if len(have) == 0 {
		t.Fatal("parsed no keys out of locale-en.ts — the catalogue format changed")
	}

	call := regexp.MustCompile(translatorCall)
	korean := regexp.MustCompile(`\p{Hangul}`)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read frontend/src: %v", err)
	}
	var missing []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == "i18n.ts" || name == "locale-en.ts" {
			continue
		}
		if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			key := m[1]
			// Only Korean keys need translating; a key that is already English
			// or a symbol passes through unchanged by design.
			if !korean.MatchString(key) || have[key] {
				continue
			}
			missing = append(missing, name+": "+key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d UI strings have no English translation:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// A placeholder the translation names but the Korean does not have is never
// filled in, so `{count}` where the key says `{n}` reaches the screen with the
// braces still on it — in English, which is the language least likely to be
// looked at while developing. This checks both directions: a name the value
// invents, and a name the key carries that the translation dropped.
func TestTranslationsKeepTheirPlaceholders(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "frontend", "src"))
	if err != nil {
		t.Fatalf("resolve frontend/src: %v", err)
	}
	catalogue, err := os.ReadFile(filepath.Join(root, "locale-en.ts"))
	if err != nil {
		t.Fatalf("read locale-en.ts: %v", err)
	}

	entry := regexp.MustCompile(`(?m)^  '((?:[^'\\]|\\.)*)': '((?:[^'\\]|\\.)*)',$`)
	// {n} and the plural form {n#one|many}, which names the same variable.
	name := regexp.MustCompile(`\{(\w+)[#}]`)

	rows := entry.FindAllStringSubmatch(string(catalogue), -1)
	// An entry this pattern cannot read is an entry it cannot check, and the
	// test would pass by skipping it. Every key must have matched a full row.
	keys := regexp.MustCompile(`(?m)^  '((?:[^'\\]|\\.)*)':`).FindAllString(string(catalogue), -1)
	if len(rows) != len(keys) || len(rows) == 0 {
		t.Fatalf("read %d of %d catalogue entries — the rest are in a shape this "+
			"test cannot check, so it would pass by ignoring them", len(rows), len(keys))
	}
	for _, row := range rows {
		ko, en := row[1], row[2]
		want := map[string]bool{}
		for _, m := range name.FindAllStringSubmatch(ko, -1) {
			want[m[1]] = true
		}
		got := map[string]bool{}
		for _, m := range name.FindAllStringSubmatch(en, -1) {
			got[m[1]] = true
		}
		for v := range got {
			if !want[v] {
				t.Errorf("%q\n  its translation uses {%s}, which the Korean does not have:\n  %q", ko, v, en)
			}
		}
		for v := range want {
			if !got[v] {
				t.Errorf("%q\n  its translation drops {%s}:\n  %q", ko, v, en)
			}
		}
	}
}

// The test above only sees strings already handed to the translator, so a file
// nobody ever converted passes it perfectly — which is exactly what happened to
// the file explorer, whose forty Korean lines sat unwrapped while the suite was
// green. This is the other half: every Korean run left in the source *outside*
// a translator call is a string the English build would show in Korean.
func TestNoKoreanEscapesTheTranslator(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "frontend", "src"))
	if err != nil {
		t.Fatalf("resolve frontend/src: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read frontend/src: %v", err)
	}

	call := regexp.MustCompile(translatorCall)
	// One Korean run and whatever punctuation and Latin sits inside it, so the
	// report shows a recognisable phrase rather than a single syllable.
	phrase := regexp.MustCompile(`[\p{Hangul}][\p{Hangul}\p{Latin}0-9 ·—,.()·…]*`)

	var stray []string
	for _, e := range entries {
		name := e.Name()
		// i18n.ts holds the language names and the catalogue is Korean by
		// definition — both are the machinery, not a message. Bench.tsx is the
		// render harness behind the --bench flag (§9): a developer tool that no
		// user reaches, written for whoever is reading the numbers.
		if e.IsDir() || name == "i18n.ts" || name == "locale-en.ts" || name == "Bench.tsx" {
			continue
		}
		if !strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".tsx") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Comments explain the Korean UI in Korean and always will.
		code := blankComments(string(src))
		// Blank out what is already translated, keeping the line count so the
		// reported line number still points at the source.
		code = call.ReplaceAllStringFunc(code, blankKeepingNewlines)

		for i, line := range strings.Split(code, "\n") {
			for _, m := range phrase.FindAllString(line, -1) {
				m = strings.TrimSpace(m)
				if m == "" {
					continue
				}
				stray = append(stray, fmt.Sprintf("%s:%d: %s", name, i+1, m))
			}
		}
	}
	sort.Strings(stray)
	if len(stray) > 0 {
		t.Errorf("%d Korean strings are not wrapped in t():\n  %s",
			len(stray), strings.Join(stray, "\n  "))
	}
}

// blankComments replaces the body of every // and /* */ comment with spaces,
// leaving newlines so line numbers survive.
//
// A regex cannot do this: `https://` inside a string literal looks exactly like
// the start of a comment, and JSX is full of them. So this walks the source
// tracking which of the three string forms it is inside.
func blankComments(src string) string {
	out := []byte(src)
	const (
		code = iota
		lineComment
		blockComment
	)
	state := code
	var quote byte // 0 when not in a string
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case state == lineComment:
			if c == '\n' {
				state = code
			} else {
				out[i] = ' '
			}
		case state == blockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
			} else if c != '\n' {
				out[i] = ' '
			}
		case quote != 0:
			if c == '\\' {
				i++ // an escaped quote does not close the string
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			out[i], out[i+1] = ' ', ' '
			i++
			state = lineComment
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			out[i], out[i+1] = ' ', ' '
			i++
			state = blockComment
		}
	}
	return string(out)
}

func blankKeepingNewlines(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		return ' '
	}, s)
}

// The parsers match against what the *server* prints, and a Korean Windows
// prints Korean. Those strings are not ours to translate: putting one in the
// catalogue would be harmless, but translating it in the source would break the
// parser silently. This pins the ones that exist today.
func TestServerOutputPatternsAreNotTreatedAsUI(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	for _, tc := range []struct{ file, pattern string }{
		// A Korean Windows prefixes error locations with this, an English one
		// with "At line:". Both are matched; neither is ours to change.
		{"internal/adapter/windowspowershell/clixml.go", "위치 줄:"},
		// Not a Korean string at all, and that is the point: command-not-found
		// is detected by exception class rather than by message text, which is
		// what makes it work in every server locale.
		{"internal/adapter/windowspowershell/transport.go", "CommandNotFoundException"},
	} {
		src, err := os.ReadFile(filepath.Join(root, tc.file))
		if err != nil {
			continue // the file moved; the other cases still carry the point
		}
		if !strings.Contains(string(src), tc.pattern) {
			t.Errorf("%s no longer contains %q — if it was translated, the parser "+
				"stopped matching what a Korean Windows actually prints",
				tc.file, tc.pattern)
		}
	}
}
