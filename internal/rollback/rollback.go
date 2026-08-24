// Package rollback keeps what an AI overwrote, so it can be put back.
//
// # Why this exists
//
// The approval dialog is the safety net for a change an AI asks for, and it
// only works while somebody is looking at it. The mode that people actually
// want — set it before going to bed and let an agent work — removes exactly
// that. Refusing to build the mode is not an option: a control people switch
// off protects nobody, and they would use a tool with no logging at all.
//
// So when prevention is off, the answer has to be recovery. Before any change
// an AI makes, the previous contents are copied here. Nothing is prevented;
// everything is undoable.
//
// # Why on the client
//
// Not on the server: LiteDeck installs nothing there and this would be an
// installation. The copies live beside the app's own config, which also means
// they survive the server they came from.
//
// # What this is not
//
// Not version control and not a backup. It holds what *this app's AI
// integration* changed, bounded, so a person can undo a night's work they did
// not watch. Anything more is git's job.
package rollback

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Action is what was done to the file.
const (
	ActionWrite  = "write"
	ActionDelete = "delete"
)

// Limits. Generous enough for a night of unattended work, bounded so a runaway
// agent cannot fill the disk it is supposed to be protecting.
const (
	// MaxAge is how long a copy is worth keeping.
	//
	// Somebody who sets an agent working overnight checks the result the next
	// day; a copy nobody looked at in that window is not being kept for them,
	// it is just an old copy of their config sitting on disk. This is a guard
	// against a night going wrong, not an archive.
	MaxAge = 24 * time.Hour

	MaxEntries = 300
	MaxBytes   = 64 << 20 // 64 MiB of saved contents
	// MaxFileBytes skips anything too large to be worth holding. A file this
	// size is not something a person restores from a list; it is git's problem.
	MaxFileBytes = 4 << 20
)

