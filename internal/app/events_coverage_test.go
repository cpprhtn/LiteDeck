package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// An event nobody listens for is a message the user never gets.
//
// This is the same hole the i18n coverage tests exist for, one layer over:
// there, a string with no translation falls back to Korean and someone
// eventually sees it. Here nothing falls back. `log:warning` was emitted from
// five places in Go — a rollback copy that could not be written, a token that
// could not be made — and no component had ever subscribed, so every one of
// those warnings was discarded by the runtime without a trace. Nothing failed,
// nothing logged, and the code reads as though it reported the problem.
//
// Dynamic names are emitted as a literal prefix plus an id (`"log:data:"+id`),
// and the frontend subscribes with a template literal carrying the same prefix,
// so matching on the prefix is what both sides actually share.
func TestEveryEmittedEventIsListenedFor(t *testing.T) {
	emitted := emittedEvents(t)
	if len(emitted) < 5 {
		t.Fatalf("only found %d emitted events; the scan is not looking where it should", len(emitted))
	}
	front := frontendSources(t)

	var orphans []string
	for _, name := range emitted {
		if !strings.Contains(front, name) {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		t.Errorf("emitted with nothing listening: %s\n"+
			"Either subscribe to it in frontend/src, or stop emitting it. An event with no "+
			"listener is silently dropped, and in server mode it is also serialised and pushed "+
			"to every browser for nobody to read.", strings.Join(orphans, ", "))
	}
}

var emitCall = regexp.MustCompile(`\.emit\(\s*"([^"]+)"`)

// emittedEvents collects the literal event names Go pushes to the frontend.
func emittedEvents(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	seen := map[string]bool{}
	err = filepath.Walk(filepath.Join(root, "internal"), func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range emitCall.FindAllStringSubmatch(string(src), -1) {
			seen[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// frontendSources is every frontend source file joined, for substring lookups.
func frontendSources(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "frontend", "src"))
	if err != nil {
		t.Fatalf("resolve frontend/src: %v", err)
	}
	var b strings.Builder
	err = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		if !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b.Write(src)
		return nil
	})
	if err != nil {
		t.Fatalf("walk frontend/src: %v", err)
	}
	return b.String()
}
