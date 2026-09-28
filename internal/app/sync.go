package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	wr "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"

	"github.com/cpprhtn/LiteDeck/internal/cfgsync"
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// Carrying settings between machines as one encrypted file.
//
// # Why a file and not a service
//
// Somebody who runs two or three servers wants the same host list on their laptop
// and their desktop, and every product answer to that is an account: sign in here,
// sign in there, and a company holds the list. LiteDeck asks nobody to sign up for
// anything (principle 4).
//
// A git repository was the first answer, and it was the wrong one for most people:
// "make a private repository and register a deploy key with write access" is four
// sentences of vocabulary before the feature does anything. That work is on the
// `sync-git-repo` branch and may come back.
//
// What everybody does have is a folder that syncs itself — Google Drive, Dropbox,
// iCloud, a stick, an email to yourself. So: one encrypted file, and LiteDeck
// neither knows nor cares where it goes.
//
// # What travels
//
// Where each server is, who to log in as, which key by fingerprint, the host keys
// that have been trusted, and how much an AI client is allowed to do. Not the
// password, the key passphrase, the sudo password, the private key or its path,
// or the MCP token — those are secrets or facts about one machine (§3.2).
//
// # Importing is not restoring
//
// A policy in the file that is looser than this machine's is withheld and goes to
// the pending list (§6.2). Whoever exported the file decided what an AI client may
// do to those servers, and they were not sitting at this desk. The same reasoning
// makes a tombstone in a file mean nothing: it is a snapshot of one moment, and
// opening last month's backup must not delete the servers added since.
//
// # Desktop only, and never from MCP
//
// Every binding here refuses in server mode: it reads and writes files on the
// machine the app runs on, which in server mode is the server box. And there is no
// MCP tool for any of it — the pending list is where a person gives an AI client
// more room, and a model that could press that button would be approving its own
// permissions. TestMCPCannotReachSync pins both.

