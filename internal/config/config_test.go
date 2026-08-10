package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleHost() Host {
	return Host{
		ID:       "h1",
		Name:     "prod-web-01",
		Group:    "production",
		Hostname: "10.0.0.5",
		Port:     22,
		User:     "deploy",
		Auth:     []AuthMethod{AuthAgent, AuthPassword},
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatalf("new store is not empty: %v", s.List())
	}

	if err := s.Upsert(sampleHost()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Reopening must see the same thing — the file, not the in-memory copy,
	// is the source of truth.
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := s2.List()
	if len(got) != 1 || got[0].ID != "h1" || got[0].Addr() != "10.0.0.5:22" {
		t.Fatalf("reopened store = %+v", got)
	}

	edited := sampleHost()
	edited.Name = "renamed"
	if err := s2.Upsert(edited); err != nil {
		t.Fatalf("Upsert edit: %v", err)
	}
	if len(s2.List()) != 1 {
		t.Errorf("upserting an existing ID duplicated it: %+v", s2.List())
	}
	if got, _ := s2.Get("h1"); got.Name != "renamed" {
		t.Errorf("edit lost: %+v", got)
	}

	if err := s2.Delete("h1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s2.Delete("h1"); err == nil {
		t.Error("deleting a missing host succeeded; want an error")
	}
}

// TestStoreNeverPersistsSecrets is the §6 guarantee in test form: hosts.json is
// plain JSON that users copy between machines and paste into bug reports, so a
// secret must never be able to reach it.
//
// The check is an allowlist of persisted keys rather than a search for the word
// "password" — "password" is a legitimate *value* in the auth list, naming a
// method. An allowlist also means any future field has to be reviewed here
// before it can ship, which is the actual protection.
func TestStoreNeverPersistsSecrets(t *testing.T) {
	allowed := map[string]bool{
		"id": true, "name": true, "group": true, "hostname": true,
		"port": true, "user": true, "auth": true, "identityFile": true,
		"proxyJump": true, "source": true,
	}

	dir := t.TempDir()
	s, _ := Open(dir)
	full := sampleHost()
	full.Auth = []AuthMethod{AuthAgent, AuthKey, AuthPassword}
	full.IdentityFile = "/home/u/.ssh/id_ed25519"
	full.ProxyJump = "bastion"
	full.Source = "ssh_config"
	if err := s.Upsert(full); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	for k := range rows[0] {
		if !allowed[k] {
			t.Errorf("hosts.json persists unreviewed field %q — if it can hold a secret it belongs in the keychain (§6)", k)
		}
	}

	// A secret must not be reachable even by mistake: the type has no field to
	// put one in. Verified by round-tripping a struct that tries.
	var probe map[string]any
	if err := json.Unmarshal(mustMarshal(t, full), &probe); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password", "passphrase", "secret", "token", "credential"} {
		for k := range probe {
			if strings.EqualFold(k, forbidden) {
				t.Errorf("Host has a secret-bearing field %q", k)
			}
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStoreFilePermissions(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Upsert(sampleHost()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("hosts.json mode = %o, want 600", perm)
	}
}

func TestHostValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Host)
		ok   bool
	}{
		{"valid", func(*Host) {}, true},
		{"no id", func(h *Host) { h.ID = "" }, false},
		{"no hostname", func(h *Host) { h.Hostname = "" }, false},
		{"no user", func(h *Host) { h.User = "" }, false},
		{"bad port", func(h *Host) { h.Port = 70000 }, false},
		{"no auth", func(h *Host) { h.Auth = nil }, false},
		{"unknown auth", func(h *Host) { h.Auth = []AuthMethod{"magic"} }, false},
		{"key without identity file", func(h *Host) { h.Auth = []AuthMethod{AuthKey} }, false},
		{"key with identity file", func(h *Host) {
			h.Auth = []AuthMethod{AuthKey}
			h.IdentityFile = "/home/u/.ssh/id_ed25519"
		}, true},
	}
	for _, c := range cases {
		h := sampleHost()
		c.mut(&h)
		err := h.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestHostDefaults(t *testing.T) {
	h := Host{Hostname: "example.com"}
	if h.Addr() != "example.com:22" {
		t.Errorf("Addr with no port = %q, want example.com:22", h.Addr())
	}
	if h.Label() != "example.com" {
		t.Errorf("Label with no name = %q", h.Label())
	}
}
