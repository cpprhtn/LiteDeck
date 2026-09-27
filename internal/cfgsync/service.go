package cfgsync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// One pass of the sync, and the loop that triggers it (§8.1, §8.2).
//
// # The shape
//
//	pull → decrypt → merge → gate → apply → encrypt → commit and push
//
// with a retry from the top when somebody else pushed in between. Nothing here
// decides anything: the decisions are in merge.go and gate.go, as pure functions
// over values, and this file is the part that reads and writes. That split is why
// a rule like "loosening waits for a person" can be read in one place instead of
// being spread through an I/O path.
//
// # Never two at once
//
// A mutex, and a trigger that finds it held gives up rather than queueing. Two
// passes over one repository would both pull the same state, both merge it, and
// both push — and the second would be refused, retry, and do it again. The
// five-minute timer and the ten-second debounce after an edit both land here.

// Local is the app's own storage, as the sync needs to see it.
//
// An interface because internal/app owns these — hosts.json, settings.json,
// known_hosts — and this package must not import it. The methods are deliberately
// coarse: the sync applies a whole record at a time, so there is no way to write
// half of one.
type Local interface {
	// Hosts returns every host on this machine, including ones not syncable.
	Hosts() []config.Host
	// SaveHost writes a host, adding it if it is new.
	SaveHost(h config.Host) error
	// DeleteHost removes one. A host deleted elsewhere and tombstoned arrives
	// here.
	DeleteHost(id string) error
	// Settings is the current settings, for reading the per-host policy.
	Settings() config.Settings
	// SetPolicy writes one host's policy: shared, approval mode, exec, delete.
	// Whatever expiry a relaxed mode had locally is the local machine's to keep
	// or restart (§6.2).
	SetPolicy(hostID string, p RecordPolicy) error
	// HostKeys returns the keys this machine trusts for an address, in
	// `host:port` form (§6.3).
	HostKeys(addr string) ([]RecordHostKey, error)
	// AddHostKey trusts a key for an address. Only ever called for an address
	// that had none.
	AddHostKey(addr string, k RecordHostKey) error
}

