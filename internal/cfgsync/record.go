package cfgsync

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// One host, as it travels (§3.1, §4.5).
//
// # What is in here and what is not
//
// Everything a second machine needs to reach the same server and treat it with
// the same care: where it is, who to log in as, which key by fingerprint, which
// host keys have been trusted, and how much an AI client is allowed to do to it.
//
// Not in here: the password, the key passphrase, the sudo password, the private
// key or its path, the MCP token. Those are either secrets (which live in the OS
// credential store and are not synced at all, §3.2) or facts about one machine —
// `/Users/me/.ssh/id_ed25519` is not a path the Windows box has. The fingerprint
// travels instead, and each machine keeps its own map from fingerprint to where
// that key actually is (§3.3).
//
// # Why the expiry does not travel
//
// A relaxed approval mode carries an expiry — "do not ask me for eight hours".
// The mode travels; the expiry does not (§6.2). Two machines whose clocks differ
// by minutes would otherwise disagree about whether a window is still open, and
// the disagreement would resolve in the direction of more permission. The
// decision not to be asked while you are away from the desk belongs to the person
// at that desk.

// Record is one host in the repository (§4.5).
type Record struct {
	ID  string `json:"id"`
	Rev int64  `json:"rev"`
	// UpdatedAt and UpdatedBy are for the person reading a conflict, not for the
	// merge rules that matter: policy is settled by strictness and host keys by
	// the local value, neither by time (§6.7 ⑦).
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
	// Deleted is a tombstone. A record is never removed from the repository,
	// because a file that simply vanished is indistinguishable from a history
	// rewrite, and one of those is an attack (§6.4).
	Deleted bool `json:"deleted,omitempty"`

	Host     RecordHost      `json:"host"`
	Auth     RecordAuth      `json:"auth"`
	HostKeys []RecordHostKey `json:"host_keys,omitempty"`
	Policy   RecordPolicy    `json:"policy"`
}

// RecordHost is where to connect, as the user described it.
type RecordHost struct {
	Name     string `json:"name"`
	Group    string `json:"group,omitempty"`
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	// ProxyJump is `user@host:port`, not a reference to another record — which
	// is why nothing here breaks when the two machines disagree about IDs, and
	// why a deleted host cannot be detected as still referenced (§3.1).
	ProxyJump string `json:"proxy_jump,omitempty"`
}

// RecordAuth is how to log in, minus anything secret and anything local.
type RecordAuth struct {
	// Methods in the order the user wants them tried — the app stores an
	// ordered list, not a single choice, and the order is the user's decision.
	Methods []string `json:"methods"`
	// KeyFingerprint identifies the key without saying where it is. The
	// receiving machine looks it up in its own map (§3.3) and asks if it has
	// never seen it.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	KeyLabel       string `json:"key_label,omitempty"`
}

// RecordHostKey is one trusted host key (§3.1).
//
// Carried per record, but applied per address: known_hosts is one OpenSSH file
// keyed by host:port, so two records for the same server under two accounts
// would otherwise fight over one line in it (§6.3).
type RecordHostKey struct {
	Alg         string    `json:"alg"`
	Key         string    `json:"key"` // base64, as known_hosts spells it
	Fingerprint string    `json:"fingerprint"`
	TrustedAt   time.Time `json:"trusted_at"`
	TrustedBy   string    `json:"trusted_by"`
}

// RecordPolicy is how much an AI client may do to this host (§3.1, §6.2).
//
// Four fields, all mapped from settings.json: whether the host is shared with
// clients at all, the approval mode for changes, and the two switches for
// running commands and deleting files. There is deliberately no expiry here
// (§6.2).
type RecordPolicy struct {
	// Shared is settings.mcp.hosts — whether an AI client can see this host.
	Shared bool `json:"shared"`
	// MCPApproval is "ask", "strict" or "bypass" (internal/app/mcp_approval.go).
	MCPApproval   string `json:"mcp_approval"`
	ExecEnabled   bool   `json:"exec_enabled"`
	DeleteEnabled bool   `json:"delete_enabled"`
}

