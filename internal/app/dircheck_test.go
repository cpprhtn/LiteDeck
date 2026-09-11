package app

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

// fakeDirs answers like a server would, and counts what it was asked.
type fakeDirs struct {
	tree     fstest.MapFS
	reads    int
	stats    int
	unlist   map[string]bool // directories that refuse to be listed
	listFail int
}

func (f *fakeDirs) ReadDir(p string) ([]fs.FileInfo, error) {
	f.reads++
	if f.unlist[p] {
		f.listFail++
		return nil, errors.New("permission denied")
	}
	entries, err := fs.ReadDir(f.tree, trimLead(p))
	if err != nil {
		return nil, err
	}
	out := make([]fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, fi)
	}
	return out, nil
}

func (f *fakeDirs) Stat(p string) (fs.FileInfo, error) {
	f.stats++
	return fs.Stat(f.tree, trimLead(p))
}

func trimLead(p string) string {
	if p == "/" {
		return "."
	}
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

func newFake() *fakeDirs {
	return &fakeDirs{
		tree: fstest.MapFS{
			"home/deploy/app/main.go":  {},
			"home/deploy/web/index.js": {},
			"home/deploy/docs/a.md":    {},
			"etc/nginx/nginx.conf":     {},
		},
		unlist: map[string]bool{},
	}
}

// Siblings share a parent, and one listing answers for all of them.
//
// This used to be one Stat per path, run one after another. On a real history
// that is around a hundred round trips before the panel can draw — 2.8 seconds
// on the server this was measured against, 4.2 on a Raspberry Pi.
func TestDirCheckAsksOncePerParent(t *testing.T) {
	f := newFake()
	d := newDirCheck(f)

	for _, p := range []string{
		"/home/deploy/app", "/home/deploy/web", "/home/deploy/docs",
		"/home/deploy/app", "/home/deploy/nope",
	} {
		d.exists(p)
	}
	if f.reads != 1 {
		t.Errorf("한 부모 아래 다섯 번 물었는데 목록을 %d번 읽었다 — 한 번이어야 한다", f.reads)
	}
	if f.stats != 0 {
		t.Errorf("목록으로 답할 수 있는데 Stat을 %d번 했다", f.stats)
	}
}

func TestDirCheckAnswersCorrectly(t *testing.T) {
	f := newFake()
	d := newDirCheck(f)
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/home/deploy/app", true},
		{"/home/deploy/nope", false},
		{"/etc/nginx", true},
		{"/home/deploy/app/main.go", false}, // a file is not a directory
		{"", false},
	} {
		if got := d.exists(tc.path); got != tc.want {
			t.Errorf("%q → %v, 기대 %v", tc.path, got, tc.want)
		}
	}
}

// A directory can be traversable without being listable — `--x` on a home is
// unusual but real. Falling back to Stat is what keeps those paths answerable.
func TestDirCheckFallsBackToStatWhenTheParentWillNotList(t *testing.T) {
	f := newFake()
	f.unlist["/home/deploy"] = true
	d := newDirCheck(f)

	if !d.exists("/home/deploy/app") {
		t.Error("목록을 못 읽는다고 있는 폴더를 없다고 했다")
	}
	if f.stats == 0 {
		t.Error("목록이 막혔는데 Stat으로 되묻지 않았다")
	}
	// And it must not keep retrying the listing for every sibling.
	d.exists("/home/deploy/web")
	d.exists("/home/deploy/docs")
	if f.listFail != 1 {
		t.Errorf("막힌 목록을 %d번 다시 읽으려 했다 — 한 번이어야 한다", f.listFail)
	}
}
