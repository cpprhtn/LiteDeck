package cfgsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// Two machines, one repository (§10.2).
//
// Each device gets its own app data directory with a real hosts.json, a real
// settings.json and its own git working copy; the remote is a bare repository on
// disk. What is simulated is the network and the person, not the storage — the
// failures this is looking for (a policy that travels the wrong way, a rev that
// goes backwards, a record that lands under the wrong name) all happen in the
// files.
type device struct {
	t        *testing.T
	name     string
	dir      string
	hosts    *config.Store
	settings *config.SettingsStore
	store    *Store
	svc      *Service
	keys     map[string][]RecordHostKey // known_hosts, by address
	clock    time.Time
}

// localFiles is Local over this device's real config files.
type localFiles struct{ d *device }

func (l localFiles) Hosts() []config.Host { return l.d.hosts.List() }

func (l localFiles) SaveHost(h config.Host) error { return l.d.hosts.Upsert(h) }

func (l localFiles) DeleteHost(id string) error { return l.d.hosts.Delete(id) }

func (l localFiles) Settings() config.Settings { return l.d.settings.Get() }

func (l localFiles) SetPolicy(hostID string, p RecordPolicy) error {
	s := l.d.settings.Get().MCP
	if s.Hosts == nil {
		s.Hosts = map[string]bool{}
	}
	if s.Write == nil {
		s.Write = map[string]config.MCPWritePolicy{}
	}
	if s.Exec == nil {
		s.Exec = map[string]bool{}
	}
	if s.Delete == nil {
		s.Delete = map[string]bool{}
	}
	s.Hosts[hostID] = p.Shared
	// The expiry is not carried across, and not invented here either: a mode that
	// arrived without one starts with none (§6.2).
	s.Write[hostID] = config.MCPWritePolicy{Mode: p.MCPApproval}
	s.Exec[hostID] = p.ExecEnabled
	s.Delete[hostID] = p.DeleteEnabled
	return l.d.settings.SetMCP(s)
}

func (l localFiles) HostKeys(addr string) ([]RecordHostKey, error) { return l.d.keys[addr], nil }

func (l localFiles) AddHostKey(addr string, k RecordHostKey) error {
	l.d.keys[addr] = append(l.d.keys[addr], k)
	return nil
}

