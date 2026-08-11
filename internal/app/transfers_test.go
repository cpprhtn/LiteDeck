package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Walking a tree is the part of a directory transfer that costs the server:
// readdir after readdir over its sftp-server, before a single byte moves. It
// runs inside the job so the Cancel button reaches it — these pin that it
// actually looks.

func TestWalkStopsWhenCancelled(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.txt", "sub/b.txt", "sub/deeper/c.txt"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	files, total, err := walkLocalDir(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("walkLocalDir on a cancelled context = (%d files, %d bytes, %v), want context.Canceled",
			len(files), total, err)
	}
}

func TestWalkCountsWhatItFinds(t *testing.T) {
	root := t.TempDir()
	bodies := map[string]string{
		"a.txt":               "hello",
		"sub/b.txt":           "world!",
		"sub/deeper/c.txt":    "x",
		"sub/deeper/empty.md": "",
	}
	var want int64
	for rel, body := range bodies {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		want += int64(len(body))
	}
	// A symlink pointing outside the tree. Following it would copy something
	// the user never selected, so it is skipped and contributes nothing.
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("not part of the tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, total, err := walkLocalDir(context.Background(), root)
	if err != nil {
		t.Fatalf("walkLocalDir: %v", err)
	}
	if len(files) != len(bodies) {
		t.Errorf("walked %d files, want %d: %+v", len(files), len(bodies), files)
	}
	if total != want {
		t.Errorf("total = %d bytes, want %d", total, want)
	}
	for _, f := range files {
		if _, ok := bodies[f.Rel]; !ok {
			t.Errorf("unexpected entry %q — paths must be relative and forward-slashed", f.Rel)
		}
	}
}