// Approval modes, as strings, so this package does not import internal/app.
//
// The values are the ones in settings.json; TestApprovalModesMatchTheApp holds
// the two lists together, because a mode added there and not here would arrive on
// another machine as an unknown string and be treated as the strictest — which is
// safe, and silent, and wrong.
const (
	ApprovalAsk    = "ask"
	ApprovalStrict = "strict"
	ApprovalBypass = "bypass"
)

// Strictness orders the approval modes (§6.2).
//
// Higher is stricter. The order is stated once, here, because "tighten
// automatically, loosen only with a person's say-so" is the rule the whole sync
// design rests on, and a rule that depends on comparing two strings needs
// somewhere for that comparison to live.
//
// An unknown mode is the strictest thing there is. A repository written by a
// newer LiteDeck may name a mode this build has never heard of; treating it as
// permissive would be opening a door because we did not recognise its label.
func Strictness(mode string) int {
	switch mode {
	case ApprovalBypass:
		return 0
	case ApprovalAsk, "":
		return 1
	case ApprovalStrict:
		return 2
	default:
		return 3
	}
}

// StrictestPolicy is what a host arriving on a new machine starts as (§6.2).
//
// The strictest of the app's *defaults*, which is the defaults: a host nobody has
// configured is not shared with AI clients, runs no commands, deletes no files,
// and asks before it writes.
//
// Not "strict", although that mode exists and is stricter. Strict is something a
// person opts into for one host, and starting here would make every host that ever
// arrives raise a question — "the repository says ask, this device says strict" —
// and then push `strict` back, until one person's choice for one server had
// tightened the whole fleet. A default that produces a question is not a default.
func StrictestPolicy() RecordPolicy {
	return RecordPolicy{
		Shared:        false,
		MCPApproval:   ApprovalAsk,
		ExecEnabled:   false,
		DeleteEnabled: false,
	}
}

// Marshal encodes a record for the repository.
//
// Indented, and with a trailing newline, for one reason: a user who clones their
// own repository to look at it should see something. It is ciphertext by then, so
// this only shows up in tests and in a `git show` of the plaintext during
// development — but a format nobody can read by hand is a format nobody can
// debug.
func (r Record) Marshal() ([]byte, error) {
	r.normalise()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cfgsync: encode record: %w", err)
	}
	return append(b, '\n'), nil
}

// UnmarshalRecord decodes one, and refuses what it cannot make sense of.
//
// Validation here rather than at the point of use: this is the boundary where
// bytes somebody else wrote become a value this app acts on. A record with no ID
// cannot be filed, and a record whose ID is not the UUID its file is named after
// is either a bug or somebody moving records around inside the repository.
func UnmarshalRecord(b []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("cfgsync: decode record: %w", err)
	}
	if !lowercaseUUID.MatchString(r.ID) {
		return Record{}, fmt.Errorf("cfgsync: record id %q is not a lowercase hyphenated UUID", r.ID)
	}
	if r.Rev < 1 {
		return Record{}, fmt.Errorf("cfgsync: record %s has rev %d", r.ID, r.Rev)
	}
	r.normalise()
	return r, nil
}

// normalise puts the parts with no natural order into one, so that two machines
// that know the same things produce the same bytes.
//
// Without this, a record whose host keys arrived in a different order is a
// different ciphertext, which is a commit, which is a push, which is the other
// machine pulling and doing the same back. Sync loops are not hypothetical.
func (r *Record) normalise() {
	sort.Slice(r.HostKeys, func(i, j int) bool {
		if r.HostKeys[i].Alg != r.HostKeys[j].Alg {
			return r.HostKeys[i].Alg < r.HostKeys[j].Alg
		}
		return r.HostKeys[i].Key < r.HostKeys[j].Key
	})
	r.UpdatedAt = r.UpdatedAt.UTC().Truncate(time.Second)
	for i := range r.HostKeys {
		r.HostKeys[i].TrustedAt = r.HostKeys[i].TrustedAt.UTC().Truncate(time.Second)
	}
}

// Addr is the host:port these host keys belong to (§6.3).
func (r Record) Addr() string {
	port := r.Host.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", r.Host.Hostname, port)
}

