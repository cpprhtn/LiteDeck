package app

import (
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// What the cache has to get right, and what it must not.
//
// The digest is the most expensive single thing this app runs — measured on a
// real server through the Command Log at 2.7 to 4.1 seconds a call, four calls
// in one session. Everything below is about paying that once.
func TestDigestCacheKeysOnConnectionAndMark(t *testing.T) {
	c := newDigestCache()
	want := DigestView{Since: 100, Readable: true}
	c.put("h", 7, 100, want)

	if got, ok := c.get("h", 7, 100); !ok || got.Since != want.Since {
		t.Error("같은 연결·같은 표시인데 캐시가 안 맞았다")
	}
	// A reconnect can be a rebooted machine, or a different one behind the
	// same name. Its journal is not the one that was counted.
	if _, ok := c.get("h", 8, 100); ok {
		t.Error("다시 연결했는데 옛 연결의 답을 줬다")
	}
	// Dismissing the strip moves the mark. A cache that ignored that would
	// answer the old question forever.
	if _, ok := c.get("h", 7, 200); ok {
		t.Error("표시가 옮겨졌는데 옛 답을 줬다")
	}
	if _, ok := c.get("other", 7, 100); ok {
		t.Error("다른 호스트의 답을 줬다")
	}
	c.forget("h")
	if _, ok := c.get("h", 7, 100); ok {
		t.Error("잊으라고 했는데 남아 있다")
	}
}

// countDigestRuns counts how many times the digest script actually ran.
func countDigestRuns(a *App) int {
	runs := 0
	for _, e := range a.CommandLog() {
		if !strings.Contains(e.Line, "journalctl --since") {
			continue
		}
		if e.Repeat > 0 {
			runs += e.Repeat
		} else {
			runs++
		}
	}
	return runs
}

// The binding, on a real server: four calls, one journal read.
//
// Skipped where the fixture user cannot read the journal, which is the case in
// the container today — HostDigest then returns before running anything and
// there is nothing to count. Left in rather than deleted because it is the test
// that would catch the cache being bypassed, and it becomes real on any host
// that can read.
func TestDigestIsReadOncePerMark(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)

	info, err := a.DetectHost("fixture")
	if err != nil {
		t.Fatalf("DetectHost: %v", err)
	}
	if !info.CanReadJournal {
		t.Skip("픽스처 사용자가 저널을 못 읽는다 — 셀 명령이 아예 안 돈다")
	}

	for i := 0; i < 4; i++ {
		if _, err := a.HostDigest("fixture"); err != nil {
			t.Fatalf("HostDigest #%d: %v", i, err)
		}
	}
	if runs := countDigestRuns(a); runs != 1 {
		t.Errorf("네 번 불렀는데 저널을 %d번 훑었다 — 한 번이어야 한다", runs)
	}
}
