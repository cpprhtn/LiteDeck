package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cpprhtn/LiteDeck/internal/cfgsync"
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// Settings sync, as the UI sees it.
//
// The rules and the crypto are in internal/cfgsync; this is the part that knows
// where this machine keeps things — hosts.json, settings.json, known_hosts, the
// OS credential store — and when to run.
//
// # Desktop only, for now
//
// Every binding here refuses in server mode. The sync writes to the machine the
// app runs on: a git working copy, state files, and possibly a private key. On the
// desktop that is the user's own laptop. In server mode it is the server box, and
// /rpc would hand "set up a sync of my host list, on the server, with my
// passphrase" to anybody who can log into the web UI. A server joining a sync is
// its own feature and is not this one.
//
// # Not reachable from MCP
//
// There is no sync tool, and there cannot be one: the pending list is the place a
// person decides to give an AI client more room, and a model that could press that
// button would be a model approving its own permissions. TestMCPCannotReachSync
// pins it.

// SyncView is the sync's state, for the settings screen (§9.1).
type SyncView struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	RemoteURL string `json:"remoteUrl,omitempty"`
	AuthKind  string `json:"authKind,omitempty"`
	DeviceID  string `json:"deviceId,omitempty"`
	// Unlocked is whether the vault is open in this process. When it is not, the
	// UI asks for the passphrase before anything can sync.
	Unlocked bool `json:"unlocked"`
	// CanRemember is false where this machine has no credential store, which also
	// disables the toggle rather than offering a promise it cannot keep (§6.7 ①).
	CanRemember bool   `json:"canRemember"`
	Remember    bool   `json:"remember"`
	LastSync    int64  `json:"lastSync,omitempty"`
	Head        string `json:"head,omitempty"`
	Pending     int    `json:"pending"`
	// Syncing is true while a pass is running, so the button can say so.
	Syncing bool   `json:"syncing"`
	Error   string `json:"error,omitempty"`
	// AgentAvailable is false on Windows, where the OpenSSH agent speaks over a
	// named pipe this code does not use — so that choice is hidden rather than
	// offered and then failing (§6.7 ⑥).
	AgentAvailable bool `json:"agentAvailable"`
}

// syncRuntime is everything the sync needs, built once the vault is open.
type syncRuntime struct {
	store   *cfgsync.Store
	backend *cfgsync.GitBackend
	svc     *cfgsync.Service
	vault   *cfgsync.Vault
}

// syncState is the app's sync half.
type syncState struct {
	mu      sync.Mutex
	store   *cfgsync.Store
	rt      *syncRuntime
	running bool
	lastErr string
	// debounce collapses a burst of local edits into one sync (§8.1).
	debounce *time.Timer
	stop     chan struct{}
}

// syncLocal is cfgsync.Local over this machine's own files.
type syncLocal struct{ a *App }

func (l syncLocal) Hosts() []config.Host {
	if l.a.hosts == nil {
		return nil
	}
	return l.a.hosts.List()
}

func (l syncLocal) SaveHost(h config.Host) error {
	if l.a.hosts == nil {
		return errors.New("app: no host list")
	}
	return l.a.hosts.Upsert(h)
}

func (l syncLocal) DeleteHost(id string) error {
	if l.a.hosts == nil {
		return nil
	}
	return l.a.hosts.Delete(id)
}

func (l syncLocal) Settings() config.Settings {
	if l.a.settings == nil {
		return config.Settings{}
	}
	return l.a.settings.Get()
}

// SetPolicy writes one host's policy into settings.json.
//
// The expiry is not touched beyond being dropped: a mode that arrived from another
// machine starts with no countdown, and the countdown restarts here when somebody
// on this machine chooses it (§6.2).
func (l syncLocal) SetPolicy(hostID string, p cfgsync.RecordPolicy) error {
	if l.a.settings == nil {
		return nil
	}
	m := l.a.settings.Get().MCP
	if m.Hosts == nil {
		m.Hosts = map[string]bool{}
	}
	if m.Write == nil {
		m.Write = map[string]config.MCPWritePolicy{}
	}
	if m.Exec == nil {
		m.Exec = map[string]bool{}
	}
	if m.Delete == nil {
		m.Delete = map[string]bool{}
	}
	m.Hosts[hostID] = p.Shared
	mode := p.MCPApproval
	if mode == "" {
		mode = WriteAsk
	}
	until := int64(0)
	if mode != WriteAsk {
		// A relaxed mode needs an expiry, and the one that arrived (if any) was
		// not carried. Start the app's own window now, on this clock.
		until = time.Now().Add(maxWriteWindow).Unix()
	}
	m.Write[hostID] = config.MCPWritePolicy{Mode: mode, Until: until}
	m.Exec[hostID] = p.ExecEnabled
	m.Delete[hostID] = p.DeleteEnabled
	return l.a.settings.SetMCP(m)
}

