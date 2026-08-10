package rollback

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecordAndRestoreRoundTrip(t *testing.T) {
	s := Open(t.TempDir())

	before := []byte("worker_processes 1;\n")
	e, err := s.Record("h1", "/etc/nginx/nginx.conf", ActionWrite, before, false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !e.Undoable() {
		t.Error("a small file must be undoable")
	}

	got, err := s.Contents(e.ID)
	if err != nil {
		t.Fatalf("contents: %v", err)
	}
	if !bytes.Equal(got, before) {
		t.Errorf("contents = %q, want %q", got, before)
	}
}

// Undoing a creation means removing the file, not writing bytes back. Getting
// this backwards would leave an empty file where there should be nothing.
func TestCreationRecordsNoContents(t *testing.T) {
	s := Open(t.TempDir())
	e, err := s.Record("h1", "/tmp/new.md", ActionWrite, nil, true)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !e.Created {
		t.Fatal("Created must be set")
	}
	body, err := s.Contents(e.ID)
	if err != nil || body != nil {
		t.Errorf("a creation has nothing to restore: %v %q", err, body)
	}
}

// A file too big to keep is still listed. "This changed and cannot be undone"
// is information; silently omitting it would make the list a lie.
func TestOversizedChangeIsRecordedButNotKept(t *testing.T) {
	s := Open(t.TempDir())
	e, err := s.Record("h1", "/var/log/huge.log", ActionWrite,
		bytes.Repeat([]byte("x"), MaxFileBytes+1), false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if e.Undoable() {
		t.Error("an oversized change must not claim to be undoable")
	}
	if len(s.List("h1")) != 1 {
		t.Error("it must still appear in the list")
	}
	if _, err := s.Contents(e.ID); err == nil {
		t.Error("restoring it should fail with a reason")
	}
}

func TestDeleteIsRecoverable(t *testing.T) {
	s := Open(t.TempDir())
	body := []byte("123\n")
	e, err := s.Record("h1", "/home/x/test.md", ActionDelete, body, false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := s.Contents(e.ID)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("a deleted file's contents must come back: %v %q", err, got)
	}
}

// The history must survive a restart, or "undo what happened overnight" does
// not work across the app being closed — which is exactly when it is needed.
func TestHistorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	first := Open(dir)
	e, err := first.Record("h1", "/etc/hosts", ActionWrite, []byte("127.0.0.1 x\n"), false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	second := Open(dir)
	list := second.List("h1")
	if len(list) != 1 || list[0].ID != e.ID {
		t.Fatalf("history did not survive: %v", list)
	}
	if body, err := second.Contents(e.ID); err != nil || !bytes.Equal(body, []byte("127.0.0.1 x\n")) {
		t.Errorf("contents did not survive: %v %q", err, body)
	}
}

// A runaway agent must not fill the disk the history is meant to protect.
func TestOldestAreDroppedPastTheLimit(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	for i := 0; i < MaxEntries+20; i++ {
		if _, err := s.Record("h1", "/tmp/f", ActionWrite, []byte("x"), false); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if got := len(s.List("h1")); got > MaxEntries {
		t.Errorf("kept %d entries, limit is %d", got, MaxEntries)
	}
	// The blobs of dropped entries go too, or the cap on entries would not cap
	// the disk.
	matches, _ := filepath.Glob(filepath.Join(dir, "ai-history", "*.blob"))
	if len(matches) > MaxEntries {
		t.Errorf("%d blobs left behind", len(matches))
	}
}

func TestListIsPerHostAndNewestFirst(t *testing.T) {
	s := Open(t.TempDir())
	s.Record("h1", "/a", ActionWrite, []byte("1"), false)
	s.Record("h2", "/b", ActionWrite, []byte("2"), false)
	s.Record("h1", "/c", ActionWrite, []byte("3"), false)

	only := s.List("h1")
	if len(only) != 2 {
		t.Fatalf("want 2 entries for h1, got %d", len(only))
	}
	if only[0].Path != "/c" {
		t.Errorf("newest first: got %s", only[0].Path)
	}
	if len(s.List("")) != 3 {
		t.Error("an empty host filter should return everything")
	}
}

func TestForgetRemovesEntryAndBlob(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	e, _ := s.Record("h1", "/a", ActionWrite, []byte("1"), false)
	if err := s.Forget(e.ID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if len(s.List("h1")) != 0 {
		t.Error("entry still listed")
	}
	if _, err := os.Stat(filepath.Join(dir, "ai-history", e.ID+".blob")); !os.IsNotExist(err) {
		t.Error("blob left behind")
	}
}

// The copies are somebody's config files. They must not be world-readable.
func TestSavedContentsAreNotReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	e, _ := s.Record("h1", "/etc/secret.conf", ActionWrite, []byte("password=hunter2"), false)

	for _, p := range []string{
		filepath.Join(dir, "ai-history"),
		filepath.Join(dir, "ai-history", e.ID+".blob"),
		filepath.Join(dir, "ai-history", "index.json"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v; these are the user's files", p, info.Mode().Perm())
		}
	}
}

// A history nobody can read must not stop the app from opening.
func TestCorruptIndexStartsEmptyRatherThanFailing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ai-history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ai-history", "index.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(dir)
	if len(s.List("")) != 0 {
		t.Error("a corrupt index should read as empty")
	}
	if _, err := s.Record("h1", "/a", ActionWrite, []byte("1"), false); err != nil {
		t.Errorf("it must still be usable: %v", err)
	}
}

func TestUnknownEntry(t *testing.T) {
	s := Open(t.TempDir())
	if _, err := s.Contents("nope"); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Errorf("want a clear error, got %v", err)
	}
}

// A copy nobody looked at within a day is not being kept for anybody. Somebody
// who set an agent working overnight checks the result the next morning.
func TestEntriesExpireAfterADay(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)

	old, _ := s.Record("h1", "/etc/old.conf", ActionWrite, []byte("old"), false)
	fresh, _ := s.Record("h1", "/etc/new.conf", ActionWrite, []byte("new"), false)

	// Age the first entry past the window, the way a reopened app would find it.
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].ID == old.ID {
			s.entries[i].At = time.Now().Add(-MaxAge - time.Minute)
		}
	}
	s.mu.Unlock()

	list := s.List("h1")
	if len(list) != 1 || list[0].ID != fresh.ID {
		t.Fatalf("expired entry should be gone, got %v", list)
	}
	// The bytes go with it, or the expiry would cap the list and not the disk.
	if _, err := os.Stat(filepath.Join(dir, "ai-history", old.ID+".blob")); !os.IsNotExist(err) {
		t.Error("the expired entry's copy is still on disk")
	}
}

// Expiry has to apply on read too: an app left open overnight would otherwise
// keep offering to restore something whose copy is already gone.
func TestExpiryAppliesWithoutAnyNewWrites(t *testing.T) {
	s := Open(t.TempDir())
	e, _ := s.Record("h1", "/etc/a.conf", ActionWrite, []byte("a"), false)

	s.mu.Lock()
	s.entries[0].At = time.Now().Add(-MaxAge - time.Second)
	s.mu.Unlock()

	if len(s.List("")) != 0 {
		t.Error("List must apply the age limit rather than only Record")
	}
	if _, err := s.Contents(e.ID); err == nil {
		t.Error("an expired entry should no longer be restorable")
	}
}
