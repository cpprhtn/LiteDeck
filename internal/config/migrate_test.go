package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeSecrets is the credential store as a map, so a test can watch the order
// of operations and make one of them fail.
type fakeSecrets struct {
	items map[string]string // "kind:hostID" → value
	gone  bool              // no credential store on this machine
	ops   []string
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{items: map[string]string{}} }

func (f *fakeSecrets) Available() bool { return !f.gone }

func (f *fakeSecrets) Get(hostID, kind string) (string, error) {
	v, ok := f.items[kind+":"+hostID]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (f *fakeSecrets) Set(hostID, kind, value string) error {
	f.ops = append(f.ops, "set "+kind+":"+hostID)
	f.items[kind+":"+hostID] = value
	return nil
}

func (f *fakeSecrets) Delete(hostID, kind string) error {
	f.ops = append(f.ops, "del "+kind+":"+hostID)
	delete(f.items, kind+":"+hostID)
	return nil
}

func migrationFixture(t *testing.T) (*Store, *SettingsStore, *fakeSecrets) {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	hosts := []Host{
		{ID: "host-1786033219477533000", Name: "prod", Hostname: "10.0.0.5", Port: 22,
			User: "deploy", Auth: []AuthMethod{AuthPassword}},
		{ID: "sshconfig:cpp", Name: "cpp", Hostname: "example.com", Port: 22,
			User: "me", Auth: []AuthMethod{AuthAgent}, Source: SSHConfigSource},
	}
	if err := store.ReplaceAll(hosts); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}

	settings := OpenSettings(dir)
	if err := settings.SetMCP(MCPSettings{
		Enabled: true,
		Hosts:   map[string]bool{"host-1786033219477533000": true, "sshconfig:cpp": true},
		Write:   map[string]MCPWritePolicy{"host-1786033219477533000": {Mode: "strict"}},
		Delete:  map[string]bool{"host-1786033219477533000": true},
		Exec:    map[string]bool{"host-1786033219477533000": true},
	}); err != nil {
		t.Fatalf("seed mcp: %v", err)
	}
	if err := settings.SetLastSeen("host-1786033219477533000", 1789000000); err != nil {
		t.Fatalf("seed lastSeen: %v", err)
	}
	if err := settings.SetShellHistory("host-1786033219477533000", true); err != nil {
		t.Fatalf("seed shellHistory: %v", err)
	}
	settings.RememberLogins("host-1786033219477533000", []string{"203.0.113.7"})

	secrets := newFakeSecrets()
	for _, kind := range SecretKinds {
		secrets.items[kind+":host-1786033219477533000"] = "secret-" + kind
	}
	return store, settings, secrets
}

// The rename carries everything that was keyed by the old ID.
//
// Six of the seven settings maps fail silently when they are missed: the value
// simply reverts to its default, and the user finds out when an AI client says
// it cannot reach a server they shared, or when the security tab flags an
// address it had been taught to recognise.
func TestMigrateCarriesSecretsAndSettings(t *testing.T) {
	store, settings, secrets := migrationFixture(t)

	rep, err := MigrateHostIDs(store, settings, secrets)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	id, ok := rep.Renamed["host-1786033219477533000"]
	if !ok {
		t.Fatal("the generated-ID host was not renamed")
	}
	if !strings.Contains(id, "-") || id != strings.ToLower(id) {
		t.Errorf("new id %q is not a lowercase hyphenated UUID", id)
	}

	// hosts.json
	h, ok := store.Get(id)
	if !ok {
		t.Fatalf("no host under the new id %q", id)
	}
	if h.LegacyID != "host-1786033219477533000" {
		t.Errorf("LegacyID = %q — a bug report names the old one", h.LegacyID)
	}
	if _, ok := store.Get("host-1786033219477533000"); ok {
		t.Error("the old id still resolves, so both would be synced")
	}

	// credentials
	for _, kind := range SecretKinds {
		if got, err := secrets.Get(id, kind); err != nil || got != "secret-"+kind {
			t.Errorf("%s did not move: %q %v", kind, got, err)
		}
		if _, err := secrets.Get("host-1786033219477533000", kind); err == nil {
			t.Errorf("%s is still under the old id, so it is stored twice", kind)
		}
	}
	if rep.Secrets != len(SecretKinds) {
		t.Errorf("moved %d secrets, want %d", rep.Secrets, len(SecretKinds))
	}

	// settings, all seven
	got := settings.Get()
	if !got.MCP.Hosts[id] {
		t.Error("the host is no longer shared with AI clients")
	}
	if got.MCP.Write[id].Mode != "strict" {
		t.Errorf("write policy lost: %+v", got.MCP.Write)
	}
	if !got.MCP.Delete[id] || !got.MCP.Exec[id] {
		t.Error("delete or exec toggle lost")
	}
	if got.LastSeen[id] != 1789000000 {
		t.Errorf("lastSeen lost: %v", got.LastSeen)
	}
	if !got.ShellHistory[id] {
		t.Error("shell history permission lost")
	}
	if len(got.KnownLogins[id]) != 1 {
		t.Errorf("known logins lost: %v", got.KnownLogins)
	}
	// Untouched keys stay untouched.
	if !got.MCP.Hosts["sshconfig:cpp"] {
		t.Error("an entry that was not being renamed was dropped")
	}
}