func (l syncLocal) HostKeys(addr string) ([]cfgsync.RecordHostKey, error) {
	if l.a.configDir == "" {
		return nil, nil
	}
	keys, err := sshcore.TrustedKeys(config.KnownHostsPath(l.a.configDir), addr)
	if err != nil {
		return nil, err
	}
	out := make([]cfgsync.RecordHostKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, cfgsync.RecordHostKey{
			Alg:         k.Type(),
			Key:         base64.StdEncoding.EncodeToString(k.Marshal()),
			Fingerprint: ssh.FingerprintSHA256(k),
		})
	}
	return out, nil
}

func (l syncLocal) AddHostKey(addr string, k cfgsync.RecordHostKey) error {
	if l.a.configDir == "" {
		return nil
	}
	blob, err := base64.StdEncoding.DecodeString(k.Key)
	if err != nil {
		return fmt.Errorf("app: host key for %s is not base64: %w", addr, err)
	}
	key, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return fmt.Errorf("app: host key for %s does not parse: %w", addr, err)
	}
	// The fingerprint is checked against the key rather than trusted: it is what
	// the pending list showed the person, and a record whose two halves disagree
	// would have them approving one key and trusting another.
	if k.Fingerprint != "" && ssh.FingerprintSHA256(key) != k.Fingerprint {
		return fmt.Errorf("app: the host key for %s does not match the fingerprint "+
			"that was shown", addr)
	}
	return sshcore.TrustKey(config.KnownHostsPath(l.a.configDir), addr, key)
}

// syncDir is where the local sync files live (§7.3).
func (a *App) syncDir() string {
	if a.configDir == "" {
		return ""
	}
	return filepath.Join(a.configDir, "sync")
}

// syncNotHere is the refusal in server mode.
func (a *App) syncNotHere() error {
	return i18n.Errorf("서버 모드에서는 설정 동기화를 쓸 수 없습니다")
}

// openSyncStore loads the local sync files, once.
func (a *App) openSyncStore() (*cfgsync.Store, error) {
	a.sync.mu.Lock()
	defer a.sync.mu.Unlock()
	if a.sync.store != nil {
		return a.sync.store, nil
	}
	dir := a.syncDir()
	if dir == "" {
		return nil, errors.New("app: no config directory")
	}
	store, err := cfgsync.OpenStore(dir)
	if err != nil {
		return nil, err
	}
	a.sync.store = store
	return store, nil
}

// SyncState is what the settings screen draws (§9.1).
func (a *App) SyncState() (SyncView, error) {
	if a.headless {
		return SyncView{}, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncView{}, err
	}
	cfg := store.Config()
	st := store.State()

	a.sync.mu.Lock()
	unlocked := a.sync.rt != nil
	running := a.sync.running
	lastErr := a.sync.lastErr
	a.sync.mu.Unlock()

	canRemember := a.secrets != nil && a.secrets.Available()
	v := SyncView{
		Available:      true,
		Enabled:        cfg.Enabled,
		RemoteURL:      cfgsync.Redact(cfg.RemoteURL),
		AuthKind:       cfg.AuthKind,
		DeviceID:       cfg.DeviceID,
		Unlocked:       unlocked,
		CanRemember:    canRemember,
		Remember:       cfg.Remember && canRemember,
		Head:           st.Head,
		Pending:        len(store.Pending()),
		Syncing:        running,
		Error:          lastErr,
		AgentAvailable: agentAvailableHere(),
	}
	if !st.LastSync.IsZero() {
		v.LastSync = st.LastSync.Unix()
	}
	return v, nil
}

