package cfgsync

import (
	"strings"
	"testing"
	"time"
)

func policyRecord(p RecordPolicy) Record {
	r := sampleRecord()
	r.Policy = p
	r.HostKeys = nil
	return r
}

func find(ps []PendingChange, field string) (PendingChange, bool) {
	for _, p := range ps {
		if p.Field == field {
			return p, true
		}
	}
	return PendingChange{}, false
}

// Every policy field, in both directions, for a host this machine already has and
// for one arriving for the first time (§6.2).
//
// The table is the specification. A loosening that slips through is not a bug
// somebody notices — it is an AI client quietly being allowed to do something on a
// server, because another machine said so.
func TestGatePolicyFieldByField(t *testing.T) {
	loose := RecordPolicy{Shared: true, MCPApproval: ApprovalBypass, ExecEnabled: true, DeleteEnabled: true}
	strict := StrictestPolicy()

	cases := []struct {
		name        string
		local       *RecordPolicy // nil = a host arriving for the first time
		incoming    RecordPolicy
		wantApply   RecordPolicy
		wantPending []string
	}{
		{
			name:     "everything loosens on a known host",
			local:    &strict,
			incoming: loose,
			// Nothing moves. The local value is kept in every field.
			wantApply:   strict,
			wantPending: []string{FieldShared, FieldApproval, FieldExec, FieldDelete},
		},
		{
			name:        "everything tightens",
			local:       &loose,
			incoming:    strict,
			wantApply:   strict,
			wantPending: nil,
		},
		{
			name:        "identical",
			local:       &loose,
			incoming:    loose,
			wantApply:   loose,
			wantPending: nil,
		},
		{
			name:     "one field tightens and another loosens in the same revision",
			local:    &RecordPolicy{Shared: true, MCPApproval: ApprovalBypass, ExecEnabled: false, DeleteEnabled: false},
			incoming: RecordPolicy{Shared: true, MCPApproval: ApprovalStrict, ExecEnabled: true, DeleteEnabled: false},
			// The mode tightens and is taken; exec loosens and waits. Judging the
			// record as one thing would lose one half or the other.
			wantApply:   RecordPolicy{Shared: true, MCPApproval: ApprovalStrict, ExecEnabled: false, DeleteEnabled: false},
			wantPending: []string{FieldExec},
		},
		{
			name:     "a host arriving for the first time starts strict",
			local:    nil,
			incoming: loose,
			// Not the app's defaults — the strictest values it has. A host nobody
			// on this machine has looked at is not handed to an AI client because
			// a repository said so.
			wantApply:   strict,
			wantPending: []string{FieldShared, FieldApproval, FieldExec, FieldDelete},
		},
		{
			name:        "a new host that is already strict needs no decision",
			local:       nil,
			incoming:    strict,
			wantApply:   strict,
			wantPending: nil,
		},
		{
			name:        "ask is stricter than bypass and looser than strict",
			local:       &RecordPolicy{MCPApproval: ApprovalAsk},
			incoming:    RecordPolicy{MCPApproval: ApprovalBypass},
			wantApply:   RecordPolicy{MCPApproval: ApprovalAsk},
			wantPending: []string{FieldApproval},
		},
		{
			name:        "an empty mode is ask, not a loosening",
			local:       &RecordPolicy{MCPApproval: ApprovalAsk},
			incoming:    RecordPolicy{MCPApproval: ""},
			wantApply:   RecordPolicy{MCPApproval: ApprovalAsk},
			wantPending: nil,
		},
		{
			name:        "a mode this build has never heard of is treated as stricter",
			local:       &RecordPolicy{MCPApproval: ApprovalAsk},
			incoming:    RecordPolicy{MCPApproval: "paranoid"},
			wantApply:   RecordPolicy{MCPApproval: "paranoid"},
			wantPending: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var local *Record
			if tc.local != nil {
				r := policyRecord(*tc.local)
				local = &r
			}
			d := Gate(local, policyRecord(tc.incoming), 0, nil)
			if d.Skip {
				t.Fatal("the record was skipped")
			}
			if d.Apply.Policy != tc.wantApply {
				t.Errorf("applied  %+v\nwant     %+v", d.Apply.Policy, tc.wantApply)
			}
			if len(d.Pending) != len(tc.wantPending) {
				t.Errorf("pending = %+v, want fields %v", d.Pending, tc.wantPending)
			}
			for _, field := range tc.wantPending {
				p, ok := find(d.Pending, field)
				if !ok {
					t.Errorf("%s is not waiting for anybody", field)
					continue
				}
				if p.Local == p.Incoming {
					t.Errorf("%s: pending item says %q → %q", field, p.Local, p.Incoming)
				}
				if p.Rev != policyRecord(tc.incoming).Rev {
					t.Errorf("%s: pending rev = %d", field, p.Rev)
				}
				if p.By == "" || p.At.IsZero() {
					t.Errorf("%s: pending item does not say which device or when: %+v", field, p)
				}
			}
		})
	}
}

