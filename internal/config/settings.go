package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Preferences that are not hosts (§6).
//
// A separate file from hosts.json rather than a field inside it. hosts.json is
// the thing people copy between machines, paste into bug reports and hand-edit;
// a UI preference has no business travelling with it, and a parse error in one
// must not cost the other.

// Settings is what the app remembers between runs.
//
// Deliberately small. Anything that belongs to a host belongs in hosts.json,
// and anything the OS already knows should be asked of the OS rather than
// stored — this file is for choices the user made that nothing else records.
type Settings struct {
	// Language is a BCP 47 tag ("ko", "en"). Empty means "follow the OS",
	// which is the default and stays the default until somebody chooses
	// otherwise — an explicit choice is the only reason to write this file.
	Language string `json:"language,omitempty"`
}

// SettingsStore is settings.json.
type SettingsStore struct {
	path string

	mu       sync.RWMutex
	settings Settings
}

// OpenSettings loads settings.json from dir. A missing file is the normal
// first-run state, not an error.
//
// A *corrupt* file is not an error either: preferences are not worth refusing
// to start over. The defaults are used and the bad file is left alone for
// inspection rather than silently overwritten.
func OpenSettings(dir string) *SettingsStore {
	s := &SettingsStore{path: filepath.Join(dir, "settings.json")}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// Nothing to report to: this runs before the UI exists. The
			// defaults are a working state.
			return s
		}
		return s
	}
	_ = json.Unmarshal(data, &s.settings)
	return s
}

// Path returns the backing file.
func (s *SettingsStore) Path() string { return s.path }

// Get returns a copy of the current settings.
func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetLanguage records an explicit choice. An empty tag means "follow the OS"
// and is a legitimate value — it is how somebody undoes a choice.
func (s *SettingsStore) SetLanguage(tag string) error {
	s.mu.Lock()
	s.settings.Language = tag
	s.mu.Unlock()
	return s.save()
}

// save writes settings.json atomically, for the same reason hosts.json is
// written that way: a crash mid-write must not leave a truncated file that
// fails to parse on the next start.
func (s *SettingsStore) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.settings, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("config: encode settings: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*.json")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("config: replace %s: %w", s.path, err)
	}
	return nil
}
