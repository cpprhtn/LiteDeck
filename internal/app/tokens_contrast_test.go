package app

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Colour contrast is a build gate (§4.7-1).
//
// The palette this replaced failed WCAG on four counts and shipped that way for
// eleven releases: faint text at 2.57:1, the light accent — which is the colour
// of nearly every clickable word in the app — at 3.65, and every badge and error
// banner whose text sits on a tint of its own colour. None of it was noticed
// because nothing measured it, and a designer eyeballing a screen at full
// brightness will not catch 3.65 either.
//
// So it is measured here. This reads frontend/src/tokens.css, resolves the
// var() chain down to hex, and computes the real ratio. A colour that fails is
// a wrong colour, not a wrong test.
//
// # Why in Go, and why in this package
//
// Same reason as the i18n coverage test next door: the frontend has no test
// runner, and adding one to check eight colours would cost more than it saves.
// `go test ./...` is the gate CI already runs.

// contrastFloor is the WCAG AA ratio for body text.
//
// Not 3:1. Everything checked here is text somebody has to read — labels,
// values, timestamps, links. The 3:1 allowance is for large text (18.66px bold
// or 24px plain) and for the boundaries of controls, and this app has neither
// at these sizes: the type scale tops out at 20px.
const contrastFloor = 4.5

// tokenPair is one foreground that must stay readable on one background.
type tokenPair struct {
	fg, bg string
	// why says what breaks on screen when this pair goes under, so a failure
	// reads as a bug report rather than as a number.
	why string
}

// The grounds a piece of text can land on. --bg-hover and --bg-selected are
// translucent overlays on these, so they are covered by whichever ground they
// sit on rather than being checked separately.
var contrastPairs = []tokenPair{
	{"--fg", "--bg", "본문"},
	{"--fg", "--bg-sunken", "본문 (가라앉은 바탕)"},
	{"--fg", "--bg-raised", "본문 (패널)"},

	{"--fg-muted", "--bg", "보조 설명"},
	{"--fg-muted", "--bg-sunken", "보조 설명 (표 머리글)"},
	{"--fg-muted", "--bg-raised", "보조 설명 (패널)"},

	{"--fg-faint", "--bg", "차트 눈금·그룹 라벨"},
	{"--fg-faint", "--bg-sunken", "차트 눈금 (차트 바탕)"},
	{"--fg-faint", "--bg-raised", "패널 안의 흐린 글자"},

	{"--accent", "--bg", "링크·ghost 버튼"},
	{"--accent", "--bg-sunken", "링크 (툴바)"},
	{"--accent", "--bg-raised", "링크 (패널)"},

	{"--ok", "--bg", "정상 상태"},
	{"--ok", "--bg-raised", "정상 상태 (패널)"},
	{"--warn", "--bg", "주의 상태"},
	{"--warn", "--bg-raised", "주의 상태 (패널)"},
	{"--danger", "--bg", "위험 상태"},
	{"--danger", "--bg-raised", "위험 상태 (패널)"},

	// Text on a tint of its own colour. This is the family the old palette got
	// wrong everywhere at once, because the tint was mixed from the same hue as
	// the text and 18% of a colour is not far enough from it.
	{"--accent", "--accent-bg", "강조 배지"},
	{"--ok", "--ok-bg", "정상 배지"},
	{"--warn", "--warn-bg", "주의 배지"},
	{"--danger", "--danger-bg", "위험 배지·오류 배너"},

	{"--fg-on-accent", "--accent", "강조 버튼의 글자"},
}

func TestTokenContrast(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "frontend", "src", "tokens.css"))
	if err != nil {
		t.Fatalf("resolve tokens.css: %v", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tokens.css: %v", err)
	}

	light, dark := parseThemes(t, string(src))
	for _, theme := range []struct {
		name   string
		tokens map[string]string
	}{{"라이트", light}, {"다크", dark}} {
		for _, p := range contrastPairs {
			fg, ok := resolve(theme.tokens, p.fg)
			if !ok {
				t.Errorf("%s: %s를 못 찾았다 — 토큰 이름이 바뀌었으면 이 표도 고쳐야 한다", theme.name, p.fg)
				continue
			}
			bg, ok := resolve(theme.tokens, p.bg)
			if !ok {
				t.Errorf("%s: %s를 못 찾았다", theme.name, p.bg)
				continue
			}
			got := contrast(fg, bg)
			if got < contrastFloor {
				t.Errorf("%s %s(%s) on %s(%s) = %.2f:1 — 기준 %.1f 미달. 읽을 수 없게 되는 것: %s",
					theme.name, p.fg, fg, p.bg, bg, got, contrastFloor, p.why)
			}
		}
	}
}

