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

	// MCP holds the AI integration's settings. Off until somebody turns it on:
	// an endpoint that speaks for every connected server is not something to
	// open because the app was installed.
	MCP MCPSettings `json:"mcp,omitzero"`
}

// MCPSettings is the AI integration (§4 of the MCP design note).
type MCPSettings struct {
	// Enabled starts the local endpoint at launch.
	Enabled bool `json:"enabled,omitempty"`
	// Token authorises clients. Persisted rather than regenerated per launch,
	// because a token that changes every start breaks the client config the
	// user pasted in once and expects to keep working.
	Token string `json:"token,omitempty"`
	// Port to bind on loopback, and whether the user chose it.
	//
	// The two are separate because they answer different questions: Port is
	// what the app should try, PortPinned is whether anybody asked for it.
	// Until v1.2.3 the port that happened to get bound was written back here on
	// every launch, so one busy moment on the default port moved the endpoint
	// permanently — the app tried the remembered port next launch, got it, and
	// never went home (#2). A Port with no PortPinned is a leftover of that and
	// is ignored.
	Port       int  `json:"port,omitempty"`
	PortPinned bool `json:"portPinned,omitempty"`
	// Hosts the AI may read, by host ID. Absent means no: registering a server
	// in LiteDeck must not hand it to an AI as a side effect.
	Hosts map[string]bool `json:"hosts,omitempty"`
	// Write is the per-host approval mode for changes. Absent means "ask",
	// which is the default and the only mode that needs no expiry.
	Write map[string]MCPWritePolicy `json:"write,omitempty"`
	// Delete lists hosts where file deletion is offered at all. Separate from
	// the approval mode because they answer different questions: whether the
	// tool exists, and whether using it interrupts you. Absent means no.
	Delete map[string]bool `json:"delete,omitempty"`
}

// MCPWritePolicy is how one host handles a write an AI asks for (§4.2).
//
// Deliberately separate from Hosts: sharing a server to be read and letting
// something change it are different decisions, and collapsing them into one
// switch would make the cautious answer "share nothing".
type MCPWritePolicy struct {
	// Mode is "ask", "auto" or "bypass". Empty is "ask".
	Mode string `json:"mode"`
	// Until is when a relaxed mode reverts, in unix seconds. There is no
	// "forever": a mode nobody remembers enabling is the one that causes the
	// incident, and renewing it costs a click.
	Until int64 `json:"until,omitempty"`
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
// SetMCP replaces the MCP settings.
func (s *SettingsStore) SetMCP(m MCPSettings) error {
	s.mu.Lock()
	s.settings.MCP = m
	s.mu.Unlock()
	return s.save()
}

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