// SyncPending is the list of changes waiting for this person (§9.1).
func (a *App) SyncPending() ([]cfgsync.PendingChange, error) {
	if a.headless {
		return nil, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return nil, err
	}
	return store.Pending(), nil
}

// SyncHistory is the sync log, newest first (§9.1).
func (a *App) SyncHistory(limit int) ([]cfgsync.HistoryEntry, error) {
	if a.headless {
		return nil, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	return store.History(limit), nil
}

// SyncCreate sets up a new sync repository (§9.1, the first wizard).
//
// The order matters: connect, check that the remote is empty or already ours,
// *then* create the vault. Creating it first would leave the user having chosen a
// passphrase for a repository this never managed to reach.
func (a *App) SyncCreate(remoteURL, authKind, passphrase string, remember bool) (SyncView, error) {
	if a.headless {
		return SyncView{}, a.syncNotHere()
	}
	if err := cfgsync.CheckPassphrase(passphrase); err != nil {
		return SyncView{}, err
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncView{}, err
	}
	cfg, err := a.prepareSyncConfig(store, remoteURL, authKind, remember)
	if err != nil {
		return SyncView{}, err
	}

	backend, err := a.openSyncBackend(cfg)
	if err != nil {
		return SyncView{}, err
	}
	files, err := backend.ReadAll()
	if err != nil {
		return SyncView{}, err
	}
	if err := cfgsync.IsSyncRepository(files); err != nil {
		return SyncView{}, err
	}
	if _, already := files[cfgsync.VaultPath]; already {
		return SyncView{}, i18n.Errorf("이 저장소에는 이미 동기화가 설정되어 있습니다 — 「기존 동기화에 합류」를 쓰세요")
	}

	vf, vault, err := cfgsync.CreateVault(passphrase)
	if err != nil {
		return SyncView{}, err
	}
	svc := cfgsync.NewService(store, syncLocal{a}, backend, vault)
	if err := svc.InitRepository(context.Background(), vf); err != nil {
		return SyncView{}, err
	}

	a.setSyncRuntime(store, backend, svc, vault)
	a.rememberVaultKey(vault, remember)
	if _, err := a.SyncNow(); err != nil {
		return a.SyncState()
	}
	return a.SyncState()
}

// SyncJoin joins an existing repository (§9.1, the second wizard).
func (a *App) SyncJoin(remoteURL, authKind, passphrase string, remember bool) (SyncView, error) {
	if a.headless {
		return SyncView{}, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncView{}, err
	}
	cfg, err := a.prepareSyncConfig(store, remoteURL, authKind, remember)
	if err != nil {
		return SyncView{}, err
	}
	backend, err := a.openSyncBackend(cfg)
	if err != nil {
		return SyncView{}, err
	}
	if _, err := backend.Pull(context.Background()); err != nil && !errors.Is(err, cfgsync.ErrEmptyRemote) {
		return SyncView{}, err
	}
	files, err := backend.ReadAll()
	if err != nil {
		return SyncView{}, err
	}
	raw, ok := files[cfgsync.VaultPath]
	if !ok {
		return SyncView{}, i18n.Errorf("이 저장소에는 동기화 설정이 없습니다 — 「새 동기화 만들기」를 쓰세요")
	}
	var vf cfgsync.VaultFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		return SyncView{}, fmt.Errorf("app: vault.json: %w", err)
	}
	vault, err := vf.Open(passphrase)
	if err != nil {
		return SyncView{}, err
	}

	svc := cfgsync.NewService(store, syncLocal{a}, backend, vault)
	a.setSyncRuntime(store, backend, svc, vault)
	a.rememberVaultKey(vault, remember)
	if _, err := a.SyncNow(); err != nil {
		return a.SyncState()
	}
	return a.SyncState()
}

// SyncUnlock opens the vault with a passphrase, for a machine that is not
// remembering the key (§6.7 ①).
func (a *App) SyncUnlock(passphrase string, remember bool) (SyncView, error) {
	if a.headless {
		return SyncView{}, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncView{}, err
	}
	cfg := store.Config()
	if cfg.RemoteURL == "" {
		return SyncView{}, i18n.Errorf("동기화가 설정되지 않았습니다")
	}
	return a.SyncJoin(cfg.RemoteURL, cfg.AuthKind, passphrase, remember)
}