// A host that came from ~/.ssh/config keeps its name.
//
// The importer matches by ID, so a renamed one is not recognised on the next
// import and a second copy appears. They are out of scope for sync for the same
// reason: that file already carries hosts between machines.
func TestMigrateLeavesSSHConfigHostsAlone(t *testing.T) {
	store, settings, secrets := migrationFixture(t)
	rep, err := MigrateHostIDs(store, settings, secrets)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, renamed := rep.Renamed["sshconfig:cpp"]; renamed {
		t.Error("an ssh_config host was renamed — the next import will duplicate it")
	}
	if _, ok := store.Get("sshconfig:cpp"); !ok {
		t.Error("the ssh_config host lost its id")
	}
}

// Running it again does nothing.
//
// It runs at every start, and a second UUID would orphan everything the first
// one just carried across.
func TestMigrateIsIdempotent(t *testing.T) {
	store, settings, secrets := migrationFixture(t)
	first, err := MigrateHostIDs(store, settings, secrets)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	id := first.Renamed["host-1786033219477533000"]

	second, err := MigrateHostIDs(store, settings, secrets)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(second.Renamed) != 0 {
		t.Errorf("the second run renamed %v", second.Renamed)
	}
	if _, ok := store.Get(id); !ok {
		t.Error("the second run moved the host again")
	}
	if NeedsHostIDMigration(store.List()) {
		t.Error("still reports work to do after it was done")
	}
}

// A credential store that cannot be reached is not a failure.
//
// A headless Rocky or a minimal Ubuntu has no D-Bus Secret Service. Nothing was
// stored there, so nothing is lost — but the rename still has to happen, or the
// host never gets an ID it can be synced under.
func TestMigrateWithoutACredentialStore(t *testing.T) {
	store, settings, secrets := migrationFixture(t)
	secrets.gone = true

	rep, err := MigrateHostIDs(store, settings, secrets)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !rep.SecretsSkipped {
		t.Error("the report does not say the credential store was unreachable")
	}
	if len(rep.Renamed) != 1 {
		t.Errorf("the rename did not happen: %v", rep.Renamed)
	}
	if len(secrets.ops) != 0 {
		t.Errorf("it touched a store it had been told was gone: %v", secrets.ops)
	}
}

// A secret is written under the new name before the old one is dropped.
//
// The other order loses the password when the write fails, and there is no
// second copy anywhere — hosts.json does not hold one, deliberately.
func TestMigrateWritesTheNewSecretBeforeDroppingTheOld(t *testing.T) {
	store, settings, secrets := migrationFixture(t)
	if _, err := MigrateHostIDs(store, settings, secrets); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := 0; i+1 < len(secrets.ops); i += 2 {
		if !strings.HasPrefix(secrets.ops[i], "set ") || !strings.HasPrefix(secrets.ops[i+1], "del ") {
			t.Fatalf("order is %v — a delete ran before its write", secrets.ops)
		}
	}
}

// When a credential cannot be written, nothing else moves.
//
// hosts.json is written last for exactly this: it still names the old ID, so
// the next start runs the same migration again and finishes it. Renaming the
// host first would strand the secrets behind an ID no host claims.
func TestMigrateStopsBeforeRenamingWhenASecretCannotMove(t *testing.T) {
	store, settings, secrets := migrationFixture(t)
	// The new IDs are random, so the deterministic break is a store that
	// refuses every write rather than one named host.
	failing := &alwaysFailingSecrets{fakeSecrets: secrets}

	if _, err := MigrateHostIDs(store, settings, failing); err == nil {
		t.Fatal("a credential store that refused a write was reported as success")
	}
	if _, ok := store.Get("host-1786033219477533000"); !ok {
		t.Error("the host was renamed anyway, stranding its secrets")
	}
	if !NeedsHostIDMigration(store.List()) {
		t.Error("the next start will not retry")
	}
}

type alwaysFailingSecrets struct{ *fakeSecrets }

func (a *alwaysFailingSecrets) Set(string, string, string) error {
	return errors.New("credential store refused")
}

// A new per-host map cannot be added without somebody looking at this.
//
// TestMigrateCarriesSecretsAndSettings proves the seven that exist today are
// carried across — drop one `rename` call and it fails. What it cannot see is
// an eighth map added later: that one would simply lose its value at the next
// migration and say nothing. So this reads the struct itself and fails on any
// string-keyed map it has not been told about.
func TestANewPerHostMapHasToBeDeclared(t *testing.T) {
	// The maps keyed by host ID, by field path. Anything else in Settings is
	// not per-host and does not belong here.
	perHost := map[string]bool{
		"MCP.Hosts":    true,
		"MCP.Write":    true,
		"MCP.Delete":   true,
		"MCP.Exec":     true,
		"LastSeen":     true,
		"ShellHistory": true,
		"KnownLogins":  true,
	}

	var found []string
	var walk func(t reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name := prefix + f.Name
			switch f.Type.Kind() {
			case reflect.Map:
				if f.Type.Key().Kind() == reflect.String {
					found = append(found, name)
				}
			case reflect.Struct:
				walk(f.Type, name+".")
			}
		}
	}
	walk(reflect.TypeOf(Settings{}), "")

	for _, name := range found {
		if !perHost[name] {
			t.Errorf("Settings.%s is a string-keyed map this test has never seen.\n"+
				"If it is keyed by host ID, add it to RenameHosts and to this list — "+
				"a per-host map that is not renamed loses its value at the migration, "+
				"and says nothing when it does. If it is keyed by something else, add "+
				"it to this list as false.", name)
		}
	}
	for name := range perHost {
		if !slicesContains(found, name) {
			t.Errorf("Settings.%s is gone; drop it from RenameHosts and from here", name)
		}
	}
}

func slicesContains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