func newDevice(t *testing.T, name, url string, vf VaultFile) *device {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hosts, err := config.Open(dir)
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	d := &device{
		t: t, name: name, dir: dir, hosts: hosts,
		settings: config.OpenSettings(dir),
		keys:     map[string][]RecordHostKey{},
		clock:    time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
	}
	store, err := OpenStore(filepath.Join(dir, "sync"))
	if err != nil {
		t.Fatalf("sync store: %v", err)
	}
	d.store = store
	if err := store.SetConfig(Config{
		Enabled: true, RemoteURL: url, DeviceID: name + "-0000-4000-8000-000000000000",
		DeviceName: name,
	}); err != nil {
		t.Fatalf("config: %v", err)
	}

	backend, err := OpenGit(context.Background(), GitOptions{Dir: store.RepoDir(), URL: url})
	if err != nil && !errors.Is(err, ErrEmptyRemote) {
		t.Fatalf("backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })

	vault, err := vf.Open(goodPass)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	d.svc = NewService(store, localFiles{d}, backend, vault)
	d.svc.now = func() time.Time { d.clock = d.clock.Add(time.Minute); return d.clock }
	return d
}

func (d *device) sync() Result {
	d.t.Helper()
	res, err := d.svc.Sync(context.Background())
	if err != nil {
		d.t.Fatalf("%s sync: %v", d.name, err)
	}
	return res
}

func (d *device) addHost(id, name string) {
	d.t.Helper()
	if err := d.hosts.Upsert(config.Host{
		ID: id, Name: name, Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		d.t.Fatalf("add host: %v", err)
	}
}

func (d *device) host(id string) config.Host {
	d.t.Helper()
	h, ok := d.hosts.Get(id)
	if !ok {
		d.t.Fatalf("%s has no host %s", d.name, id)
	}
	return h
}

// A repository both devices can open.
func seedRepo(t *testing.T) (string, VaultFile) {
	t.Helper()
	url := bareRemote(t)
	vf, _, err := CreateVault(goodPass)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	// One device creates the repository, which is what the wizard does.
	d := newDevice(t, "seed", url, vf)
	if err := d.svc.InitRepository(context.Background(), vf); err != nil {
		t.Fatalf("init: %v", err)
	}
	return url, vf
}

// A host added on one machine turns up on the other.
func TestAHostAddedOnOneDeviceArrivesOnTheOther(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	a.addHost(testID, "prod-web")
	if res := a.sync(); res.Sent != 1 {
		t.Fatalf("a sent %d records", res.Sent)
	}

	res := b.sync()
	if res.Received != 1 {
		t.Fatalf("b received %d records: %+v", res.Received, res)
	}
	got := b.host(testID)
	if got.Name != "prod-web" || got.Hostname != "10.0.0.5" || got.User != "deploy" {
		t.Errorf("b has %+v", got)
	}

	// And the policy did not come with it loosened. A host b has never seen
	// starts at the strictest thing the app has (§6.2).
	s := b.settings.Get().MCP
	if s.Hosts[testID] {
		t.Error("a host that arrived by sync was shared with AI clients")
	}
	if s.Exec[testID] || s.Delete[testID] {
		t.Error("exec or delete started on")
	}
	if s.Write[testID].Mode != ApprovalAsk {
		t.Errorf("approval mode = %q, want the app's default", s.Write[testID].Mode)
	}

	// Syncing again changes nothing and pushes nothing.
	again := b.sync()
	if again.Received != 0 || again.Sent != 0 {
		t.Errorf("a second sync did work: %+v", again)
	}
}

// A loosened policy does not arrive applied — it arrives as a question (§6.2).
func TestLooseningWaitsForAPersonOnEachDevice(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	a.addHost(testID, "prod-web")
	a.sync()
	b.sync()

	// On a, somebody shares the host and turns off the dialogs.
	if err := (localFiles{a}).SetPolicy(testID, RecordPolicy{
		Shared: true, MCPApproval: ApprovalBypass, ExecEnabled: true,
	}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	a.sync()

	res := b.sync()
	if res.Pending == 0 {
		t.Fatal("b applied a loosened policy without asking")
	}
	s := b.settings.Get().MCP
	if s.Hosts[testID] || s.Exec[testID] || s.Write[testID].Mode == ApprovalBypass {
		t.Errorf("b applied the loosening: %+v", s)
	}
	fields := map[string]bool{}
	for _, p := range b.store.Pending() {
		fields[p.Field] = true
		if p.By == "" {
			t.Errorf("the pending item does not say which device changed it: %+v", p)
		}
	}
	for _, want := range []string{FieldShared, FieldApproval, FieldExec} {
		if !fields[want] {
			t.Errorf("%s is not waiting: %+v", want, b.store.Pending())
		}
	}

	// Dismissing one stops it being asked again at that revision.
	p := b.store.Pending()[0]
	if err := b.store.Dismiss(p.RecordID, p.Field, p.Rev); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	after := b.sync()
	for _, q := range b.store.Pending() {
		if q.Field == p.Field && q.Rev == p.Rev {
			t.Errorf("%s was asked again after being dismissed", p.Field)
		}
	}
	if after.Pending != len(b.store.Pending()) {
		t.Errorf("the result says %d pending, the file says %d", after.Pending, len(b.store.Pending()))
	}

	// Withholding it on b must not undo it in the repository: a would see its own
	// change reverted and make it again, forever.
	ra := a.sync()
	if ra.Received != 0 {
		t.Errorf("a received its own withheld change back: %+v", ra)
	}
	if a.settings.Get().MCP.Write[testID].Mode != ApprovalBypass {
		t.Errorf("a's own policy was reverted by b withholding it: %+v", a.settings.Get().MCP)
	}
}

// Tightening needs nobody.
func TestTighteningAppliesByItself(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	a.addHost(testID, "prod-web")
	// Both sides start from a shared host with dialogs off, agreed by a person on
	// each machine.
	for _, d := range []*device{a, b} {
		if err := (localFiles{d}).SetPolicy(testID, RecordPolicy{Shared: true, MCPApproval: ApprovalBypass}); err != nil {
			t.Fatalf("policy: %v", err)
		}
	}
	a.sync()
	b.addHost(testID, "prod-web")
	b.sync()

	// a tightens.
	if err := (localFiles{a}).SetPolicy(testID, RecordPolicy{Shared: false, MCPApproval: ApprovalStrict}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	a.sync()

	res := b.sync()
	if res.Pending != 0 {
		t.Errorf("a tightening was held for approval: %+v", b.store.Pending())
	}
	s := b.settings.Get().MCP
	if s.Hosts[testID] || s.Write[testID].Mode != ApprovalStrict {
		t.Errorf("b did not apply the tightening: %+v", s)
	}
}

// Both devices change the same host at once, and they converge (§8.3).
func TestTwoDevicesChangingOneHostConverge(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	a.addHost(testID, "prod-web")
	a.sync()
	b.sync()

	// Neither has synced since; both rename it.
	ha := a.host(testID)
	ha.Name = "renamed-on-a"
	if err := a.hosts.Upsert(ha); err != nil {
		t.Fatalf("update: %v", err)
	}
	hb := b.host(testID)
	hb.Name = "renamed-on-b"
	hb.Group = "prod"
	if err := b.hosts.Upsert(hb); err != nil {
		t.Fatalf("update: %v", err)
	}

	a.sync()
	// b's push is refused, b pulls, merges and pushes — inside one Sync call.
	res := b.sync()
	if res.Sent == 0 {
		t.Errorf("b did not push its merge: %+v", res)
	}

	// Now both see the same name, whichever one that is.
	a.sync()
	if a.host(testID).Name != b.host(testID).Name {
		t.Errorf("the two devices disagree: %q vs %q",
			a.host(testID).Name, b.host(testID).Name)
	}
	// And the value that lost is in the history rather than gone.
	found := false
	for _, e := range b.store.History(0) {
		for _, c := range e.Conflicts {
			if strings.Contains(c.Dropped, "renamed-on-a") || strings.Contains(c.Dropped, "renamed-on-b") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the overwritten name is nowhere in the sync history")
	}
}

// A deleted host is tombstoned, and the tombstone travels.
func TestADeletedHostIsRemovedEverywhere(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	a.addHost(testID, "prod-web")
	a.sync()
	b.sync()
	if _, ok := b.hosts.Get(testID); !ok {
		t.Fatal("b never received the host")
	}

	// Deleting on a: the host goes, and the record becomes a tombstone. The file
	// stays in the repository, because a file that simply vanished cannot be told
	// apart from a rewound history (§6.4).
	state := a.store.State()
	base := state.Records[testID].Base
	tomb := Tombstone(*base, a.store.Config().DeviceID, a.clock)
	p, err := RecordPath(testID)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	plain, err := tomb.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	vault, err := vf.Open(goodPass)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	blob, err := vault.Encrypt(p, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	backendA, err := OpenGit(context.Background(), GitOptions{Dir: a.store.RepoDir(), URL: url})
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	if _, err := backendA.Pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if err := backendA.CommitAndPush(context.Background(),
		map[string][]byte{p: blob}, nil, "sync: a"); err != nil {
		t.Fatalf("push tombstone: %v", err)
	}

	b.sync()
	if _, ok := b.hosts.Get(testID); ok {
		t.Error("b kept a host that was deleted elsewhere")
	}
	files, err := backendA.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := files[p]; !ok {
		t.Error("the record file was removed from the repository, which looks like a rewind")
	}
}

// A rewound repository is noticed and nothing is applied from it (§6.4).
func TestARewoundRepositoryIsRefused(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	a.addHost(testID, "prod-web")
	// Two revisions, so there is something to roll back to.
	a.sync()
	h := a.host(testID)
	h.Name = "prod-web-2"
	if err := a.hosts.Upsert(h); err != nil {
		t.Fatalf("update: %v", err)
	}
	a.sync()
	b.sync()
	if b.host(testID).Name != "prod-web-2" {
		t.Fatalf("b has %q", b.host(testID).Name)
	}

	// Somebody restores an old copy of the repository: the record comes back at a
	// revision b has already seen.
	old := b.store.State().Records[testID].Base
	rewound := *old
	rewound.Rev = 1
	rewound.Host.Name = "prod-web"
	p, _ := RecordPath(testID)
	plain, err := rewound.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	vault, _ := vf.Open(goodPass)
	blob, err := vault.Encrypt(p, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	backend, err := OpenGit(context.Background(), GitOptions{Dir: a.store.RepoDir(), URL: url})
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	if _, err := backend.Pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if err := backend.CommitAndPush(context.Background(),
		map[string][]byte{p: blob}, nil, "sync: someone"); err != nil {
		t.Fatalf("push: %v", err)
	}

	res := b.sync()
	if b.host(testID).Name != "prod-web-2" {
		t.Errorf("b applied a rewound record: %q", b.host(testID).Name)
	}
	found := false
	for _, w := range res.Warnings {
		if w.Kind == WarnRollback {
			found = true
		}
	}
	if !found {
		t.Errorf("no rollback warning: %+v", res.Warnings)
	}
}

// One unreadable record does not stop the others.
//
// Anybody who can write to the repository could otherwise disable syncing
// altogether by corrupting a single file.
func TestOneCorruptRecordDoesNotStopTheRest(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	const otherID = "9b1c7d2e-1111-4222-8333-444455556666"
	a.addHost(testID, "prod-web")
	a.addHost(otherID, "db")
	a.sync()

	// Overwrite one record with noise, the way a broken tool or a hostile
	// repository host would.
	bad, _ := RecordPath(testID)
	backend, err := OpenGit(context.Background(), GitOptions{Dir: a.store.RepoDir(), URL: url})
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	if _, err := backend.Pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if err := backend.CommitAndPush(context.Background(),
		map[string][]byte{bad: []byte(`{"v":1,"nonce":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","ct":"AAAA"}`)},
		nil, "sync: vandal"); err != nil {
		t.Fatalf("push: %v", err)
	}

	res := b.sync()
	if _, ok := b.hosts.Get(otherID); !ok {
		t.Error("the readable record was not applied")
	}
	warned := false
	for _, w := range res.Warnings {
		if w.Kind == WarnUndecryptable && w.RecordID == testID {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning about the corrupt record: %+v", res.Warnings)
	}
}

// A host imported from ~/.ssh/config stays on the machine it was imported on
// (§2.3).
func TestSSHConfigHostsAreNotSynced(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	b := newDevice(t, "b", url, vf)

	if err := a.hosts.Upsert(config.Host{
		ID: "sshconfig:cpp", Name: "cpp", Hostname: "example.com", Port: 22,
		User: "me", Auth: []config.AuthMethod{config.AuthAgent}, Source: config.SSHConfigSource,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// And a host whose ID predates the UUID migration, which no other machine
	// could arrive at.
	if err := a.hosts.Upsert(config.Host{
		ID: "host-1786033219477533000", Name: "old", Hostname: "10.0.0.9", Port: 22,
		User: "me", Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Not merely "nothing was pushed" — RecordPath refuses those IDs too, so a
	// missing Syncable check would look the same from the outside. The record set
	// itself has to be empty.
	if recs := a.svc.localRecords(a.store.State()); len(recs) != 0 {
		t.Errorf("these hosts became records: %+v", recs)
	}
	if res := a.sync(); res.Sent != 0 {
		t.Errorf("a pushed %d records", res.Sent)
	}
	if res := b.sync(); res.Received != 0 {
		t.Errorf("b received %d records", res.Received)
	}
	if len(b.hosts.List()) != 0 {
		t.Errorf("b has %+v", b.hosts.List())
	}
}

// Two syncs at once do not both run.
func TestOnlyOneSyncRunsAtATime(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	a.svc.mu.Lock()
	a.svc.running = true
	a.svc.mu.Unlock()

	if _, err := a.svc.Sync(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("a second sync started: %v", err)
	}
}

// The local state files are where the design says, and hold no secret (§7.3).
func TestTheLocalFilesHoldNoSecret(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	a.addHost(testID, "prod-web")
	a.sync()

	syncDir := filepath.Join(a.dir, "sync")
	for _, name := range []string{configFile, stateFile, pendingFile, historyFile} {
		if _, err := os.Stat(filepath.Join(syncDir, name)); err != nil {
			t.Errorf("%s is missing: %v", name, err)
		}
	}
	entries, err := os.ReadDir(syncDir)
	if err != nil {
		t.Fatalf("read sync dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(syncDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		// The passphrase and the vault key never touch these files (§6.7 ①).
		if strings.Contains(string(data), goodPass) {
			t.Errorf("%s contains the passphrase", e.Name())
		}
		if info, err := e.Info(); err == nil && info.Mode().Perm() != 0o600 {
			// Windows does not honour this, which is why the vault key is not in a
			// file at all (§6.7 ②). On the systems where it means something, it
			// should be right.
			t.Logf("%s is %v", e.Name(), info.Mode().Perm())
		}
	}
}

// refusingBackend wraps a backend and refuses the first n pushes.
//
// The two-device tests above never collide: each Sync pulls first, so the second
// device merges against what the first one pushed. The collision needs a push that
// is refused *after* a successful pull, which is what happens when the other
// machine pushes in the second between the two — a gap no test can schedule.
type refusingBackend struct {
	Backend
	refuse int
}

func (b *refusingBackend) CommitAndPush(ctx context.Context, writes map[string][]byte, deletes []string, msg string) error {
	if b.refuse > 0 {
		b.refuse--
		return ErrRemoteAhead
	}
	return b.Backend.CommitAndPush(ctx, writes, deletes, msg)
}

// A refused push is retried, and the retry is bounded (§8.2).
func TestARefusedPushIsRetriedAndThenGivenUpOn(t *testing.T) {
	url, vf := seedRepo(t)
	a := newDevice(t, "a", url, vf)
	a.addHost(testID, "prod-web")

	once := &refusingBackend{Backend: a.svc.backend, refuse: 1}
	a.svc.backend = once
	res, err := a.svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("a sync that was refused once did not recover: %v", err)
	}
	if res.Sent != 1 {
		t.Errorf("sent %d records after the retry", res.Sent)
	}

	// And a repository somebody is pushing to continuously is given up on rather
	// than retried forever.
	b := newDevice(t, "b", url, vf)
	b.addHost("9b1c7d2e-1111-4222-8333-444455556666", "db")
	always := &refusingBackend{Backend: b.svc.backend, refuse: maxPushAttempts + 5}
	b.svc.backend = always
	if _, err := b.svc.Sync(context.Background()); !errors.Is(err, ErrRemoteAhead) {
		t.Errorf("a sync that is always refused gave %v", err)
	}
	if always.refuse != 5 {
		t.Errorf("it tried %d times, want %d", maxPushAttempts+5-always.refuse, maxPushAttempts)
	}
}