// TestTokenContrastCatchesARegression proves the check bites.
//
// A gate nobody has seen fail is a gate nobody knows works. This feeds the
// previous palette's faint grey — the real value that shipped — through the
// same arithmetic and requires it to be rejected.
func TestTokenContrastCatchesARegression(t *testing.T) {
	if got := contrast("#a1a1a6", "#ffffff"); got >= contrastFloor {
		t.Fatalf("예전 --fg-faint가 %.2f로 통과했다 — 계산이 틀렸다", got)
	}
	if got := contrast("#0a84ff", "#ffffff"); got >= contrastFloor {
		t.Fatalf("예전 라이트 --accent가 %.2f로 통과했다 — 계산이 틀렸다", got)
	}
}

var (
	darkBlock = regexp.MustCompile(`(?s)@media \(prefers-color-scheme: dark\) \{\s*:root \{(.*?)\n  \}`)
	rootBlock = regexp.MustCompile("(?ms)^:root \\{(.*?)^\\}")
	declLine  = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+):\s*([^;]+);`)
)

// parseThemes returns the light tokens and the dark tokens, the latter being
// the light set with the dark block's overrides applied — which is how the
// cascade actually resolves them in the browser.
func parseThemes(t *testing.T, src string) (light, dark map[string]string) {
	t.Helper()

	root := rootBlock.FindStringSubmatch(src)
	if root == nil {
		t.Fatal("tokens.css에서 :root 블록을 못 읽었다 — 형식이 바뀌었다")
	}
	light = declarations(root[1])
	if len(light) < 20 {
		t.Fatalf("라이트 토큰이 %d 개뿐이다 — 파서가 블록을 잘못 잡았다", len(light))
	}

	over := darkBlock.FindStringSubmatch(src)
	if over == nil {
		t.Fatal("tokens.css에서 다크 블록을 못 읽었다")
	}
	dark = map[string]string{}
	for k, v := range light {
		dark[k] = v
	}
	overrides := declarations(over[1])
	if len(overrides) < 10 {
		t.Fatalf("다크 재정의가 %d 개뿐이다 — 파서가 블록을 잘못 잡았다", len(overrides))
	}
	for k, v := range overrides {
		dark[k] = v
	}
	return light, dark
}

func declarations(block string) map[string]string {
	out := map[string]string{}
	for _, m := range declLine.FindAllStringSubmatch(block, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

var varRef = regexp.MustCompile(`^var\((--[a-z0-9-]+)\)$`)

// resolve follows the var() chain to an opaque hex colour.
//
// Anything that is not one — a translucent overlay, a font stack, a length —
// comes back false. That is deliberate: a pair naming a token that is not an
// opaque colour is a mistake in the table above, not something to guess at.
func resolve(tokens map[string]string, name string) (string, bool) {
	seen := map[string]bool{}
	v, ok := tokens[name]
	for ok {
		if seen[name] {
			return "", false // a cycle; the file is broken and the test above will say so
		}
		seen[name] = true
		v = strings.TrimSpace(v)
		if m := varRef.FindStringSubmatch(v); m != nil {
			name = m[1]
			v, ok = tokens[name]
			continue
		}
		if len(v) == 7 && strings.HasPrefix(v, "#") {
			return strings.ToLower(v), true
		}
		return "", false
	}
	return "", false
}

// contrast is the WCAG 2.1 relative-luminance ratio.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(hex string) float64 {
	ch := [3]float64{}
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			panic(fmt.Sprintf("색이 아닌 값이 여기까지 왔다: %q", hex))
		}
		c := float64(n) / 255
		if c <= 0.04045 {
			ch[i] = c / 12.92
		} else {
			ch[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
}