// Host keys: taken only where this machine trusts nothing for that address (§6.3).
func TestGateHostKeys(t *testing.T) {
	ed := RecordHostKey{Alg: "ssh-ed25519", Key: "AAAA", Fingerprint: "SHA256:aaa"}
	edOther := RecordHostKey{Alg: "ssh-ed25519", Key: "BBBB", Fingerprint: "SHA256:bbb"}
	rsa := RecordHostKey{Alg: "rsa-sha2-512", Key: "CCCC", Fingerprint: "SHA256:ccc"}

	cases := []struct {
		name        string
		localKeys   []RecordHostKey
		incoming    []RecordHostKey
		wantApply   int
		wantPending bool
		wantWarn    string
	}{
		{"nothing trusted here yet", nil, []RecordHostKey{ed}, 1, false, ""},
		{"the same key", []RecordHostKey{ed}, []RecordHostKey{ed}, 0, false, ""},
		{"a different key for the same algorithm", []RecordHostKey{ed}, []RecordHostKey{edOther}, 0, true, WarnHostKeyMismatch},
		{"an algorithm this machine has not trusted", []RecordHostKey{ed}, []RecordHostKey{ed, rsa}, 0, true, ""},
		{"the record carries none", []RecordHostKey{ed}, nil, 0, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sampleRecord()
			r.HostKeys = tc.incoming
			local := sampleRecord()
			d := Gate(&local, r, 0, tc.localKeys)

			if len(d.ApplyHostKeys) != tc.wantApply {
				t.Errorf("applied %d keys, want %d: %+v", len(d.ApplyHostKeys), tc.wantApply, d.ApplyHostKeys)
			}
			_, pending := find(d.Pending, FieldHostKeys)
			if pending != tc.wantPending {
				t.Errorf("pending = %v, want %v (%+v)", pending, tc.wantPending, d.Pending)
			}
			if tc.wantWarn != "" {
				found := false
				for _, w := range d.Warnings {
					if w.Kind == tc.wantWarn {
						found = true
						if !strings.Contains(w.Detail, r.Addr()) {
							t.Errorf("the warning does not name the address: %q", w.Detail)
						}
					}
				}
				if !found {
					t.Errorf("no %s warning: %+v", tc.wantWarn, d.Warnings)
				}
			}
			// A key is never replaced by syncing, whatever else happens.
			for _, applied := range d.ApplyHostKeys {
				for _, have := range tc.localKeys {
					if have.Alg == applied.Alg && have.Key != applied.Key {
						t.Errorf("a trusted %s key was replaced with %s", have.Alg, applied.Fingerprint)
					}
				}
			}
		})
	}
}

