package rollback

import (
	"bytes"
	"encoding/json"
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

// age rewrites an entry's timestamp on disk, standing in for time passing.
func age(t *testing.T, dir, id string, d time.Duration) {
	t.Helper()
	index := filepath.Join(dir, "ai-history", "index.json")
	data, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].At = time.Now().Add(-d)
			found = true
		}
	}
	if !found {
		t.Fatalf("no entry %s in the index", id)
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	if err := os.WriteFile(index, out, 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func blobs(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "ai-history", "*.blob"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return m
}

// The documented promise is that copies clear themselves after 24 hours. Until
// this, expiry only ran when an AI made another change or somebody opened the
// panel — both of which stop the moment a user is done with the integration,
// which is precisely when it matters. What is held is the previous contents of
// their server configuration.
func TestOpenExpiresCopiesWithoutTheFeatureBeingUsedAgain(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	old, err := s.Record("h1", "/etc/nginx/nginx.conf", ActionWrite, []byte("secret_key = hunter2\n"), false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(blobs(t, dir)) != 1 {
		t.Fatalf("the copy was not written")
	}

	age(t, dir, old.ID, MaxAge+time.Hour)

	// A fresh launch, and nothing else — no Record, no List.
	reopened := Open(dir)
	if got := blobs(t, dir); len(got) != 0 {
		t.Errorf("an expired copy survived a restart: %v", got)
	}
	// And it is gone from the index too, not merely from disk.
	if got := reopened.List(""); len(got) != 0 {
		t.Errorf("the index still names %d expired entries", len(got))
	}
}

// A copy that is still inside its window has to survive the same restart, or
// the undo list would be empty every morning.
func TestOpenKeepsCopiesInsideTheWindow(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	e, err := s.Record("h1", "/etc/hosts", ActionWrite, []byte("127.0.0.1 x\n"), false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	age(t, dir, e.ID, MaxAge/2)

	if got := Open(dir).List("h1"); len(got) != 1 {
		t.Fatalf("a copy inside the window was dropped: %v", got)
	}
	if len(blobs(t, dir)) != 1 {
		t.Error("its contents were removed")
	}
}

// prune deletes blobs but only trims the index in memory. Quitting after a read
// that expired something left index.json naming copies that were already gone,
// and the next launch offered to restore files it no longer had.
func TestListPersistsWhatItExpired(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	stale, err := s.Record("h1", "/etc/hosts", ActionWrite, []byte("old\n"), false)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	// Aged in memory rather than on disk, so this is about List and nothing
	// else. Going through Open would prune it there — which it now does, and
	// which would leave this test passing whatever List did. It is the
	// long-running window that matters here: an app open past midnight expires
	// an entry on a read, and quitting before the next write must not leave the
	// index naming a copy that is already gone.
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].ID == stale.ID {
			s.entries[i].At = time.Now().Add(-MaxAge - time.Hour)
		}
	}
	s.mu.Unlock()

	_ = s.List("")

	// What the index on disk says now is what the next launch will believe.
	data, err := os.ReadFile(filepath.Join(dir, "ai-history", "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	for _, e := range entries {
		if e.ID == stale.ID {
			t.Fatal("the index still names a copy that was deleted from disk")
		}
	}
}

// A corrupt index is survived by starting empty — which used to strand every
// copy it referenced on disk, past any expiry, with nothing left to look at them.
func TestOpenSweepsBlobsTheIndexDoesNotName(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if _, err := s.Record("h1", "/etc/hosts", ActionWrite, []byte("kept\n"), false); err != nil {
		t.Fatalf("record: %v", err)
	}

	orphan := filepath.Join(dir, "ai-history", "9999.blob")
	if err := os.WriteFile(orphan, []byte("nobody's copy\n"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	// Aged past the window. A fresh one is deliberately left alone — see the
	// test below.
	old := time.Now().Add(-MaxAge - time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatalf("age orphan: %v", err)
	}

	Open(dir)

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("an orphaned copy survived: %v", err)
	}
	// The one the index does name is untouched.
	if got := len(blobs(t, dir)); got != 1 {
		t.Errorf("%d blobs left, want the one that is still referenced", got)
	}
}

// Record writes the blob and *then* saves the index, so for a moment a good copy
// exists that no index on disk names yet. A second LiteDeck starting in that
// window must not delete the first one's undo copy — the sweep is the only thing
// here that acts on files it cannot account for.
func TestOpenLeavesAFreshUnnamedBlobAlone(t *testing.T) {
	dir := t.TempDir()
	Open(dir) // creates the directory

	// Exactly the state Record is in between its two writes.
	inFlight := filepath.Join(dir, "ai-history", "4242.blob")
	if err := os.WriteFile(inFlight, []byte("another instance is mid-Record\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	Open(dir)

	if _, err := os.Stat(inFlight); err != nil {
		t.Errorf("a copy that was still being written was swept: %v", err)
	}
}
