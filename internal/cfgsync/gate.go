package cfgsync

import (
	"fmt"
	"sort"
	"time"
)

// What this machine agrees to apply from what it was handed (§6).
//
// # The rule
//
// Tightening happens by itself; loosening waits for a person on this machine.
// Everything in this file follows from that one sentence, and it is the reason the
// feature is worth having: a host list that travels is convenient, and a
// permission that travels is somebody else's machine deciding what an AI client
// may do to your server. The vault key is enough to write a valid record (§6.1),
// so a machine of the user's that has been taken over could write "this host may
// run any command". What it cannot do is press the button here.
//
// # Why it is a pure function
//
// Gate reads nothing and writes nothing. Every case below — an unfamiliar
// approval mode, a host key that changed, a repository rewound to last week — is
// a table row in gate_test.go. The alternative is discovering the behaviour by
// running two machines against a real repository, which is how a rule like this
// ends up with an accidental exception nobody can find.

// Field names used in pending items and conflicts. They are shown to the user
// and stored in pending.json, so they are stable strings rather than an enum
// whose numbers would change meaning between versions.
const (
	FieldShared      = "policy.shared"
	FieldApproval    = "policy.mcp_approval"
	FieldExec        = "policy.exec_enabled"
	FieldDelete      = "policy.delete_enabled"
	FieldHostKeys    = "host_keys"
	FieldConnection  = "host"
	FieldAuthMethods = "auth.methods"
)

// Warning kinds.
const (
	// WarnRollback is a record whose rev went backwards: the repository was
	// rewound, by an attacker or by somebody's force-push (§6.4).
	WarnRollback = "rollback"
	// WarnVanished is a record file that disappeared without a tombstone. A
	// normal delete always leaves one, so this is the same event as a rollback.
	WarnVanished = "vanished"
	// WarnHostKeyMismatch is a host key that differs from the one this machine
	// already trusts for that address (§6.3).
	WarnHostKeyMismatch = "host_key_mismatch"
	// WarnDeletedButModified is a tombstone for a host this machine has changed
	// since it last synced (§8.3).
	WarnDeletedButModified = "deleted_but_modified"
	// WarnUndecryptable is a record that does not open. The others still apply.
	WarnUndecryptable = "undecryptable"
	// WarnAddressConflict is two records claiming different host keys for the
	// same address — two machines looking at different servers, or one of them
	// out of date (§6.3).
	WarnAddressConflict = "address_conflict"
)

// PendingChange is one field held back until somebody on this machine says yes
// (§6.2).
type PendingChange struct {
	RecordID string `json:"recordId"`
	// Rev is the revision the change arrived in. Dismissing a pending item
	// remembers this, so the same decision is not asked again at every sync —
	// and a later revision asks again, because it is a new decision.
	Rev   int64  `json:"rev"`
	Field string `json:"field"`
	// Local and Incoming are for display, already rendered as text: the panel
	// shows "strict → bypass", and a typed union of four field kinds would be
	// four times the code for the same sentence.
	Local    string    `json:"local"`
	Incoming string    `json:"incoming"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
}

// Warning is something the person should see, which no button fixes.
type Warning struct {
	RecordID string `json:"recordId"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
}

// Decision is what Gate concluded (§7.2).
type Decision struct {
	// Apply is the record to write locally. Where a field was loosening, it
	// holds this machine's stricter value, not the incoming one.
	Apply Record
	// ApplyHostKeys are host keys to add to known_hosts now: only for addresses
	// where this machine trusts none, so nothing is ever replaced silently.
	ApplyHostKeys []RecordHostKey
	Pending       []PendingChange
	Warnings      []Warning
	// Skip means apply nothing from this record. Set when the repository looks
	// rewound: the safe reading of "the history moved backwards" is that this
	// data cannot be trusted at all.
	Skip bool
}

// Gate decides how an incoming record may touch this machine (§6.2, §6.3, §6.4).
//
// local is the record as this machine has it, or nil for a host arriving for the
// first time. lastSeenRev is the highest rev this machine has seen for this ID
// (state.json); zero for one it has never seen. localKeys are the host keys this
// machine already trusts for the record's address, read from known_hosts —
// per address rather than per record, because that file is keyed by address and
// two records can name the same server (§6.3).
func Gate(local *Record, incoming Record, lastSeenRev int64, localKeys []RecordHostKey) Decision {
	// Rewind first, before anything is read out of the record. A repository whose
	// history went backwards may be handing back a permission that was revoked.
	if incoming.Rev < lastSeenRev {
		return Decision{
			Skip: true,
			Warnings: []Warning{{
				RecordID: incoming.ID, Kind: WarnRollback,
				Detail: fmt.Sprintf("rev %d, but this device already saw rev %d",
					incoming.Rev, lastSeenRev),
			}},
		}
	}

	d := Decision{Apply: incoming}

	// A tombstone carries no host or policy worth gating. Whether the delete is
	// honoured is a merge question (§8.3) and is answered there.
	if incoming.Deleted {
		return d
	}

	// Policy, field by field. Never as a whole: a record that tightens the
	// approval mode and loosens file deletion in one revision is ordinary, and
	// judging it as one thing would either apply the loosening or withhold the
	// tightening.
	localPolicy := StrictestPolicy()
	if local != nil {
		localPolicy = local.Policy
	}
	d.Apply.Policy = gatePolicy(localPolicy, incoming, &d)

	// Host keys. Applied per address, and only where there is nothing to
	// contradict.
	d.ApplyHostKeys, d.Pending, d.Warnings = gateHostKeys(incoming, localKeys, d.Pending, d.Warnings)

	// The rest of the record — where it is, what it is called, how to log in —
	// arrives as it is. None of it grants anything, and all of it is visible on
	// the host card.
	return d
}