// SameContent reports whether two records describe the same thing, ignoring the
// bookkeeping.
//
// Used to decide whether there is anything to push. Comparing whole records
// would compare UpdatedAt, which differs every time, and every sync would be a
// commit.
func (r Record) SameContent(other Record) bool {
	a, b := r, other
	a.Rev, b.Rev = 0, 0
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	a.UpdatedBy, b.UpdatedBy = "", ""
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// Syncable reports whether a host belongs in the repository at all (§2.3, §4.1).
//
// Two exclusions, for different reasons. A host imported from ~/.ssh/config is
// re-derived from that file on every import and matched by ID, so syncing it
// would produce a duplicate on the next import — and that file is already the
// user's own way of carrying hosts between machines. A host whose ID is not a
// UUID has not been through the migration, which means this build is older than
// the host list it is looking at.
func Syncable(h config.Host) bool {
	return h.Source != config.SSHConfigSource && lowercaseUUID.MatchString(h.ID)
}

// FromHost builds the record's host and auth halves from the local host entry.
//
// The identity file is deliberately dropped: it is a path on this machine (§3.3).
// What identifies the key across machines is its fingerprint, which the caller
// supplies — it is not in hosts.json, because it has to be read out of the key
// file itself.
func FromHost(h config.Host, keyFingerprint, keyLabel string) (RecordHost, RecordAuth) {
	methods := make([]string, 0, len(h.Auth))
	for _, m := range h.Auth {
		methods = append(methods, string(m))
	}
	return RecordHost{
			Name:      h.Name,
			Group:     h.Group,
			Hostname:  h.Hostname,
			Port:      h.Port,
			User:      h.User,
			ProxyJump: h.ProxyJump,
		}, RecordAuth{
			Methods:        methods,
			KeyFingerprint: keyFingerprint,
			KeyLabel:       keyLabel,
		}
}

// ToHost turns a record back into a local host entry.
//
// keep is the host as it exists on this machine, or the zero value for one
// arriving for the first time. Its ID and the fields that belong to this machine
// — the identity file above all — are carried over rather than taken from the
// record, because the record has nothing to say about them.
func (r Record) ToHost(keep config.Host) config.Host {
	out := keep
	out.ID = r.ID
	out.Name = r.Host.Name
	out.Group = r.Host.Group
	out.Hostname = r.Host.Hostname
	out.Port = r.Host.Port
	out.User = r.Host.User
	out.ProxyJump = r.Host.ProxyJump
	out.Auth = nil
	for _, m := range r.Auth.Methods {
		switch config.AuthMethod(m) {
		case config.AuthAgent, config.AuthKey, config.AuthPassword:
			out.Auth = append(out.Auth, config.AuthMethod(m))
		default:
			// A method this build does not know is dropped rather than carried.
			// The alternative is writing it into hosts.json, where every later
			// read has to cope with it.
		}
	}
	// Source stays whatever it was locally. A record never arrives claiming to
	// be from ssh_config — those are not synced — and letting it say so would be
	// a way to make the next import overwrite a host it did not create.
	out.Source = keep.Source
	return out
}

// PolicyFromSettings reads one host's policy out of the local settings.
func PolicyFromSettings(s config.Settings, hostID string) RecordPolicy {
	mode := s.MCP.Write[hostID].Mode
	if mode == "" {
		mode = ApprovalAsk
	}
	return RecordPolicy{
		Shared: s.MCP.Hosts[hostID],
		// The mode as stored, not as currently in effect. An expiry that has
		// passed is this machine's business; what travels is what the user
		// chose (§6.2).
		MCPApproval:   mode,
		ExecEnabled:   s.MCP.Exec[hostID],
		DeleteEnabled: s.MCP.Delete[hostID],
	}
}

// FingerprintLabel is a short name for a key, derived from its path.
//
// The path itself does not travel, but "work-ed25519" is what makes a prompt on
// the next machine answerable: "pick the key for prod-web" is a question nobody
// can answer, and "the one you call work-ed25519" is.
func FingerprintLabel(identityFile string) string {
	if identityFile == "" {
		return ""
	}
	if i := strings.LastIndexAny(identityFile, `/\`); i >= 0 {
		return identityFile[i+1:]
	}
	return identityFile
}
