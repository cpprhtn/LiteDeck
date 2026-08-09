package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every message Go hands to the screen must have an English translation (§8).
//
// The catalogue is keyed by the Korean source text, so editing the Korean
// orphans its translation with nothing to show for it — the English build
// quietly falls back to Korean and the suite stays green. This walks the real
// syntax tree of every package, collects the literal handed to S, T and Errorf,
// and fails on one the catalogue does not carry.
//
// A parser rather than a regex: `i18n.S("…" + x)` and a call split across three
// lines both defeat a pattern match, and the point of this test is that it
// cannot be fooled by formatting.
func TestEveryMessageIsTranslated(t *testing.T) {
	calls := collectCalls(t)
	if len(calls) == 0 {
		t.Fatal("found no i18n calls in the repository — the walk is broken, not the catalogue")
	}

	var missing []string
	for key, where := range calls {
		if _, ok := english[key]; !ok {
			missing = append(missing, fmt.Sprintf("%s: %q", where, key))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d messages have no English translation:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// A catalogue entry nobody calls is worse than useless: it is evidence of
// coverage that does not exist. Two of these shipped — strings written for the
// frontend that ended up here — and looked exactly like wired-up messages.
func TestCatalogueHasNoOrphans(t *testing.T) {
	calls := collectCalls(t)
	var orphans []string
	for key := range english {
		if _, ok := calls[key]; !ok {
			orphans = append(orphans, strconv.Quote(key))
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("%d catalogue entries are never used:\n  %s",
			len(orphans), strings.Join(orphans, "\n  "))
	}
}

// The format verbs are part of the key, so a translation that drops one, adds
// one, or changes its type formats into %!s(MISSING) on a user's screen — in
// the language they chose, which makes it the one place a bug is least likely
// to be noticed during development.
func TestTranslationsKeepTheirVerbs(t *testing.T) {
	// %[1]s and friends are how English reorders arguments; the index is not
	// part of the verb's identity, only its type is.
	verb := regexp.MustCompile(`%(?:\[\d+\])?[a-zA-Z]`)
	strip := regexp.MustCompile(`\[\d+\]`)

	for ko, en := range english {
		want := map[string]int{}
		for _, v := range verb.FindAllString(ko, -1) {
			want[strip.ReplaceAllString(v, "")]++
		}
		got := map[string]int{}
		for _, v := range verb.FindAllString(en, -1) {
			got[strip.ReplaceAllString(v, "")]++
		}
		for v, n := range want {
			if got[v] != n {
				t.Errorf("%q\n  has %d %s, its translation has %d:\n  %q", ko, n, v, got[v], en)
			}
		}
		for v, n := range got {
			if want[v] != n {
				t.Errorf("%q\n  has no %s, but its translation has %d:\n  %q", ko, v, n, en)
			}
		}
	}
}

// collectCalls returns every string literal passed as the first argument to
// S, T or Errorf, mapped to the file:line it was found at.
func collectCalls(t *testing.T) map[string]string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The probe binary is a developer tool (§9): it prints its findings
			// for whoever is debugging a server, and is not shipped in the app.
			switch d.Name() {
			case "node_modules", "build", ".git", "litedeck-probe":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if !isTranslator(call.Fun) {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				// Something built at runtime. That cannot be looked up, so it
				// is a mistake rather than a message — say so here rather than
				// let it fall back to Korean on a user's screen.
				t.Errorf("%s: first argument is not a string literal",
					fset.Position(call.Pos()))
				return true
			}
			key, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: %v", fset.Position(lit.Pos()), err)
				return true
			}
			rel, _ := filepath.Rel(root, path)
			out[key] = fmt.Sprintf("%s:%d", rel, fset.Position(lit.Pos()).Line)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// isTranslator matches i18n.S/T/Errorf from another package and the bare
// S/T/Errorf this package calls internally.
func isTranslator(fun ast.Expr) bool {
	name := ""
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		if !ok || pkg.Name != "i18n" {
			return false
		}
		name = f.Sel.Name
	case *ast.Ident:
		name = f.Name
	default:
		return false
	}
	return name == "S" || name == "T" || name == "Errorf"
}
