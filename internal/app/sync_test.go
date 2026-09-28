package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/cfgsync"
	"github.com/cpprhtn/LiteDeck/internal/config"
)

// No MCP tool can reach the sync (§6.5).
//
// The pending list is where a person gives an AI client more room on a server. A
// tool that could press that button would be a model approving its own
// permissions, and the passphrase question is worse: a model that can ask for it is
// a model that can be talked into asking for it.
//
// Checked three ways, because one of them alone is a trap. The tool names would
// pass if a tool were called something neutral; the schema check would pass if the
// sync were reachable through an existing tool; and the source check is what
// notices a handler calling one of these functions directly.
func TestMCPCannotReachSync(t *testing.T) {
	a := appWithSettings(t)
	for _, tool := range registered(t, a).Tools() {
		name := strings.ToLower(tool.Name)
		for _, forbidden := range []string{"sync", "vault", "passphrase", "pending"} {
			if strings.Contains(name, forbidden) {
				t.Errorf("there is an MCP tool called %q", tool.Name)
			}
		}
		// And not in a description either, which is where a model would be told
		// to go looking for one.
		if strings.Contains(strings.ToLower(tool.Description), "passphrase") {
			t.Errorf("tool %q mentions the passphrase to the model", tool.Name)
		}
	}

	// No MCP handler calls into the sync. The approval path is the one that
	// matters — SyncApplyPending is the only way a policy gets looser — but the
	// whole package is checked, because "MCP asks the model's question" is the
	// shape of the mistake, not one function.
	for _, file := range []string{"mcp.go", "mcp_tools.go", "mcp_write.go", "mcp_approval.go", "mcp_rollback.go"} {
		src := readAppSource(t, file)
		for _, forbidden := range []string{"a.Sync", "cfgsync.", "syncLocal{"} {
			if strings.Contains(src, forbidden) {
				t.Errorf("%s reaches the sync (%s)", file, forbidden)
			}
		}
	}
}