// A revision that went backwards stops everything for that record (§6.4).
//
// The repository host can rewind history — a force-push, or a restored backup. The
// record that comes back may be one where a permission had since been revoked, and
// applying it would be revoking the revocation.
func TestGateRefusesARewoundRecord(t *testing.T) {
	r := sampleRecord()
	r.Rev = 3
	local := sampleRecord()

	d := Gate(&local, r, 7, nil)
	if !d.Skip {
		t.Error("a record older than the last one this device saw was applied")
	}
	if len(d.Warnings) != 1 || d.Warnings[0].Kind != WarnRollback {
		t.Errorf("warnings = %+v", d.Warnings)
	}
	if !strings.Contains(d.Warnings[0].Detail, "7") {
		t.Errorf("the warning does not say what was expected: %q", d.Warnings[0].Detail)
	}
	if len(d.Pending) != 0 || len(d.ApplyHostKeys) != 0 {
		t.Error("a rewound record still produced something to apply")
	}

	// Equal is fine: a sync that changed nothing re-reads the same revision.
	r.Rev = 7
	if Gate(&local, r, 7, nil).Skip {
		t.Error("the revision this device already has was treated as a rewind")
	}
}

// Two records claiming different keys for one address is the repository
// disagreeing with itself (§6.3).
func TestAddressConflictsAcrossRecords(t *testing.T) {
	a := sampleRecord()
	a.ID = "11111111-1111-4111-8111-111111111111"
	a.HostKeys = []RecordHostKey{{Alg: "ssh-ed25519", Key: "AAAA", Fingerprint: "SHA256:aaa"}}

	b := sampleRecord()
	b.ID = "22222222-2222-4222-8222-222222222222"
	b.Host.User = "root" // same server, another account: an ordinary thing to have
	b.HostKeys = []RecordHostKey{{Alg: "ssh-ed25519", Key: "BBBB", Fingerprint: "SHA256:bbb"}}

	warns := AddressConflicts([]Record{a, b})
	if len(warns) != 1 || warns[0].Kind != WarnAddressConflict {
		t.Fatalf("warnings = %+v", warns)
	}
	if !strings.Contains(warns[0].Detail, "SHA256:aaa") || !strings.Contains(warns[0].Detail, "SHA256:bbb") {
		t.Errorf("the warning does not show both fingerprints: %q", warns[0].Detail)
	}

	// Agreeing is silence, and so is a tombstone.
	b.HostKeys = a.HostKeys
	if w := AddressConflicts([]Record{a, b}); len(w) != 0 {
		t.Errorf("two records that agree warned: %+v", w)
	}
	b.HostKeys = []RecordHostKey{{Alg: "ssh-ed25519", Key: "BBBB"}}
	b.Deleted = true
	if w := AddressConflicts([]Record{a, b}); len(w) != 0 {
		t.Errorf("a tombstone was compared: %+v", w)
	}

	// The answer does not depend on the order they arrived in.
	b.Deleted = false
	first := AddressConflicts([]Record{a, b})
	second := AddressConflicts([]Record{b, a})
	if len(first) != len(second) {
		t.Errorf("order changed the answer: %d vs %d", len(first), len(second))
	}
}

// A tombstone is not gated on policy — there is nothing in it to gate.
//
// Nothing this app writes produces one any more: a settings file is a snapshot,
// and an import never deletes. The case is kept because a record carries the flag
// and a file may come from elsewhere — a future version, or another tool — and
// "what happens to a tombstone" should be a decided question rather than an
// accident.
func TestGatePassesATombstoneThrough(t *testing.T) {
	r := Record{ID: testID, Rev: 9, UpdatedAt: time.Now().UTC(), UpdatedBy: "device-b",
		Deleted: true, Policy: StrictestPolicy()}
	local := sampleRecord()
	d := Gate(&local, r, 0, nil)
	if d.Skip || len(d.Pending) != 0 {
		t.Errorf("a tombstone produced %+v", d)
	}
	if !d.Apply.Deleted {
		t.Error("the tombstone lost its deleted flag")
	}
	if d.Apply.Host.Hostname != "" || d.Apply.Auth.KeyFingerprint != "" {
		t.Errorf("the tombstone still describes the host: %+v", d.Apply)
	}
}
