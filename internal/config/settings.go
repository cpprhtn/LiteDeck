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

	// LastSeen is when this app was last looking at a host, in unix seconds,
	// by host ID. It is what "since you last looked" is measured from.
	//
	// Local, and deliberately: it is a fact about this person's attention, not
	// about the server. Two people watching the same box have two different
	// answers, and writing it to the server would give them one wrong one.
	LastSeen map[string]int64 `json:"lastSeen,omitempty"`

	// ShellHistory lists hosts whose shell history file may be read, by host ID.
	//
	// Off until asked for, and its own switch rather than part of connecting: a
	// shell history is the densest credential file on a server, and reading one
	// is a different decision from opening a terminal on the same box.
	ShellHistory map[string]bool `json:"shellHistory,omitempty"`

	// KnownLogins lists the addresses this person has already seen succeed on a
	// host, by host ID.
	//
	// The security tab marks a successful login from anywhere else. That is the
	// one line on the screen that can be urgent: failed passwords arrive by the
	// thousand and mean nothing on their own, and a success from an address
	// nobody recognises means something whatever the failure count says.
	//
	// Local for the same reason LastSeen is. "Addresses I recognise" is a fact
	// about this person — the colleague who logs in from another country is not
	// a surprise to themselves — and one shared list would be wrong for both.
	KnownLogins map[string][]string `json:"knownLogins,omitempty"`
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
	// Until v1.3.0 the port that happened to get bound was written back here on
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
	// Exec lists hosts where arbitrary commands may be run. Its own switch for
	// the same reason Delete has one, and off for the same reason: most people
	// want an agent that reads and edits. Unlike the others it cannot be waved
	// through — a command leaves no copy to put back, so the approval mode does
	// not apply to it.
	Exec map[string]bool `json:"exec,omitempty"`
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

// SetMCP replaces the MCP settings.
func (s *SettingsStore) SetMCP(m MCPSettings) error {
	s.mu.Lock()
	s.settings.MCP = m
	s.mu.Unlock()
	return s.save()
}

// SetLastSeen records that this app was looking at a host, in unix seconds.
//
// Written when the digest has been shown, not when the host connects: the point
// of the mark is "you have seen what happened up to here", and moving it on
// connect would consume the answer before anybody read it.
func (s *SettingsStore) SetLastSeen(hostID string, at int64) error {
	s.mu.Lock()
	if s.settings.LastSeen == nil {
		s.settings.LastSeen = map[string]int64{}
	}
	s.settings.LastSeen[hostID] = at
	s.mu.Unlock()
	return s.save()
}

// RememberLogins adds addresses to a host's known set and reports which of them
// were new.
//
// Called when the security tab has shown them, not when they are read: the mark
// exists so an address is surprising exactly once, and moving it before anybody
// looked would consume the surprise.
func (s *SettingsStore) RememberLogins(hostID string, addrs []string) []string {
	s.mu.Lock()
	if s.settings.KnownLogins == nil {
		s.settings.KnownLogins = map[string][]string{}
	}
	known := map[string]bool{}
	for _, a := range s.settings.KnownLogins[hostID] {
		known[a] = true
	}
	var fresh []string
	for _, a := range addrs {
		if a == "" || known[a] {
			continue
		}
		known[a] = true
		fresh = append(fresh, a)
		s.settings.KnownLogins[hostID] = append(s.settings.KnownLogins[hostID], a)
	}
	s.mu.Unlock()
	if len(fresh) == 0 {
		return nil
	}
	// A failed write costs the mark, not the answer: the addresses are still
	// reported as new this time, and asked about again next time.
	_ = s.save()
	return fresh
}

// KnownLogins is the addresses already seen succeeding on a host.
func (s *SettingsStore) KnownLogins(hostID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.settings.KnownLogins[hostID]...)
}

// SetShellHistory turns the shell history on or off for one host.
func (s *SettingsStore) SetShellHistory(hostID string, allowed bool) error {
	s.mu.Lock()
	if s.settings.ShellHistory == nil {
		s.settings.ShellHistory = map[string]bool{}
	}
	if allowed {
		s.settings.ShellHistory[hostID] = true
	} else {
		delete(s.settings.ShellHistory, hostID)
	}
	s.mu.Unlock()
	return s.save()
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