// A policy that arrives relaxed gets its expiry from this machine's clock (§6.2).
//
// The record carries no expiry on purpose. Applying "bypass" with no expiry at all
// would be the one thing the app does not have: a relaxed mode with no end.
func TestAnArrivingRelaxedModeGetsAFreshExpiry(t *testing.T) {
	a := appWithSettings(t)
	const id = "3f2a0c1e-5b6d-4e7f-8a90-112233445566"

	if err := (syncLocal{a}).SetPolicy(id, cfgsync.RecordPolicy{
		Shared: true, MCPApproval: WriteBypass,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	got := a.settings.Get().MCP.Write[id]
	if got.Mode != WriteBypass {
		t.Errorf("mode = %q", got.Mode)
	}
	if got.Until == 0 {
		t.Error("a relaxed mode was applied with no expiry — there is no 'forever' in this app")
	}

	// And "ask" does not get one: it is the default and needs no countdown.
	if err := (syncLocal{a}).SetPolicy(id, cfgsync.RecordPolicy{MCPApproval: WriteAsk}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if u := a.settings.Get().MCP.Write[id].Until; u != 0 {
		t.Errorf("ask has an expiry: %d", u)
	}
}

// A host key arriving by sync has to match the fingerprint the person was shown.
//
// The pending list shows a fingerprint and the person approves that. If the key
// bytes and the fingerprint in the record disagree, approving one would trust the
// other — which is the whole of what a host key check is for.
func TestAHostKeyMustMatchTheFingerprintThatWasShown(t *testing.T) {
	a := appWithSettings(t)
	a.configDir = t.TempDir()

	err := (syncLocal{a}).AddHostKey("10.0.0.5:22", cfgsync.RecordHostKey{
		Alg:         "ssh-ed25519",
		Key:         "AAAAC3NzaC1lZDI1NTE5AAAAIMEqLGVRWiEK7cxL8vMvHC4LsIe0xM4rBl5NLBHnG9KX",
		Fingerprint: "SHA256:this-is-not-the-fingerprint",
	})
	if err == nil {
		t.Error("a key was trusted whose fingerprint did not match the bytes")
	}
	if err != nil && !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("refused for another reason: %v", err)
	}

	// Nothing was written.
	keys, err := (syncLocal{a}).HostKeys("10.0.0.5:22")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("the refused key was recorded anyway: %+v", keys)
	}
}

// A new host gets an ID that can travel.
func TestANewHostGetsAUUID(t *testing.T) {
	a := appWithSettings(t)
	if err := a.SaveHost(config.Host{
		Name: "prod", Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	hosts := a.hosts.List()
	if len(hosts) != 1 {
		t.Fatalf("hosts = %+v", hosts)
	}
	id := hosts[0].ID
	if strings.HasPrefix(id, "host-") {
		t.Errorf("id = %q — a name no second machine can arrive at", id)
	}
	if !cfgsync.Syncable(hosts[0]) {
		t.Errorf("a host created now is not syncable: %q", id)
	}
}

// A file exported here opens there, and the policy rules still hold.
//
// The file is the path for people with no git repository, and the temptation is
// to make it the simple path — a straight restore. It is not: whoever wrote the
// file chose what an AI client may do to those servers, and they were not at this
// desk. A backup from a machine where everything was shared and unguarded must
// not quietly make this machine the same.
func TestAnImportedFileGoesThroughTheSameGate(t *testing.T) {
	// The exporting machine: one host, shared with AI clients, dialogs off.
	from := appWithSettings(t)
	from.configDir = t.TempDir()
	if err := from.SaveHost(config.Host{
		Name: "prod-web", Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	id := from.hosts.List()[0].ID
	if err := (syncLocal{from}).SetPolicy(id, cfgsync.RecordPolicy{
		Shared: true, MCPApproval: WriteBypass, ExecEnabled: true,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}

	const pass = "a passphrase long enough"
	data, err := cfgsync.ExportBundle(from.exportableRecords(), pass, "device-a")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup"+cfgsync.BundleExt)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The receiving machine has never seen this host.
	to := appWithSettings(t)
	to.configDir = t.TempDir()

	preview, err := to.SyncPreviewFile(path, pass)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Hosts) != 1 || preview.Hosts[0].Name != "prod-web" {
		t.Fatalf("preview = %+v", preview.Hosts)
	}
	if preview.Hosts[0].State != "new" {
		t.Errorf("state = %q", preview.Hosts[0].State)
	}
	if !preview.Hosts[0].Loosens {
		t.Error("the preview does not say that this host's policy will be held back")
	}

	res, err := to.SyncImportFile(path, pass)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Received != 1 {
		t.Errorf("received %d", res.Received)
	}
	h, ok := to.hosts.Get(id)
	if !ok {
		t.Fatal("the host did not arrive")
	}
	if h.Name != "prod-web" || h.Hostname != "10.0.0.5" || h.User != "deploy" {
		t.Errorf("host = %+v", h)
	}

	// The connection details came across; the permissions did not.
	m := to.settings.Get().MCP
	if m.Hosts[id] {
		t.Error("importing a file shared a host with AI clients")
	}
	if m.Exec[id] {
		t.Error("importing a file turned on command execution")
	}
	if m.Write[id].Mode == WriteBypass {
		t.Error("importing a file turned off the approval dialogs")
	}
	if res.Pending == 0 {
		t.Error("nothing was queued for a decision, so the loosening is simply gone")
	}
}

// A wrong passphrase says so, and changes nothing.
func TestImportingWithTheWrongPassphraseChangesNothing(t *testing.T) {
	from := appWithSettings(t)
	from.configDir = t.TempDir()
	if err := from.SaveHost(config.Host{
		Name: "prod", Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := cfgsync.ExportBundle(from.exportableRecords(), "a passphrase long enough", "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	path := filepath.Join(t.TempDir(), "b"+cfgsync.BundleExt)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	to := appWithSettings(t)
	to.configDir = t.TempDir()
	if _, err := to.SyncImportFile(path, "the wrong passphrase"); err == nil {
		t.Error("a wrong passphrase imported the file")
	}
	if len(to.hosts.List()) != 0 {
		t.Errorf("hosts appeared anyway: %+v", to.hosts.List())
	}
}

// Nothing secret is in the exported file.
func TestTheExportedFileHoldsNoSecretAndNoLocalPath(t *testing.T) {
	a := appWithSettings(t)
	a.configDir = t.TempDir()
	if err := a.SaveHost(config.Host{
		Name: "prod-web", Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth:         []config.AuthMethod{config.AuthKey},
		IdentityFile: "/Users/me/.ssh/id_ed25519",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := cfgsync.ExportBundle(a.exportableRecords(), "a passphrase long enough", "d")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, forbidden := range []string{"prod-web", "10.0.0.5", "deploy", "/Users/me", "id_ed25519"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("%q is readable in the exported file", forbidden)
		}
	}

	// And after decrypting, the key's path is still not there: it is a fact about
	// the machine that wrote the file.
	records, _, err := cfgsync.OpenBundle(data, "a passphrase long enough")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	blob, err := records[0].Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "/Users/me") {
		t.Errorf("the key path travelled: %s", blob)
	}
}

// An old backup does not delete anything.
//
// A file is a snapshot, not a sync: it says what existed when it was written and
// nothing about what has happened since. Treating a host's absence — or a
// tombstone inside it — as an instruction would mean opening last month's backup
// removes the servers added since, which is the opposite of what somebody opening
// a backup expects.
func TestAnOldBackupDoesNotDeleteAnything(t *testing.T) {
	a := appWithSettings(t)
	a.configDir = t.TempDir()
	if err := a.SaveHost(config.Host{
		Name: "kept", Hostname: "10.0.0.9", Port: 22, User: "me",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	keptID := a.hosts.List()[0].ID

	// A file holding a tombstone for that host, and nothing else. Nothing this
	// app writes produces one, which is why it is built by hand here: the point
	// is what happens when a file from somewhere else carries one.
	tomb := cfgsync.Record{
		ID: keptID, Rev: 2, UpdatedAt: time.Now().UTC(), UpdatedBy: "device-b",
		Deleted: true, Policy: cfgsync.StrictestPolicy(),
	}
	const pass = "a passphrase long enough"
	data, err := cfgsync.ExportBundle([]cfgsync.Record{tomb}, pass, "device-b")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	path := filepath.Join(t.TempDir(), "old"+cfgsync.BundleExt)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := a.SyncImportFile(path, pass); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, ok := a.hosts.Get(keptID); !ok {
		t.Error("importing a backup deleted a host")
	}
}