// SyncNow runs one pass (§8.1).
func (a *App) SyncNow() (SyncResultView, error) {
	if a.headless {
		return SyncResultView{}, a.syncNotHere()
	}
	a.sync.mu.Lock()
	rt := a.sync.rt
	a.sync.mu.Unlock()
	if rt == nil {
		return SyncResultView{}, i18n.Errorf("동기화 저장소가 잠겨 있습니다 — 패스프레이즈를 입력하세요")
	}

	a.sync.mu.Lock()
	a.sync.running = true
	a.sync.mu.Unlock()
	a.emit("sync:state", nil)

	res, err := rt.svc.Sync(context.Background())

	a.sync.mu.Lock()
	a.sync.running = false
	if err != nil && !errors.Is(err, cfgsync.ErrBusy) {
		// Redacted, because the URL in it may carry a token and this string goes
		// to the screen and into bug reports (§6.6).
		a.sync.lastErr = cfgsync.Redact(err.Error())
	} else if err == nil {
		a.sync.lastErr = ""
	}
	a.sync.mu.Unlock()

	view := SyncResultView{
		Received: res.Received, Sent: res.Sent, Pending: res.Pending,
		Warnings: res.Warnings, Conflicts: res.Conflicts,
	}
	if err != nil {
		view.Error = cfgsync.Redact(err.Error())
	}
	a.emit("sync:result", view)
	a.emit("sync:state", nil)
	return view, err
}

