package cfgsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What this machine remembers about the sync (§7.3).
//
// Five files under <app data>/sync/, none of them secret. The vault key is not
// here — it is in the OS credential store if the user said to remember it, and
// nowhere at all if they did not (§6.7 ①). The remote URL is here; a token for it
// is not.
//
// # Why the base record is kept whole
//
// The design said "base snapshot hash". A hash answers "did this change", which
// is enough to decide whether to push, and not enough for the merge: settling two
// changed records field by field needs the values that were there before (§8.3).
// So the whole base record is kept. It holds nothing that hosts.json and
// settings.json do not already hold in plain text on the same disk.

// File names under the sync directory.
const (
	configFile   = "config.json"
	stateFile    = "state.json"
	pendingFile  = "pending.json"
	keymapFile   = "local-keymap.json"
	historyFile  = "history.jsonl"
	repoSubdir   = "repo"
	historyLimit = 2000
)

// Auth kinds (§5.2).
const (
	// AuthDeployKey is a sync-only ed25519 key pair, registered as a deploy key
	// on the one repository. The recommended option: it reaches nothing else.
	AuthDeployKey = "deploy_key"
	// AuthAgent is the user's existing SSH key through their agent, for a bare
	// repository on their own server. Hidden on Windows, where the agent is a
	// named pipe and this code path does not reach it (§6.7 ⑥).
	AuthAgent = "agent"
	// AuthToken is HTTPS with a fine-grained token, kept in the credential store.
	AuthToken = "token"
)

// Config is sync/config.json: how to reach the repository, and nothing secret.
type Config struct {
	Enabled   bool   `json:"enabled"`
	RemoteURL string `json:"remoteUrl,omitempty"`
	AuthKind  string `json:"authKind,omitempty"`
	// DeviceID is this machine, as a UUID. It appears in commit messages (first
	// eight characters) and in records as updated_by, so it is deliberately not
	// the hostname: a commit log on somebody else's server should not say
	// "junwons-macbook".
	DeviceID string `json:"deviceId,omitempty"`
	// DeviceName is what the user sees in the pending list — "the MacBook
	// changed this". Local only; it never reaches the repository.
	DeviceName string `json:"deviceName,omitempty"`
	// Remember caches the vault key in the OS credential store. Forced off, and
	// shown as unavailable, where there is no credential store (§6.7 ①).
	Remember bool `json:"remember,omitempty"`
}

// RecordState is what this machine knows about one record.
type RecordState struct {
	// Rev is the highest revision this machine has seen. A lower one coming back
	// is a rewound repository (§6.4).
	Rev int64 `json:"rev"`
	// Base is the record as it was at the last successful sync, for the
	// three-way merge (§8.3).
	Base *Record `json:"base,omitempty"`
	// Applied is the policy this machine last put into settings.json from this
	// record — which is not the record's own policy where a loosening was
	// withheld (§6.2).
	//
	// It exists to answer one question at the next sync: is the difference
	// between settings.json and the repository this machine holding something
	// back, or somebody on this machine having changed their mind? Without it the
	// two are indistinguishable, and the stricter withheld value gets pushed as
	// though it were a decision — the other machine sees a tightening, applies it
	// automatically, and one person's choice for one server has quietly tightened
	// every machine they own. That happened; see TestLooseningWaitsForAPerson.
	Applied *RecordPolicy `json:"applied,omitempty"`
	// Dismissed remembers which pending decisions were answered with "keep
	// mine", by field and by the revision they arrived in. A later revision asks
	// again, because that is a new decision by somebody (§9.1).
	Dismissed map[string]int64 `json:"dismissed,omitempty"`
}

// State is sync/state.json.
type State struct {
	Head     string                  `json:"head,omitempty"`
	LastSync time.Time               `json:"lastSync,omitempty"`
	Records  map[string]*RecordState `json:"records,omitempty"`
}

// KeyLocation is where a key with some fingerprint lives on this machine (§3.3).
type KeyLocation struct {
	// Type is "file" or "agent".
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
}