// Result is what one sync did, for the toast and the history (§9.1).
type Result struct {
	Received  int        `json:"received"`
	Sent      int        `json:"sent"`
	Pending   int        `json:"pending"`
	Warnings  []Warning  `json:"warnings,omitempty"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	Head      string     `json:"head,omitempty"`
	At        time.Time  `json:"at"`
}

// Service runs the sync.
type Service struct {
	store   *Store
	local   Local
	backend Backend
	vault   *Vault
	// now is time.Now, replaced in tests. Every record this machine writes is
	// stamped with it, and the merge compares stamps.
	now func() time.Time

	mu      sync.Mutex // one pass at a time
	running bool
}

// NewService assembles one. The vault must already be open: this package never
// asks for a passphrase, because who to ask is a question only the UI can answer.
func NewService(store *Store, local Local, backend Backend, vault *Vault) *Service {
	return &Service{store: store, local: local, backend: backend, vault: vault, now: time.Now}
}

// maxPushAttempts bounds the retry when the remote keeps moving (§8.2).
//
// Three, then give up until the next trigger. A loop that retries forever against
// a repository somebody else is actively pushing to is a loop that never ends and
// never says so.
const maxPushAttempts = 3

// ErrBusy reports that a sync is already running.
var ErrBusy = errors.New("cfgsync: a sync is already running")

// Sync runs one pass (§8.2).
func (s *Service) Sync(ctx context.Context) (Result, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return Result{}, ErrBusy
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	var res Result
	var err error
	for attempt := 1; attempt <= maxPushAttempts; attempt++ {
		res, err = s.once(ctx)
		if !errors.Is(err, ErrRemoteAhead) {
			break
		}
		// Somebody else pushed. Everything computed above is stale — the merge
		// was against a state that no longer exists — so it all happens again.
	}
	entry := HistoryEntry{
		At: s.now().UTC(), Received: res.Received, Sent: res.Sent,
		Pending: res.Pending, Warnings: res.Warnings, Conflicts: res.Conflicts,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	_ = s.store.AppendHistory(entry)
	return res, err
}

func (s *Service) once(ctx context.Context) (Result, error) {
	res := Result{At: s.now().UTC()}
	state := s.store.State()

	head, err := s.backend.Pull(ctx)
	switch {
	case err == nil:
	case errors.Is(err, ErrEmptyRemote):
		// A repository with no commits. Everything local is new to it.
	default:
		return res, err
	}

	files, err := s.backend.ReadAll()
	if err != nil {
		return res, err
	}

	remote, warnings := s.decryptAll(files)
	res.Warnings = append(res.Warnings, warnings...)
	res.Warnings = append(res.Warnings, AddressConflicts(values(remote))...)

	// Local records, built from this machine's host list and settings.
	local := s.localRecords(state)

	var pending []PendingChange
	writes := map[string][]byte{}
	cfg := s.store.Config()

	for _, id := range union(local, remote) {
		lr, hasLocal := local[id]
		rr, hasRemote := remote[id]
		var lp, rp, base *Record
		if hasLocal {
			lp = &lr
		}
		if hasRemote {
			rp = &rr
		}
		if rs := state.Records[id]; rs != nil {
			base = rs.Base
		}

		merged := Merge(base, lp, rp, cfg.DeviceID, s.now())
		res.Warnings = append(res.Warnings, merged.Warnings...)
		res.Conflicts = append(res.Conflicts, merged.Conflicts...)

		// What this machine applies, and what it holds back. A record it is only
		// pushing has nothing to gate: it is already what this machine believes.
		toApply := merged.Record
		if merged.Action == ActionAccept || merged.Action == ActionMerge {
			lastSeen := int64(0)
			if rs := state.Records[id]; rs != nil {
				lastSeen = rs.Rev
			}
			localKeys, err := s.local.HostKeys(merged.Record.Addr())
			if err != nil {
				res.Warnings = append(res.Warnings, Warning{
					RecordID: id, Kind: WarnUndecryptable,
					Detail: fmt.Sprintf("could not read the trusted host keys for %s: %v",
						merged.Record.Addr(), err),
				})
			}
			d := Gate(lp, merged.Record, lastSeen, localKeys)
			res.Warnings = append(res.Warnings, d.Warnings...)
			if d.Skip {
				// A rewound record. Not applied, and not pushed either: pushing
				// would overwrite whatever the repository now holds with a
				// decision made from data this machine has just refused.
				continue
			}
			toApply = d.Apply
			for _, p := range d.Pending {
				if s.store.WasDismissed(p.RecordID, p.Field, p.Rev) {
					continue
				}
				pending = append(pending, p)
			}
			if err := s.applyRecord(toApply, d.ApplyHostKeys); err != nil {
				return res, err
			}
			if rs := state.Records[id]; rs != nil {
				applied := toApply.Policy
				rs.Applied = &applied
			} else {
				applied := toApply.Policy
				state.Records[id] = &RecordState{Applied: &applied}
			}
			res.Received++
		}

		switch merged.Action {
		case ActionPush, ActionMerge:
			// What goes to the repository is the merged record, not the gated
			// one: withholding a loosening on this machine must not undo it in
			// the repository, or the machine that made the change would see it
			// silently reverted and make it again.
			p, err := RecordPath(id)
			if err != nil {
				continue
			}
			plain, err := merged.Record.Marshal()
			if err != nil {
				return res, err
			}
			sealedBytes, err := s.vault.Encrypt(p, plain)
			if err != nil {
				return res, err
			}
			writes[p] = sealedBytes
			res.Sent++
		}

		// Remember where this record stands, whichever way it went.
		rs := state.Records[id]
		if rs == nil {
			rs = &RecordState{}
			state.Records[id] = rs
		}
		snapshot := merged.Record
		if merged.Action == ActionNone {
			snapshot = toApply
		}
		if snapshot.Rev > rs.Rev {
			rs.Rev = snapshot.Rev
		}
		base2 := snapshot
		rs.Base = &base2
	}

	if len(writes) > 0 {
		msg := "sync: " + shortDevice(cfg.DeviceID)
		if err := s.backend.CommitAndPush(ctx, writes, nil, msg); err != nil {
			return res, err
		}
	}

	if err := s.store.SetPending(pending); err != nil {
		return res, err
	}
	res.Pending = len(pending)
	state.Head = s.backend.Head()
	if state.Head == "" {
		state.Head = head
	}
	state.LastSync = s.now().UTC()
	res.Head = state.Head
	if err := s.store.SetState(state); err != nil {
		return res, err
	}
	return res, nil
}

// decryptAll opens every record in the working copy.
//
// A file that does not decrypt is one warning and is skipped. The alternative —
// failing the sync — would let one corrupted record stop every other host from
// ever syncing again, which is a denial of service for anybody who can write to
// the repository (§8.2).
func (s *Service) decryptAll(files map[string][]byte) (map[string]Record, []Warning) {
	out := map[string]Record{}
	var warnings []Warning
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		id, ok := RecordID(p)
		if !ok {
			continue // vault.json, .gitattributes, README.md
		}
		plain, err := s.vault.Decrypt(p, files[p])
		if err != nil {
			warnings = append(warnings, Warning{
				RecordID: id, Kind: WarnUndecryptable,
				Detail: "this record could not be opened; the others were applied normally",
			})
			continue
		}
		r, err := UnmarshalRecord(plain)
		if err != nil {
			warnings = append(warnings, Warning{
				RecordID: id, Kind: WarnUndecryptable, Detail: err.Error(),
			})
			continue
		}
		if r.ID != id {
			// The file name and the record disagree. The AEAD already binds a
			// record to its path, so this cannot come from somebody moving files
			// around; it would be a bug in a writer.
			warnings = append(warnings, Warning{
				RecordID: id, Kind: WarnUndecryptable,
				Detail: fmt.Sprintf("the record calls itself %s", r.ID),
			})
			continue
		}
		out[id] = r
	}
	return out, warnings
}

// localRecords builds a record per syncable host from this machine's files.
//
// The revision carries over from the base snapshot: a record's rev belongs to the
// repository, not to this machine, and starting from zero would make every local
// host look like a rewind to everybody else.
func (s *Service) localRecords(state State) map[string]Record {
	settings := s.local.Settings()
	out := map[string]Record{}
	for _, h := range s.local.Hosts() {
		if !Syncable(h) {
			continue
		}
		host, auth := FromHost(h, "", FingerprintLabel(h.IdentityFile))
		r := Record{
			ID:     h.ID,
			Host:   host,
			Auth:   auth,
			Policy: publishedPolicy(PolicyFromSettings(settings, h.ID), state.Records[h.ID]),
		}
		if rs := state.Records[h.ID]; rs != nil && rs.Base != nil {
			r.Rev = rs.Base.Rev
			r.UpdatedAt = rs.Base.UpdatedAt
			r.UpdatedBy = rs.Base.UpdatedBy
			// The host keys this machine would publish are the ones it trusts,
			// not the ones the repository last said (§6.3).
			r.HostKeys = rs.Base.HostKeys
			r.Auth.KeyFingerprint = rs.Base.Auth.KeyFingerprint
		}
		if keys, err := s.local.HostKeys(r.Addr()); err == nil && len(keys) > 0 {
			r.HostKeys = keys
		}
		r.normalise()
		out[h.ID] = r
	}
	return out
}

// publishedPolicy decides what this machine tells the repository about a host's
// policy, which is not always what it enforces locally (§6.2).
//
// A withheld loosening leaves settings.json stricter than the record. Publishing
// the local value there would revert, in the repository, a change somebody made on
// another machine and has not withdrawn — and the other machine would see its own
// choice come back tightened, apply it (tightening is automatic), and the
// loosening would be gone from a fleet where nobody decided that.
//
// So: where the local value is still the one this machine applied, the record keeps
// what the repository says. Where somebody has since changed it, that is an
// opinion and it travels.
func publishedPolicy(local RecordPolicy, rs *RecordState) RecordPolicy {
	if rs == nil || rs.Applied == nil || rs.Base == nil {
		return local
	}
	applied, base := *rs.Applied, rs.Base.Policy
	out := local
	if local.Shared == applied.Shared {
		out.Shared = base.Shared
	}
	if local.ExecEnabled == applied.ExecEnabled {
		out.ExecEnabled = base.ExecEnabled
	}
	if local.DeleteEnabled == applied.DeleteEnabled {
		out.DeleteEnabled = base.DeleteEnabled
	}
	if modeWord(local.MCPApproval) == modeWord(applied.MCPApproval) {
		out.MCPApproval = modeWord(base.MCPApproval)
	}
	return out
}

// applyRecord writes one record's effects to this machine.
func (s *Service) applyRecord(r Record, keys []RecordHostKey) error {
	if r.Deleted {
		if err := s.local.DeleteHost(r.ID); err != nil {
			return fmt.Errorf("cfgsync: delete %s: %w", r.ID, err)
		}
		return nil
	}
	var keep config.Host
	for _, h := range s.local.Hosts() {
		if h.ID == r.ID {
			keep = h
			break
		}
	}
	if err := s.local.SaveHost(r.ToHost(keep)); err != nil {
		return fmt.Errorf("cfgsync: save %s: %w", r.ID, err)
	}
	if err := s.local.SetPolicy(r.ID, r.Policy); err != nil {
		return fmt.Errorf("cfgsync: policy for %s: %w", r.ID, err)
	}
	for _, k := range keys {
		if err := s.local.AddHostKey(r.Addr(), k); err != nil {
			return fmt.Errorf("cfgsync: trust %s for %s: %w", k.Alg, r.Addr(), err)
		}
	}
	return nil
}

// InitRepository creates the vault and the two plaintext files in an empty
// repository (§4.1).
//
// .gitattributes goes in the first commit, not a later one: a record committed
// before it exists has already been through whatever line-ending translation the
// user's git is configured for (§6.7 ④).
func (s *Service) InitRepository(ctx context.Context, vf VaultFile) error {
	vaultJSON, err := marshalIndent(vf)
	if err != nil {
		return err
	}
	writes := map[string][]byte{
		AttributesPath: []byte(GitAttributes),
		VaultPath:      vaultJSON,
		ReadmePath: []byte("# LiteDeck sync\n\n" +
			"This repository holds LiteDeck's host list, encrypted. The contents are " +
			"not readable without the passphrase, and LiteDeck is the only thing that " +
			"writes here.\n"),
	}
	return s.backend.CommitAndPush(ctx, writes, nil, "sync: init")
}

func marshalIndent(v any) ([]byte, error) {
	b, err := jsonMarshalIndent(v)
	if err != nil {
		return nil, fmt.Errorf("cfgsync: encode vault.json: %w", err)
	}
	return b, nil
}

func union(a, b map[string]Record) []string {
	seen := map[string]bool{}
	var out []string
	for id := range a {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for id := range b {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	// Sorted, so a sync does the same work in the same order every time and a
	// history line can be compared with the one before it.
	sort.Strings(out)
	return out
}

func values(m map[string]Record) []Record {
	out := make([]Record, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	return out
}

// shortDevice is the first eight characters of a device ID, for a commit message
// (§4.2).
func shortDevice(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "unknown"
	}
	return id
}
