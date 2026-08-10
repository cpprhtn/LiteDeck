// Package config stores the host list and application settings on the local
// machine (§6). Nothing here ever reaches a server, and nothing here ever holds
// a secret — passwords and passphrases live in the OS keychain
// (internal/secret) and nowhere else.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// AuthMethod names one way to authenticate, in the order the user wants tried.
type AuthMethod string

const (
	AuthAgent    AuthMethod = "agent"    // ssh-agent; preferred, key never enters this process
	AuthKey      AuthMethod = "key"      // private key file, optionally passphrase-protected
	AuthPassword AuthMethod = "password" // password, from the keychain or typed each time
)

// Host is one server as the user described it.
//
// There is deliberately no password field. Adding one would make hosts.json a
// secret-bearing file, and §6 forbids that — the file is plain JSON in the
// user's config directory, frequently copied between machines and pasted into
// bug reports.
type Host struct {
	ID       string `json:"id"`
	Name     string `json:"name"`            // display label; defaults to Hostname
	Group    string `json:"group,omitempty"` // sidebar folder
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
	User     string `json:"user"`

	Auth         []AuthMethod `json:"auth"`
	IdentityFile string       `json:"identityFile,omitempty"`

	// ProxyJump is a single hop, as `user@host:port` (§4.1). Chains are v1.x.
	ProxyJump string `json:"proxyJump,omitempty"`

	// Source records where the entry came from, so an ssh_config re-import can
	// tell its own entries from hand-made ones.
	Source string `json:"source,omitempty"` // "", "ssh_config"
}

// Addr returns the dial target.
func (h Host) Addr() string {
	port := h.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", h.Hostname, port)
}

// Label is what the sidebar shows.
func (h Host) Label() string {
	if h.Name != "" {
		return h.Name
	}
	return h.Hostname
}

// Validate reports why a host cannot be saved.
func (h Host) Validate() error {
	switch {
	case strings.TrimSpace(h.ID) == "":
		return errors.New("config: host ID is required")
	case strings.TrimSpace(h.Hostname) == "":
		return errors.New("config: hostname is required")
	case strings.TrimSpace(h.User) == "":
		return errors.New("config: user is required")
	case h.Port < 0 || h.Port > 65535:
		return fmt.Errorf("config: port %d out of range", h.Port)
	case len(h.Auth) == 0:
		return errors.New("config: at least one authentication method is required")
	}
	for _, a := range h.Auth {
		if a != AuthAgent && a != AuthKey && a != AuthPassword {
			return fmt.Errorf("config: unknown authentication method %q", a)
		}
	}
	if slices.Contains(h.Auth, AuthKey) && h.IdentityFile == "" {
		return errors.New("config: key authentication needs an identity file")
	}
	return nil
}

// Dir returns the application's configuration directory, creating it if needed.
// os.UserConfigDir already resolves to the locations §6 specifies:
// ~/.config on Linux, ~/Library/Application Support on macOS, %AppData% on
// Windows.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: locate config directory: %w", err)
	}
	dir := filepath.Join(base, "litedeck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("config: create %s: %w", dir, err)
	}
	return dir, nil
}

// KnownHostsPath is where LiteDeck records host keys (§7.1).
func KnownHostsPath(dir string) string {
	return filepath.Join(dir, "known_hosts")
}

// Store is the host list, backed by hosts.json.
type Store struct {
	path string

	mu    sync.RWMutex
	hosts []Host
}

// Open loads hosts.json from dir, treating a missing file as an empty list.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "hosts.json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.hosts); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", s.path, err)
	}
	return s, nil
}

// Path returns the backing file.
func (s *Store) Path() string { return s.path }

// List returns a copy of the hosts, ordered by group then label.
func (s *Store) List() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.hosts)
	slices.SortFunc(out, func(a, b Host) int {
		if a.Group != b.Group {
			return strings.Compare(a.Group, b.Group)
		}
		return strings.Compare(strings.ToLower(a.Label()), strings.ToLower(b.Label()))
	})
	return out
}

// Get looks up one host.
func (s *Store) Get(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := slices.IndexFunc(s.hosts, func(h Host) bool { return h.ID == id })
	if i < 0 {
		return Host{}, false
	}
	return s.hosts[i], true
}

// Upsert adds or replaces a host and persists the list.
func (s *Store) Upsert(h Host) error {
	if err := h.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if i := slices.IndexFunc(s.hosts, func(x Host) bool { return x.ID == h.ID }); i >= 0 {
		s.hosts[i] = h
	} else {
		s.hosts = append(s.hosts, h)
	}
	s.mu.Unlock()
	return s.save()
}

// Delete removes a host.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	before := len(s.hosts)
	s.hosts = slices.DeleteFunc(s.hosts, func(h Host) bool { return h.ID == id })
	changed := len(s.hosts) != before
	s.mu.Unlock()
	if !changed {
		return fmt.Errorf("config: no host with ID %q", id)
	}
	return s.save()
}

// save writes the list atomically: a crash mid-write must not leave the user
// with a truncated host list and no way back.
func (s *Store) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.hosts, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("config: encode hosts: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".hosts-*.json")
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