// gatePolicy applies the tightenings and queues the loosenings.
func gatePolicy(local RecordPolicy, incoming Record, d *Decision) RecordPolicy {
	out := incoming.Policy
	in := incoming.Policy

	// Shared: not shared is stricter. Absent means no, which is why registering a
	// server in LiteDeck does not hand it to an AI client as a side effect.
	if in.Shared && !local.Shared {
		out.Shared = false
		d.Pending = append(d.Pending, pending(incoming, FieldShared,
			boolWord(local.Shared), boolWord(in.Shared)))
	}

	// Approval mode: compared by strictness, not equality, because there are
	// three of them and "different" is not a direction.
	if Strictness(in.MCPApproval) < Strictness(local.MCPApproval) {
		out.MCPApproval = local.MCPApproval
		d.Pending = append(d.Pending, pending(incoming, FieldApproval,
			modeWord(local.MCPApproval), modeWord(in.MCPApproval)))
	} else if out.MCPApproval == "" {
		// An empty mode means "ask" everywhere else in the app; writing it out
		// keeps settings.json saying what it means.
		out.MCPApproval = ApprovalAsk
	}

	if in.ExecEnabled && !local.ExecEnabled {
		out.ExecEnabled = false
		d.Pending = append(d.Pending, pending(incoming, FieldExec,
			boolWord(local.ExecEnabled), boolWord(in.ExecEnabled)))
	}
	if in.DeleteEnabled && !local.DeleteEnabled {
		out.DeleteEnabled = false
		d.Pending = append(d.Pending, pending(incoming, FieldDelete,
			boolWord(local.DeleteEnabled), boolWord(in.DeleteEnabled)))
	}
	return out
}

// gateHostKeys decides what may go into known_hosts (§6.3).
//
// Three cases, and only the first is automatic:
//
//   - the address has no key here: take it, and say where it came from;
//   - the address has a different key for the same algorithm: do not touch it.
//     Either the server was rebuilt or somebody is between the two; a GUI that
//     replaces the key it was handed is a GUI that makes that indistinguishable;
//   - the record adds an algorithm this machine has not trusted: also a person's
//     decision. An extra key is an extra thing that can vouch for the server.
func gateHostKeys(incoming Record, localKeys []RecordHostKey,
	pendings []PendingChange, warnings []Warning) ([]RecordHostKey, []PendingChange, []Warning) {

	byAlg := map[string]RecordHostKey{}
	for _, k := range localKeys {
		byAlg[k.Alg] = k
	}

	var apply []RecordHostKey
	for _, k := range incoming.HostKeys {
		have, ok := byAlg[k.Alg]
		switch {
		case len(localKeys) == 0:
			// Nothing trusted for this address at all. Taking the key is how a
			// new machine gets to connect without being asked to verify a
			// fingerprint it has no way to check.
			apply = append(apply, k)
		case !ok:
			pendings = append(pendings, pending(incoming, FieldHostKeys,
				"—", k.Alg+" "+k.Fingerprint))
		case have.Key != k.Key:
			pendings = append(pendings, pending(incoming, FieldHostKeys,
				have.Alg+" "+have.Fingerprint, k.Alg+" "+k.Fingerprint))
			warnings = append(warnings, Warning{
				RecordID: incoming.ID, Kind: WarnHostKeyMismatch,
				Detail: fmt.Sprintf("%s: this device trusts %s, the repository says %s",
					incoming.Addr(), have.Fingerprint, k.Fingerprint),
			})
		}
	}
	return apply, pendings, warnings
}

func pending(r Record, field, local, incoming string) PendingChange {
	return PendingChange{
		RecordID: r.ID, Rev: r.Rev, Field: field,
		Local: local, Incoming: incoming, By: r.UpdatedBy, At: r.UpdatedAt,
	}
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func modeWord(m string) string {
	if m == "" {
		return ApprovalAsk
	}
	return m
}

// AddressConflicts reports addresses where two records disagree about the host
// key (§6.3).
//
// Not a per-record question, which is why it is not in Gate: the records are
// individually consistent and the repository as a whole is not. Two records for
// one server under two accounts is an ordinary thing to have, and the moment
// their keys differ, one of them is wrong — the server was rebuilt and only one
// machine has noticed, or two machines are looking at different servers under one
// address.
func AddressConflicts(records []Record) []Warning {
	type seen struct {
		fingerprint string
		recordID    string
	}
	keys := map[string]seen{} // addr+alg → first record that named a key
	var out []Warning
	// Sorted, so the warning list does not reshuffle between syncs and make the
	// panel look like something changed.
	sorted := make([]Record, len(records))
	copy(sorted, records)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	for _, r := range sorted {
		if r.Deleted {
			continue
		}
		for _, k := range r.HostKeys {
			at := r.Addr() + " " + k.Alg
			prev, ok := keys[at]
			if !ok {
				keys[at] = seen{k.Fingerprint, r.ID}
				continue
			}
			if prev.fingerprint != k.Fingerprint {
				out = append(out, Warning{
					RecordID: r.ID, Kind: WarnAddressConflict,
					Detail: fmt.Sprintf("%s: this record says %s, record %s says %s",
						r.Addr(), k.Fingerprint, prev.recordID, prev.fingerprint),
				})
			}
		}
	}
	return out
}
