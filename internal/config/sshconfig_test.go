package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleSSHConfig = `# LiteDeck test fixture
Host *
    ServerAliveInterval 60
    StrictHostKeyChecking yes

Host prod-web
    HostName 10.0.0.5
    User deploy
    Port 2222
    IdentityFile ~/.ssh/id_ed25519

Host db1 db2
    HostName db.internal
    User postgres

Host bastion-behind
    HostName 10.0.9.9
    User ops
    ProxyJump bastion

Host  spaced-out
    HostName=example.com
    User = alice

Host *.internal
    User wildcarded

Host plain-alias
`

func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func byName(hosts []Host) map[string]Host {
	m := make(map[string]Host, len(hosts))
	for _, h := range hosts {
		m[h.Name] = h
	}
	return m
}

func TestImportSSHConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config", sampleSSHConfig)

	hosts, err := ImportSSHConfig(path)
	if err != nil {
		t.Fatalf("ImportSSHConfig: %v", err)
	}
	m := byName(hosts)

	// Wildcard blocks configure defaults, they do not name a server. Importing
	// them would put unconnectable rows in the sidebar.
	for _, unwanted := range []string{"*", "*.internal"} {
		if _, ok := m[unwanted]; ok {
			t.Errorf("wildcard pattern %q was imported", unwanted)
		}
	}

	web, ok := m["prod-web"]
	if !ok {
		t.Fatal("prod-web missing")
	}
	if web.Hostname != "10.0.0.5" || web.User != "deploy" || web.Port != 2222 {
		t.Errorf("prod-web = %+v", web)
	}
	if web.Addr() != "10.0.0.5:2222" {
		t.Errorf("prod-web Addr = %q", web.Addr())
	}
	if filepath.Base(web.IdentityFile) != "id_ed25519" || web.IdentityFile[0] == '~' {
		t.Errorf("identity file not expanded: %q", web.IdentityFile)
	}
	if len(web.Auth) == 0 || web.Auth[0] != AuthAgent {
		t.Errorf("agent should be tried first: %v", web.Auth)
	}

	// One Host line naming several aliases yields one entry each.
	for _, name := range []string{"db1", "db2"} {
		h, ok := m[name]
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		if h.Hostname != "db.internal" || h.User != "postgres" {
			t.Errorf("%s = %+v", name, h)
		}
	}

	if got := m["bastion-behind"].ProxyJump; got != "bastion" {
		t.Errorf("ProxyJump = %q, want bastion", got)
	}

	// OpenSSH accepts Key=value and extra whitespace.
	if h := m["spaced-out"]; h.Hostname != "example.com" || h.User != "alice" {
		t.Errorf("spaced-out = %+v", h)
	}

	// A Host with no HostName uses the alias as the address.
	if h := m["plain-alias"]; h.Hostname != "plain-alias" {
		t.Errorf("plain-alias hostname = %q, want the alias itself", h.Hostname)
	}

	for _, h := range hosts {
		if h.Source != "ssh_config" {
			t.Errorf("%s: Source = %q", h.Name, h.Source)
		}
		if err := h.Validate(); err != nil {
			t.Errorf("%s: imported host does not validate: %v", h.Name, err)
		}
	}
}

// TestImportSSHConfigInclude covers configs split across config.d/, which is
// common enough that skipping Include would look like a broken import.
func TestImportSSHConfigInclude(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "config.d/extra", "Host included-host\n    HostName 192.168.1.1\n    User root\n")
	path := writeConfig(t, dir, "config",
		"Include config.d/*\n\nHost main\n    HostName main.example.com\n    User me\n")

	hosts, err := ImportSSHConfig(path)
	if err != nil {
		t.Fatalf("ImportSSHConfig: %v", err)
	}
	m := byName(hosts)
	if h, ok := m["included-host"]; !ok || h.Hostname != "192.168.1.1" {
		t.Errorf("Include not honoured: %+v", hosts)
	}
	if _, ok := m["main"]; !ok {
		t.Error("host defined after Include was lost")
	}
}

func TestImportSSHConfigMissingFile(t *testing.T) {
	if _, err := ImportSSHConfig(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("importing a missing file succeeded; want an error")
	}
}

func TestImportSSHConfigDanglingInclude(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "config",
		"Include does-not-exist/*\n\nHost survivor\n    HostName s.example.com\n    User me\n")

	hosts, err := ImportSSHConfig(path)
	if err != nil {
		t.Fatalf("a dangling Include should not fail the import: %v", err)
	}
	if _, ok := byName(hosts)["survivor"]; !ok {
		t.Error("hosts after a dangling Include were lost")
	}
}