// SyncResultView is one sync's outcome, for the toast (§9.1).
type SyncResultView struct {
	Received  int                `json:"received"`
	Sent      int                `json:"sent"`
	Pending   int                `json:"pending"`
	Warnings  []cfgsync.Warning  `json:"warnings,omitempty"`
	Conflicts []cfgsync.Conflict `json:"conflicts,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// SyncApplyPending applies one withheld change, because somebody pressed the
// button (§6.2).
//
// This is the only path by which a policy gets looser through syncing, and it is
// deliberately a person on this machine. There is no MCP tool for it.
func (a *App) SyncApplyPending(recordID, field string) error {
	if a.headless {
		return a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return err
	}
	st := store.State()
	rs := st.Records[recordID]
	if rs == nil || rs.Base == nil {
		return i18n.Errorf("이 항목은 더 이상 대기 중이 아닙니다")
	}
	want := rs.Base.Policy
	current := cfgsync.PolicyFromSettings(syncLocal{a}.Settings(), recordID)

	// One field, not the record: the others may be waiting on their own decisions.
	next := current
	switch field {
	case cfgsync.FieldShared:
		next.Shared = want.Shared
	case cfgsync.FieldApproval:
		next.MCPApproval = want.MCPApproval
	case cfgsync.FieldExec:
		next.ExecEnabled = want.ExecEnabled
	case cfgsync.FieldDelete:
		next.DeleteEnabled = want.DeleteEnabled
	case cfgsync.FieldHostKeys:
		return a.applyPendingHostKeys(recordID, *rs.Base)
	default:
		return i18n.Errorf("알 수 없는 항목입니다: %s", field)
	}
	if err := (syncLocal{a}).SetPolicy(recordID, next); err != nil {
		return err
	}
	// Recorded as applied, so the next sync does not read the difference between
	// settings.json and the record as somebody's local change (§6.2).
	if err := a.markSyncApplied(recordID, next); err != nil {
		return err
	}
	if _, err := a.SyncNow(); err != nil {
		return err
	}
	return nil
}

// applyPendingHostKeys trusts the host keys a record carries, for the address it
// names (§6.3).
func (a *App) applyPendingHostKeys(recordID string, base cfgsync.Record) error {
	for _, k := range base.HostKeys {
		if err := (syncLocal{a}).AddHostKey(base.Addr(), k); err != nil {
			return err
		}
	}
	store, err := a.openSyncStore()
	if err != nil {
		return err
	}
	if err := store.Dismiss(recordID, cfgsync.FieldHostKeys, base.Rev); err != nil {
		return err
	}
	_, err = a.SyncNow()
	return err
}

// SyncDismissPending keeps this machine's value and stops asking about that
// revision (§9.1).
func (a *App) SyncDismissPending(recordID, field string, rev int64) error {
	if a.headless {
		return a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return err
	}
	if err := store.Dismiss(recordID, field, rev); err != nil {
		return err
	}
	a.emit("sync:state", nil)
	return nil
}

// markSyncApplied records that this machine now holds p for that record.
func (a *App) markSyncApplied(recordID string, p cfgsync.RecordPolicy) error {
	store, err := a.openSyncStore()
	if err != nil {
		return err
	}
	st := store.State()
	rs := st.Records[recordID]
	if rs == nil {
		rs = &cfgsync.RecordState{}
		st.Records[recordID] = rs
	}
	applied := p
	rs.Applied = &applied
	return store.SetState(st)
}

// SyncChangePassphrase rewraps the vault key (§4.4).
func (a *App) SyncChangePassphrase(oldPass, newPass string) error {
	if a.headless {
		return a.syncNotHere()
	}
	a.sync.mu.Lock()
	rt := a.sync.rt
	a.sync.mu.Unlock()
	if rt == nil {
		return i18n.Errorf("동기화 저장소가 잠겨 있습니다 — 패스프레이즈를 입력하세요")
	}
	if _, err := rt.backend.Pull(context.Background()); err != nil && !errors.Is(err, cfgsync.ErrEmptyRemote) {
		return err
	}
	files, err := rt.backend.ReadAll()
	if err != nil {
		return err
	}
	raw, ok := files[cfgsync.VaultPath]
	if !ok {
		return i18n.Errorf("이 저장소에는 동기화 설정이 없습니다")
	}
	var vf cfgsync.VaultFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		return fmt.Errorf("app: vault.json: %w", err)
	}
	changed, err := vf.ChangePassphrase(oldPass, newPass)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(changed, "", "  ")
	if err != nil {
		return fmt.Errorf("app: encode vault.json: %w", err)
	}
	return rt.backend.CommitAndPush(context.Background(),
		map[string][]byte{cfgsync.VaultPath: append(out, '\n')}, nil, "sync: passphrase")
}

// SyncDisable stops syncing on this machine, and touches nothing remote (§9.1).
//
// The hosts stay, the records stay in the repository, and the vault key is
// forgotten. Somebody turning this off is saying "not from this machine", which is
// not the same as "delete my host list from the other two".
func (a *App) SyncDisable() (SyncView, error) {
	if a.headless {
		return SyncView{}, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncView{}, err
	}
	cfg := store.Config()
	cfg.Enabled = false
	if err := store.SetConfig(cfg); err != nil {
		return SyncView{}, err
	}
	a.sync.mu.Lock()
	if a.sync.rt != nil {
		_ = a.sync.rt.backend.Close()
	}
	a.sync.rt = nil
	a.sync.mu.Unlock()
	if a.secrets != nil && a.secrets.Available() {
		_ = a.secrets.Delete(secret.SyncAccount, secret.KindSyncVault)
	}
	a.emit("sync:state", nil)
	return a.SyncState()
}

// SyncSetRemember turns the vault-key cache on or off (§7.3).
func (a *App) SyncSetRemember(remember bool) (SyncView, error) {
	if a.headless {
		return SyncView{}, a.syncNotHere()
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncView{}, err
	}
	if remember && (a.secrets == nil || !a.secrets.Available()) {
		return SyncView{}, i18n.Errorf("이 기기에는 자격 증명 저장소가 없어 기억할 수 없습니다")
	}
	cfg := store.Config()
	cfg.Remember = remember
	if err := store.SetConfig(cfg); err != nil {
		return SyncView{}, err
	}
	a.sync.mu.Lock()
	rt := a.sync.rt
	a.sync.mu.Unlock()
	if remember && rt != nil {
		a.rememberVaultKey(rt.vault, true)
	}
	if !remember && a.secrets != nil && a.secrets.Available() {
		_ = a.secrets.Delete(secret.SyncAccount, secret.KindSyncVault)
	}
	return a.SyncState()
}

// prepareSyncConfig writes the configuration a wizard collected, giving this
// machine a device ID if it has none.
func (a *App) prepareSyncConfig(store *cfgsync.Store, remoteURL, authKind string, remember bool) (cfgsync.Config, error) {
	cfg := store.Config()
	cfg.RemoteURL = remoteURL
	cfg.Enabled = true
	if authKind != "" {
		cfg.AuthKind = authKind
	}
	if cfg.AuthKind == cfgsync.AuthAgent && !agentAvailableHere() {
		return cfg, i18n.Errorf("이 기기에서는 ssh-agent 방식을 쓸 수 없습니다")
	}
	if cfg.DeviceID == "" {
		id, err := newDeviceID()
		if err != nil {
			return cfg, err
		}
		cfg.DeviceID = id
	}
	cfg.Remember = remember && a.secrets != nil && a.secrets.Available()
	if err := store.SetConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (a *App) setSyncRuntime(store *cfgsync.Store, backend *cfgsync.GitBackend, svc *cfgsync.Service, vault *cfgsync.Vault) {
	a.sync.mu.Lock()
	defer a.sync.mu.Unlock()
	if a.sync.rt != nil && a.sync.rt.backend != backend {
		_ = a.sync.rt.backend.Close()
	}
	a.sync.store = store
	a.sync.rt = &syncRuntime{store: store, backend: backend, svc: svc, vault: vault}
}

// rememberVaultKey caches the key where the user asked for it and the machine can
// hold it (§6.7 ①).
func (a *App) rememberVaultKey(v *cfgsync.Vault, remember bool) {
	if v == nil || !remember || a.secrets == nil || !a.secrets.Available() {
		return
	}
	_ = a.secrets.Set(secret.SyncAccount, secret.KindSyncVault, v.KeyBase64())
}

// newDeviceID is this machine's name inside the sync, as a UUID.
//
// Not the hostname: it goes into commit messages and into every record's
// updated_by, and a repository hosted somewhere the user did not think about
// should not be a list of their computers' names.
func newDeviceID() (string, error) { return config.NewUUID() }

// When a sync happens (§8.1).
//
// Four triggers: once at start, the button, ten seconds after a local edit, and
// every five minutes while the app is open. Offline is not an error state — the
// status line says what happened and the next trigger tries again.
const (
	syncStartupDelay = 5 * time.Second
	syncInterval     = 5 * time.Minute
	syncDebounce     = 10 * time.Second
)

// startSync brings the sync up at launch, if it is set up and this machine can
// open the vault without asking.
//
// Never asks for anything. A machine that is not remembering the vault key starts
// locked, and the settings screen says so — a passphrase dialog at launch, before
// anybody has said they want to sync anything, is a dialog people learn to dismiss.
func (a *App) startSync() {
	if a.headless || a.configDir == "" {
		return
	}
	store, err := a.openSyncStore()
	if err != nil {
		return
	}
	cfg := store.Config()
	if !cfg.Enabled || cfg.RemoteURL == "" {
		return
	}
	if a.secrets == nil || !a.secrets.Available() || !cfg.Remember {
		// Locked. Nothing is wrong; the vault key is simply not on this machine.
		a.emit("sync:state", nil)
		return
	}
	cached, err := a.secrets.Get(secret.SyncAccount, secret.KindSyncVault)
	if err != nil || cached == "" {
		a.emit("sync:state", nil)
		return
	}
	vault, err := cfgsync.VaultFromKeyBase64(cached)
	if err != nil {
		a.emit("sync:state", nil)
		return
	}
	backend, err := a.openSyncBackend(cfg)
	if err != nil {
		a.sync.mu.Lock()
		a.sync.lastErr = cfgsync.Redact(err.Error())
		a.sync.mu.Unlock()
		a.emit("sync:state", nil)
		return
	}
	a.setSyncRuntime(store, backend, cfgsync.NewService(store, syncLocal{a}, backend, vault), vault)

	stop := make(chan struct{})
	a.sync.mu.Lock()
	a.sync.stop = stop
	a.sync.mu.Unlock()

	go func() {
		// Behind the first paint and the first connection. A sync racing the
		// window open would make the app feel slower for a feature nobody is
		// looking at yet.
		select {
		case <-time.After(syncStartupDelay):
		case <-stop:
			return
		}
		_, _ = a.SyncNow()
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = a.SyncNow()
			case <-stop:
				return
			}
		}
	}()
}

// stopSync ends the background loop.
func (a *App) stopSync() {
	a.sync.mu.Lock()
	stop := a.sync.stop
	a.sync.stop = nil
	if a.sync.debounce != nil {
		a.sync.debounce.Stop()
		a.sync.debounce = nil
	}
	a.sync.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// syncSoon schedules a sync after a local change (§8.1).
//
// Debounced, because a person adding a host types in a form: every keystroke that
// saves would otherwise be a commit. Ten seconds after they stop.
func (a *App) syncSoon() {
	a.sync.mu.Lock()
	defer a.sync.mu.Unlock()
	if a.sync.rt == nil {
		return
	}
	if a.sync.debounce != nil {
		a.sync.debounce.Stop()
	}
	a.sync.debounce = time.AfterFunc(syncDebounce, func() { _, _ = a.SyncNow() })
}

// SyncProbeResult is what the setup screen found at an address, before anything
// is written to it (§9.1).
//
// The whole point is to fail here, where there is a sentence to put beside the
// failure and the user is still on the step that caused it. A setup that only
// finds out at the first push reports "could not sync" on a different screen, an
// hour later, about a deploy key they forgot to tick a box on.
type SyncProbeResult struct {
	// Kind is one of cfgsync.Remote*: empty, sync, other, denied, unreachable.
	Kind string `json:"kind"`
	// Normalized is the URL LiteDeck will actually use, which is not always the
	// one that was pasted.
	Normalized string `json:"normalized"`
	// DeployKeysURL is where to register the key, for a forge this knows.
	DeployKeysURL string `json:"deployKeysUrl,omitempty"`
	// Hosts is how many host records the repository holds, when it is a sync
	// repository. "You will receive 6 hosts" is the confirmation somebody needs
	// before joining, and it needs no passphrase to count.
	Hosts int `json:"hosts"`
	// Detail is the underlying error, redacted, for the cases where the kind is
	// not enough.
	Detail string `json:"detail,omitempty"`
}

// SyncProbe looks at a remote without writing to it.
func (a *App) SyncProbe(remoteURL, authKind string) (SyncProbeResult, error) {
	if a.headless {
		return SyncProbeResult{}, a.syncNotHere()
	}
	normalized, err := cfgsync.NormalizeRemote(remoteURL, authKind)
	if err != nil {
		return SyncProbeResult{}, err
	}
	res := SyncProbeResult{
		Normalized:    normalized,
		DeployKeysURL: cfgsync.DeployKeysURL(remoteURL),
	}

	auth, err := a.syncAuth(cfgsync.Config{AuthKind: authKind, RemoteURL: normalized})
	if err != nil {
		return res, err
	}

	// A scratch working copy, thrown away afterwards. Cloning into the real one
	// would leave a half-set-up sync behind every time somebody mistypes an
	// address.
	dir, err := os.MkdirTemp("", "litedeck-sync-probe-")
	if err != nil {
		return res, fmt.Errorf("app: probe: %w", err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(context.Background(), syncProbeTimeout)
	defer cancel()

	backend, err := cfgsync.OpenGit(ctx, cfgsync.GitOptions{Dir: dir, URL: normalized, Auth: auth})
	switch {
	case err == nil:
	case errors.Is(err, cfgsync.ErrEmptyRemote):
		res.Kind = cfgsync.RemoteEmpty
		return res, nil
	default:
		res.Kind = cfgsync.ClassifyRemoteError(err)
		res.Detail = cfgsync.Redact(err.Error())
		return res, nil
	}
	defer backend.Close()

	files, err := backend.ReadAll()
	if err != nil {
		res.Kind = cfgsync.RemoteUnreachable
		res.Detail = cfgsync.Redact(err.Error())
		return res, nil
	}
	switch {
	case len(files) == 0:
		res.Kind = cfgsync.RemoteEmpty
	case len(files[cfgsync.VaultPath]) > 0:
		res.Kind = cfgsync.RemoteSync
		for p := range files {
			if _, ok := cfgsync.RecordID(p); ok {
				res.Hosts++
			}
		}
	default:
		res.Kind = cfgsync.RemoteOther
	}
	return res, nil
}

// syncProbeTimeout bounds the connection test.
//
// Somebody is watching this one, which is the difference from every other network
// call in the sync: fifteen seconds of a spinner is already too long to believe
// the button did anything.
const syncProbeTimeout = 15 * time.Second