// syncState is what the app holds for this.
type syncState struct {
	mu    sync.Mutex
	store *cfgsync.Store
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

// SyncResultView is one sync's outcome, for the toast (§9.1).
type SyncResultView struct {
	Received int               `json:"received"`
	Sent     int               `json:"sent"`
	Pending  int               `json:"pending"`
	Warnings []cfgsync.Warning `json:"warnings,omitempty"`
	Error    string            `json:"error,omitempty"`
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
	current := cfgsync.PolicyFromSettings((syncLocal{a}).Settings(), recordID)

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
	// Recorded as applied, so a later import does not read the difference between
	// settings.json and the file as somebody's local change (§6.2).
	if err := a.markSyncApplied(recordID, next); err != nil {
		return err
	}
	// And the question stops being asked, at this revision. A later one asks
	// again, because that is a new decision by somebody.
	if err := store.Dismiss(recordID, field, rs.Base.Rev); err != nil {
		return err
	}
	a.emit("sync:state", nil)
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
	a.emit("sync:state", nil)
	return nil
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

// newDeviceID is this machine's name inside the sync, as a UUID.
//
// Not the hostname: it goes into commit messages and into every record's
// updated_by, and a repository hosted somewhere the user did not think about
// should not be a list of their computers' names.
func newDeviceID() (string, error) { return config.NewUUID() }

// Settings as one encrypted file, for people with no git repository.
//
// The sync's whole setup — make a repository, register a deploy key with write
// access — is written for somebody who has done it before. A file is not: it goes
// in Google Drive, Dropbox, iCloud, on a stick, or in an email to yourself, and
// every one of those is something people already have.
//
// What it is not is a sync. Nothing merges, nothing notices two machines editing
// at once; it is a snapshot made here and opened there. Where the two paths
// overlap they behave the same, deliberately: an incoming policy that is looser
// than this machine's is withheld and goes to the pending list, whether it came
// out of a repository or off a stick. Whoever wrote the file decided what an AI
// client may do to these servers, and they were not sitting at this desk.

// SyncExportResult says where the file went.
type SyncExportResult struct {
	Path  string `json:"path"`
	Hosts int    `json:"hosts"`
}

// SyncExportFile writes every syncable host to one encrypted file (§9.1).
func (a *App) SyncExportFile(passphrase string) (SyncExportResult, error) {
	if a.headless {
		return SyncExportResult{}, a.syncNotHere()
	}
	if a.ctx == nil {
		return SyncExportResult{}, errors.New("app: no window")
	}
	if err := cfgsync.CheckPassphrase(passphrase); err != nil {
		return SyncExportResult{}, err
	}

	records := a.exportableRecords()
	if len(records) == 0 {
		return SyncExportResult{}, i18n.Errorf("내보낼 호스트가 없습니다")
	}

	// The device ID is written into the file. One is minted here if this machine
	// has never synced, so two backups from two machines can be told apart
	// without either of them having to be set up for syncing.
	store, err := a.openSyncStore()
	if err != nil {
		return SyncExportResult{}, err
	}
	cfg := store.Config()
	if cfg.DeviceID == "" {
		if id, err := newDeviceID(); err == nil {
			cfg.DeviceID = id
			_ = store.SetConfig(cfg)
		}
	}

	data, err := cfgsync.ExportBundle(records, passphrase, cfg.DeviceID)
	if err != nil {
		return SyncExportResult{}, err
	}

	path, err := wr.SaveFileDialog(a.ctx, wr.SaveDialogOptions{
		Title:           i18n.S("설정 파일 저장"),
		DefaultFilename: cfgsync.BundleName(time.Now()),
		Filters: []wr.FileFilter{{
			DisplayName: i18n.S("LiteDeck 설정 파일"),
			Pattern:     "*" + cfgsync.BundleExt,
		}},
	})
	if err != nil {
		return SyncExportResult{}, err
	}
	if path == "" {
		// The user closed the dialog. Not an error, and not a file.
		return SyncExportResult{}, nil
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return SyncExportResult{}, fmt.Errorf("app: write %s: %w", path, err)
	}
	return SyncExportResult{Path: path, Hosts: len(records)}, nil
}

// SyncPickFile opens the file chooser and returns the path, without reading it.
//
// Separate from the import so the passphrase is asked for after the file is
// chosen: a dialog that wants a passphrase before it knows which file is a dialog
// people answer with the wrong one.
func (a *App) SyncPickFile() (string, error) {
	if a.headless {
		return "", a.syncNotHere()
	}
	if a.ctx == nil {
		return "", errors.New("app: no window")
	}
	return wr.OpenFileDialog(a.ctx, wr.OpenDialogOptions{
		Title: i18n.S("설정 파일 열기"),
		Filters: []wr.FileFilter{{
			DisplayName: i18n.S("LiteDeck 설정 파일"),
			Pattern:     "*" + cfgsync.BundleExt,
		}},
	})
}

// SyncFileEntry is one host in a file, for the preview.
type SyncFileEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Addr string `json:"addr"`
	// State is "new", "same" or "changed" against what this machine has.
	State string `json:"state"`
	// Loosens is true where the file's policy is looser than this machine's, so
	// importing it puts that host in the pending list rather than applying it.
	Loosens bool `json:"loosens"`
}

// SyncFilePreview says what is in a file before anything is applied.
type SyncFilePreview struct {
	CreatedAt int64           `json:"createdAt"`
	Device    string          `json:"device,omitempty"`
	Hosts     []SyncFileEntry `json:"hosts"`
}

// SyncPreviewFile decrypts a file and reports what importing it would do.
func (a *App) SyncPreviewFile(path, passphrase string) (SyncFilePreview, error) {
	records, meta, err := a.readBundle(path, passphrase)
	if err != nil {
		return SyncFilePreview{}, err
	}
	local := map[string]config.Host{}
	for _, h := range a.hosts.List() {
		local[h.ID] = h
	}
	settings := (syncLocal{a}).Settings()

	out := SyncFilePreview{CreatedAt: meta.CreatedAt.Unix(), Device: meta.Device}
	for _, r := range records {
		entry := SyncFileEntry{ID: r.ID, Name: r.Host.Name, Addr: r.Addr(), State: "new"}
		if h, ok := local[r.ID]; ok {
			entry.State = "changed"
			// Compared as the record would leave it, not field by field: what the
			// user is being asked is "does importing this change anything about
			// this host", and the answer is the host it would produce.
			if sameHost(r.ToHost(h), h) {
				entry.State = "same"
			}
			p := cfgsync.PolicyFromSettings(settings, r.ID)
			entry.Loosens = looser(r.Policy, p)
		} else {
			entry.Loosens = looser(r.Policy, cfgsync.StrictestPolicy())
		}
		out.Hosts = append(out.Hosts, entry)
	}
	return out, nil
}

// SyncImportFile applies a file (§6.2 still applies).
func (a *App) SyncImportFile(path, passphrase string) (SyncResultView, error) {
	records, _, err := a.readBundle(path, passphrase)
	if err != nil {
		return SyncResultView{}, err
	}
	store, err := a.openSyncStore()
	if err != nil {
		return SyncResultView{}, err
	}

	state := store.State()
	local := map[string]config.Host{}
	for _, h := range a.hosts.List() {
		local[h.ID] = h
	}
	settings := (syncLocal{a}).Settings()

	var view SyncResultView
	pending := store.Pending()
	for _, r := range records {
		var current *cfgsync.Record
		if h, ok := local[r.ID]; ok {
			host, auth := cfgsync.FromHost(h, "", cfgsync.FingerprintLabel(h.IdentityFile))
			cur := cfgsync.Record{
				ID: h.ID, Host: host, Auth: auth,
				Policy: cfgsync.PolicyFromSettings(settings, h.ID),
			}
			current = &cur
		}
		lastSeen := int64(0)
		if rs := state.Records[r.ID]; rs != nil {
			lastSeen = rs.Rev
		}
		keys, _ := (syncLocal{a}).HostKeys(r.Addr())

		d := cfgsync.Gate(current, r, lastSeen, keys)
		view.Warnings = append(view.Warnings, d.Warnings...)
		if d.Skip {
			continue
		}
		if err := a.applySyncRecord(d.Apply, d.ApplyHostKeys); err != nil {
			return view, err
		}
		view.Received++

		rs := state.Records[r.ID]
		if rs == nil {
			rs = &cfgsync.RecordState{}
			state.Records[r.ID] = rs
		}
		applied := d.Apply.Policy
		rs.Applied = &applied
		if r.Rev > rs.Rev {
			rs.Rev = r.Rev
		}
		// The file becomes this machine's base for that host. Without it, a later
		// sync against a repository would read every imported host as a local
		// change and push it.
		base := r
		rs.Base = &base

		for _, p := range d.Pending {
			if store.WasDismissed(p.RecordID, p.Field, p.Rev) {
				continue
			}
			pending = append(pending, p)
		}
	}

	if err := store.SetState(state); err != nil {
		return view, err
	}
	if err := store.SetPending(pending); err != nil {
		return view, err
	}
	view.Pending = len(pending)
	a.emit("sync:state", nil)
	a.emit("sync:result", view)
	return view, nil
}

// applySyncRecord writes one record's effects, shared by the file and repository
// paths so an imported host cannot arrive by a route with different rules.
func (a *App) applySyncRecord(r cfgsync.Record, keys []cfgsync.RecordHostKey) error {
	l := syncLocal{a}
	if r.Deleted {
		// A tombstone in a file is not a delete. The file is a snapshot somebody
		// exported; acting on an absence in it would mean opening last month's
		// backup could remove hosts added since.
		return nil
	}
	var keep config.Host
	if h, ok := a.hosts.Get(r.ID); ok {
		keep = h
	}
	if err := l.SaveHost(r.ToHost(keep)); err != nil {
		return err
	}
	if err := l.SetPolicy(r.ID, r.Policy); err != nil {
		return err
	}
	for _, k := range keys {
		if err := l.AddHostKey(r.Addr(), k); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) readBundle(path, passphrase string) ([]cfgsync.Record, cfgsync.Bundle, error) {
	if a.headless {
		return nil, cfgsync.Bundle{}, a.syncNotHere()
	}
	if path == "" {
		return nil, cfgsync.Bundle{}, i18n.Errorf("파일을 고르지 않았습니다")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cfgsync.Bundle{}, fmt.Errorf("app: read %s: %w", path, err)
	}
	return cfgsync.OpenBundle(data, passphrase)
}

// exportableRecords builds a record per syncable host from this machine's files.
func (a *App) exportableRecords() []cfgsync.Record {
	if a.hosts == nil {
		return nil
	}
	settings := (syncLocal{a}).Settings()
	var out []cfgsync.Record
	for _, h := range a.hosts.List() {
		if !cfgsync.Syncable(h) {
			continue
		}
		host, auth := cfgsync.FromHost(h, "", cfgsync.FingerprintLabel(h.IdentityFile))
		r := cfgsync.Record{
			ID:        h.ID,
			Rev:       1,
			UpdatedAt: time.Now().UTC(),
			Host:      host,
			Auth:      auth,
			Policy:    cfgsync.PolicyFromSettings(settings, h.ID),
		}
		if keys, err := (syncLocal{a}).HostKeys(r.Addr()); err == nil {
			r.HostKeys = keys
		}
		out = append(out, r)
	}
	return out
}

// sameHost reports whether two host entries describe the same thing.
//
// config.Host holds a slice, so it is not comparable with ==; and the comparison
// has to be on the whole value rather than the fields sync carries, because a
// difference anywhere is a difference the user would see on the host card.
func sameHost(a, b config.Host) bool {
	if a.ID != b.ID || a.Name != b.Name || a.Group != b.Group || a.Hostname != b.Hostname ||
		a.Port != b.Port || a.User != b.User || a.ProxyJump != b.ProxyJump ||
		a.IdentityFile != b.IdentityFile || a.Source != b.Source {
		return false
	}
	if len(a.Auth) != len(b.Auth) {
		return false
	}
	for i := range a.Auth {
		if a.Auth[i] != b.Auth[i] {
			return false
		}
	}
	return true
}

// looser reports whether any field of a is less strict than the same field of b.
func looser(a, b cfgsync.RecordPolicy) bool {
	return (a.Shared && !b.Shared) ||
		(a.ExecEnabled && !b.ExecEnabled) ||
		(a.DeleteEnabled && !b.DeleteEnabled) ||
		cfgsync.Strictness(a.MCPApproval) < cfgsync.Strictness(b.MCPApproval)
}