// Entry is one recorded change.
type Entry struct {
	ID     string    `json:"id"`
	HostID string    `json:"hostId"`
	Path   string    `json:"path"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	// Created is true when the change made a file that did not exist. Undoing
	// it means deleting the file, not restoring bytes.
	Created bool `json:"created"`
	// Bytes is the size of the saved contents, zero when Created.
	Bytes int64 `json:"bytes"`
	// TooLarge marks a change whose previous contents were not kept. Recorded
	// anyway so the list does not silently omit it — "this changed and cannot
	// be undone" is information.
	TooLarge bool `json:"tooLarge,omitempty"`
}

// Undoable reports whether this entry can actually be put back.
func (e Entry) Undoable() bool { return !e.TooLarge }

// Store is the on-disk history.
type Store struct {
	dir string

	mu      sync.Mutex
	seq     int
	entries []Entry
}

// Open loads (or starts) a history under dir.
//
// A corrupt index is not an error worth failing the app over: the copies are a
// convenience, and refusing to start because the undo list is unreadable would
// be the tail wagging the dog. It starts empty and the app runs.
func Open(dir string) *Store {
	s := &Store{dir: filepath.Join(dir, "ai-history")}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return s
	}
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		return s
	}
	var entries []Entry
	if json.Unmarshal(data, &entries) != nil {
		return s
	}
	s.entries = entries
	for _, e := range entries {
		if n, err := strconv.Atoi(e.ID); err == nil && n > s.seq {
			s.seq = n
		}
	}

	// Expiry has to happen here, not only when the feature is next used.
	//
	// prune otherwise runs on Record and on List — an AI making a change, or
	// somebody opening the panel. Both stop happening the moment a user decides
	// they are done with the integration, which is exactly when the promise in
	// the docs matters: copies clear themselves after 24 hours. What is being
	// held is the previous contents of somebody's server configuration, so a
	// copy that outlives its window is not a tidiness problem.
	if s.prune() {
		_ = s.save()
	}
	s.sweepOrphans()
	return s
}

// sweepOrphans removes blobs the index does not name.
//
// prune only knows about entries it can see, so a corrupt index — which this
// package deliberately survives by starting empty — would strand every copy it
// referenced on disk, past any expiry, with nothing that would ever look at
// them again.
func (s *Store) sweepOrphans() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}

	s.mu.Lock()
	known := make(map[string]bool, len(s.entries))
	for _, e := range s.entries {
		known[e.ID+".blob"] = true
	}
	s.mu.Unlock()

	for _, f := range entries {
		name := f.Name()
		if f.IsDir() || !strings.HasSuffix(name, ".blob") || known[name] {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, name))
	}
}

func (s *Store) indexPath() string { return filepath.Join(s.dir, "index.json") }
func (s *Store) blobPath(id string) string {
	return filepath.Join(s.dir, id+".blob")
}

// Record saves the state of a file before an AI changes it.
//
// before is what the file held; created says it did not exist. A failure here
// is returned but callers treat it as a warning: losing the ability to undo is
// bad, and refusing to make the change the user approved is worse.
func (s *Store) Record(hostID, path, action string, before []byte, created bool) (Entry, error) {
	s.mu.Lock()
	s.seq++
	e := Entry{
		ID:      strconv.Itoa(s.seq),
		HostID:  hostID,
		Path:    path,
		At:      time.Now(),
		Action:  action,
		Created: created,
		Bytes:   int64(len(before)),
	}
	if !created && len(before) > MaxFileBytes {
		e.TooLarge = true
		e.Bytes = int64(len(before))
	}
	s.entries = append(s.entries, e)
	s.mu.Unlock()

	if !created && !e.TooLarge {
		if err := os.WriteFile(s.blobPath(e.ID), before, 0o600); err != nil {
			return e, fmt.Errorf("rollback: save previous contents: %w", err)
		}
	}
	_ = s.prune()
	return e, s.save()
}

// List returns the history for one host, newest first. An empty hostID returns
// everything.
func (s *Store) List(hostID string) []Entry {
	// Enforced on read as well as on write: an app left open overnight would
	// otherwise keep showing entries that have already aged out.
	//
	// Written back when it removed something. Pruning deletes blobs from disk
	// but leaves the index in memory, so quitting after a read that expired
	// something left index.json naming copies that were already gone — and the
	// next launch loaded them back and offered to restore files it no longer
	// had.
	if s.prune() {
		_ = s.save()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if hostID == "" || e.HostID == hostID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// Get returns one entry.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Contents returns the saved previous contents of an entry.
func (s *Store) Contents(id string) ([]byte, error) {
	e, ok := s.Get(id)
	if !ok {
		return nil, fmt.Errorf("rollback: no entry %q", id)
	}
	if e.Created {
		return nil, nil // undoing a creation means deleting, not writing
	}
	if e.TooLarge {
		return nil, fmt.Errorf("rollback: %s was too large to keep a copy of", e.Path)
	}
	return os.ReadFile(s.blobPath(id))
}

// Forget drops an entry once it has been restored, so the list shows work still
// outstanding rather than a growing pile of things already dealt with.
func (s *Store) Forget(id string) error {
	s.mu.Lock()
	kept := s.entries[:0:0]
	for _, e := range s.entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	s.entries = kept
	s.mu.Unlock()

	_ = os.Remove(s.blobPath(id))
	return s.save()
}

// prune enforces the limits, oldest first. It reports whether anything was
// dropped, so callers know whether the index on disk needs rewriting.
func (s *Store) prune() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	before := len(s.entries)

	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].At.Before(s.entries[j].At) })

	// Age first, so an expired entry frees its bytes before the size cap has to
	// throw away something recent.
	cutoff := time.Now().Add(-MaxAge)
	fresh := s.entries[:0:0]
	for _, e := range s.entries {
		if e.At.Before(cutoff) {
			_ = os.Remove(s.blobPath(e.ID))
			continue
		}
		fresh = append(fresh, e)
	}
	s.entries = fresh

	var total int64
	for _, e := range s.entries {
		total += e.Bytes
	}
	// Drop from the oldest end until both limits are satisfied. Recent changes
	// are the ones somebody is about to want back.
	cut := 0
	for cut < len(s.entries) && (len(s.entries)-cut > MaxEntries || total > MaxBytes) {
		total -= s.entries[cut].Bytes
		_ = os.Remove(s.blobPath(s.entries[cut].ID))
		cut++
	}
	s.entries = s.entries[cut:]
	return len(s.entries) != before
}

func (s *Store) save() error {
	s.mu.Lock()
	data, err := json.MarshalIndent(s.entries, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("rollback: encode index: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, ".index-*.json")
	if err != nil {
		return fmt.Errorf("rollback: create temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("rollback: write index: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("rollback: chmod index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("rollback: close index: %w", err)
	}
	if err := os.Rename(name, s.indexPath()); err != nil {
		return fmt.Errorf("rollback: replace index: %w", err)
	}
	return nil
}