// HistoryEntry is one line of sync/history.jsonl (§9.1).
//
// Local only, and it may name hosts: it is what the user reads to find out what a
// sync did, and "a record changed" is not that. It is never uploaded.
type HistoryEntry struct {
	At        time.Time  `json:"at"`
	Received  int        `json:"received,omitempty"`
	Sent      int        `json:"sent,omitempty"`
	Pending   int        `json:"pending,omitempty"`
	Warnings  []Warning  `json:"warnings,omitempty"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Store holds the local sync files.
//
// Every write is whole-file and atomic: a half-written state.json would make the
// next start disagree with itself about which revisions it has seen, and the
// answer to that question decides whether a rewound repository is noticed.
type Store struct {
	dir string

	mu      sync.Mutex
	cfg     Config
	state   State
	pending []PendingChange
	keymap  map[string]KeyLocation
}

// OpenStore loads the sync directory, creating it if needed.
//
// A file that will not parse is treated as absent rather than fatal, with one
// exception: state.json. Losing the revisions this machine has seen would make
// every record look new and quietly disable the rewind check, so a corrupt
// state.json is an error the caller has to see.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("cfgsync: the sync store needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cfgsync: create %s: %w", dir, err)
	}
	s := &Store{dir: dir, keymap: map[string]KeyLocation{}}
	s.state.Records = map[string]*RecordState{}

	_ = readJSON(filepath.Join(dir, configFile), &s.cfg)
	if err := readJSON(filepath.Join(dir, stateFile), &s.state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cfgsync: %s is unreadable, so this device cannot tell "+
			"which revisions it has already seen: %w", stateFile, err)
	}
	if s.state.Records == nil {
		s.state.Records = map[string]*RecordState{}
	}
	_ = readJSON(filepath.Join(dir, pendingFile), &s.pending)
	_ = readJSON(filepath.Join(dir, keymapFile), &s.keymap)
	if s.keymap == nil {
		s.keymap = map[string]KeyLocation{}
	}
	return s, nil
}

// RepoDir is where the git working copy lives.
func (s *Store) RepoDir() string { return filepath.Join(s.dir, repoSubdir) }

// Config returns a copy of the configuration.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// SetConfig replaces it.
func (s *Store) SetConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = c
	return writeJSON(filepath.Join(s.dir, configFile), c)
}

// State returns a copy of the sync state. The record states are copied too, so a
// caller cannot change what is persisted by holding on to a pointer.
func (s *Store) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := State{Head: s.state.Head, LastSync: s.state.LastSync,
		Records: make(map[string]*RecordState, len(s.state.Records))}
	for id, rs := range s.state.Records {
		copied := *rs
		if rs.Base != nil {
			base := *rs.Base
			copied.Base = &base
		}
		if rs.Applied != nil {
			applied := *rs.Applied
			copied.Applied = &applied
		}
		if rs.Dismissed != nil {
			copied.Dismissed = map[string]int64{}
			for k, v := range rs.Dismissed {
				copied.Dismissed[k] = v
			}
		}
		out.Records[id] = &copied
	}
	return out
}

// SetState replaces it.
func (s *Store) SetState(st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Records == nil {
		st.Records = map[string]*RecordState{}
	}
	s.state = st
	return writeJSON(filepath.Join(s.dir, stateFile), st)
}

// Pending returns the changes waiting for somebody to decide (§6.2).
func (s *Store) Pending() []PendingChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PendingChange, len(s.pending))
	copy(out, s.pending)
	return out
}

// SetPending replaces the list.
//
// Replaced rather than merged, because it is derived: every sync recomputes what
// is still waiting. An item that stayed would be one the repository no longer
// asks for, and answering it would apply something nobody is proposing any more.
func (s *Store) SetPending(list []PendingChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if list[i].RecordID != list[j].RecordID {
			return list[i].RecordID < list[j].RecordID
		}
		return list[i].Field < list[j].Field
	})
	s.pending = list
	return writeJSON(filepath.Join(s.dir, pendingFile), list)
}

// Dismiss records that somebody chose to keep this machine's value (§9.1).
func (s *Store) Dismiss(recordID, field string, rev int64) error {
	s.mu.Lock()
	rs := s.state.Records[recordID]
	if rs == nil {
		rs = &RecordState{}
		s.state.Records[recordID] = rs
	}
	if rs.Dismissed == nil {
		rs.Dismissed = map[string]int64{}
	}
	rs.Dismissed[field] = rev
	kept := s.pending[:0:0]
	for _, p := range s.pending {
		if p.RecordID == recordID && p.Field == field {
			continue
		}
		kept = append(kept, p)
	}
	s.pending = kept
	state, pending := s.state, s.pending
	s.mu.Unlock()

	if err := writeJSON(filepath.Join(s.dir, stateFile), state); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.dir, pendingFile), pending)
}

// WasDismissed reports whether this decision was already answered at this
// revision or a later one.
func (s *Store) WasDismissed(recordID, field string, rev int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.state.Records[recordID]
	if rs == nil {
		return false
	}
	at, ok := rs.Dismissed[field]
	return ok && at >= rev
}

// KeyMap returns the fingerprint-to-key map for this machine (§3.3).
func (s *Store) KeyMap() map[string]KeyLocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]KeyLocation, len(s.keymap))
	for k, v := range s.keymap {
		out[k] = v
	}
	return out
}

// SetKeyLocation records where a fingerprint's key is on this machine.
func (s *Store) SetKeyLocation(fingerprint string, loc KeyLocation) error {
	s.mu.Lock()
	s.keymap[fingerprint] = loc
	snapshot := make(map[string]KeyLocation, len(s.keymap))
	for k, v := range s.keymap {
		snapshot[k] = v
	}
	s.mu.Unlock()
	return writeJSON(filepath.Join(s.dir, keymapFile), snapshot)
}

// AppendHistory adds one line to the sync history, oldest lines dropped once it
// grows past historyLimit.
func (s *Store) AppendHistory(e HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, historyFile)
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("cfgsync: encode history: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("cfgsync: open history: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("cfgsync: write history: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cfgsync: close history: %w", err)
	}
	return s.trimHistory(path)
}

func (s *Store) trimHistory(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= historyLimit {
		return nil
	}
	kept := strings.Join(lines[len(lines)-historyLimit:], "\n") + "\n"
	return atomicWrite(path, []byte(kept))
}

// History reads the sync history, newest first, at most limit entries.
func (s *Store) History(limit int) []HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dir, historyFile))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var out []HistoryEntry
	for i := len(lines) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if lines[i] == "" {
			continue
		}
		var e HistoryEntry
		if json.Unmarshal([]byte(lines[i]), &e) != nil {
			// One bad line is skipped. The history is for reading, and refusing
			// to show any of it because one line is broken shows nothing.
			continue
		}
		out = append(out, e)
	}
	return out
}

func readJSON(path string, into any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("cfgsync: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("cfgsync: encode %s: %w", filepath.Base(path), err)
	}
	return atomicWrite(path, append(data, '\n'))
}

// atomicWrite replaces a file or leaves the old one.
//
// The same shape as the rest of the app's writes: temp file beside the target,
// then rename. A state.json truncated by a crash is worse than an old one — the
// rewind check reads it.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("cfgsync: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cfgsync: replace %s: %w", filepath.Base(path), err)
	}
	return nil
}

// jsonMarshalIndent is json.MarshalIndent with this package's shape, kept here so
// service.go does not import encoding/json for one call.
func jsonMarshalIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
