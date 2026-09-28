package cfgsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// What this machine remembers about settings it has imported.
//
// Two files under <app data>/sync/, neither of them secret: what this machine has
// already seen, and which key on this machine answers to which fingerprint.
//
// # Why anything is remembered at all
//
// So that a file older than what is already here is noticed rather than applied.
// Each record carries a revision; this remembers the highest one seen per host,
// and a lower one coming back means the file predates what this machine has —
// somebody opening a backup from last month over settings they have since
// changed (§6.4).

// File names under the sync directory.
const (
	configFile  = "config.json"
	stateFile   = "state.json"
	pendingFile = "pending.json"
	keymapFile  = "local-keymap.json"
)

// Config is sync/config.json. Nothing secret, and very little of anything.
type Config struct {
	// DeviceID is this machine, as a UUID. It is written into every exported
	// file so two backups can be told apart — and it is a UUID rather than the
	// hostname because that file ends up in somebody's cloud folder, and a list
	// of the user's computers is not free to give away.
	DeviceID string `json:"deviceId,omitempty"`
}

// RecordState is what this machine knows about one record.
type RecordState struct {
	// Rev is the highest revision this machine has seen. A lower one coming back
	// is a rewound repository (§6.4).
	Rev int64 `json:"rev"`
	// Base is the record as it was last applied here.
	Base *Record `json:"base,omitempty"`
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

// Store holds the local sync files.
//
// Every write is whole-file and atomic: a half-written state.json would make the
// next start disagree with itself about which revisions it has seen, and the
// answer to that question decides whether a rewound repository is noticed.
type Store struct {
	dir string

	mu     sync.Mutex
	cfg    Config
	state  State
	keymap map[string]KeyLocation
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
	_ = readJSON(filepath.Join(dir, keymapFile), &s.keymap)
	if s.keymap == nil {
		s.keymap = map[string]KeyLocation{}
	}
	return s, nil
}

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
