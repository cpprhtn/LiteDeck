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
		if h.Hostname != "db.internal" || h.User != "postgres" || h.Port != 22 {
			t.Errorf("%s = %+v", name, h)
		}
		if len(h.Auth) != 2 || h.Auth[0] != AuthAgent || h.Auth[1] != AuthPassword {
			t.Errorf("%s auth = %v, want agent then password", name, h.Auth)
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
	if h := m["plain-alias"]; h.Hostname != "plain-alias" || h.Port != 22 {
		t.Errorf("plain-alias = %+v, want hostname plain-alias on port 22", h)
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

// A Match block ends the Host block before it.
//
// Everything inside one is conditional on things this importer cannot evaluate
// — the final hostname, the local user, the exit status of a command — so its
// directives used to be attached to whichever Host came last. A `Match host
// bastion` carrying a ProxyJump then put that ProxyJump on an unrelated server,
// and the import looked right until somebody tried to connect.
func TestMatchBlockDoesNotLeakIntoTheHostAbove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := `Host prod
  HostName prod.example.com
  User deploy

Match host bastion
  ProxyJump jump@gateway:22
  User someone-else

Host staging
  HostName staging.example.com
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	hosts, err := ImportSSHConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Host{}
	for _, h := range hosts {
		byName[h.Name] = h
	}

	prod, ok := byName["prod"]
	if !ok {
		t.Fatal("prod was not imported")
	}
	if prod.ProxyJump != "" {
		t.Errorf("prod picked up the Match block's ProxyJump: %q", prod.ProxyJump)
	}
	if prod.User != "deploy" {
		t.Errorf("prod user = %q, want deploy", prod.User)
	}
	// And the Host after the Match block is still read.
	if s, ok := byName["staging"]; !ok || s.Hostname != "staging.example.com" {
		t.Errorf("staging = %+v", s)
	}
}
