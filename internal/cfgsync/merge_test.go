package cfgsync

import (
	"testing"
	"time"
)

var (
	t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func rec(mut func(*Record)) *Record {
	r := sampleRecord()
	r.Rev = 5
	r.UpdatedAt = t0
	r.UpdatedBy = "device-a"
	r.HostKeys = nil
	if mut != nil {
		mut(&r)
	}
	return &r
}

// Every row of the merge table (§8.3).
func TestMergeTable(t *testing.T) {
	base := rec(nil)

	cases := []struct {
		name    string
		base    *Record
		local   *Record
		remote  *Record
		want    Action
		wantRev int64
		check   func(t *testing.T, res MergeResult)
	}{
		{
			name: "neither changed", base: base, local: rec(nil), remote: rec(nil),
			want: ActionNone,
		},
		{
			name: "local changed", base: base,
			local:  rec(func(r *Record) { r.Host.Name = "renamed-here" }),
			remote: rec(nil),
			want:   ActionPush, wantRev: 6,
			check: func(t *testing.T, res MergeResult) {
				if res.Record.Host.Name != "renamed-here" {
					t.Errorf("pushed %q", res.Record.Host.Name)
				}
				if res.Record.UpdatedBy != "device-b" {
					t.Errorf("the push is not stamped with this device: %q", res.Record.UpdatedBy)
				}
			},
		},
		{
			name: "remote changed", base: base, local: rec(nil),
			remote: rec(func(r *Record) { r.Host.Name = "renamed-there"; r.Rev = 6; r.UpdatedBy = "device-c" }),
			want:   ActionAccept, wantRev: 6,
			check: func(t *testing.T, res MergeResult) {
				if res.Record.Host.Name != "renamed-there" {
					t.Errorf("accepted %q", res.Record.Host.Name)
				}
				if res.Record.UpdatedBy != "device-c" {
					t.Error("an accepted record was restamped, which would look like a local change")
				}
			},
		},
		{
			name: "a host this machine does not have", base: nil, local: nil,
			remote: rec(nil),
			want:   ActionAccept, wantRev: 5,
		},
		{
			// The difference from the row above is base: this machine had the host
			// and no longer does. Accepting the record back would undo the delete
			// at the next sync, silently, using the mechanism meant to carry it.
			name: "deleted here, still in the repository", base: base, local: nil,
			remote: rec(nil),
			want:   ActionPush, wantRev: 6,
			check: func(t *testing.T, res MergeResult) {
				if !res.Record.Deleted {
					t.Error("a host deleted on this device was not tombstoned")
				}
				if res.Record.Host.Hostname != "" {
					t.Errorf("the tombstone still describes the host: %+v", res.Record.Host)
				}
			},
		},
		{
			name: "a host added here and never synced", base: nil,
			local: rec(func(r *Record) { r.Rev = 1 }), remote: nil,
			want: ActionPush, wantRev: 2,
			check: func(t *testing.T, res MergeResult) {
				if len(res.Warnings) != 0 {
					t.Errorf("a new local host warned: %+v", res.Warnings)
				}
			},
		},
		{
			name: "a record that vanished from the repository", base: base,
			local: rec(nil), remote: nil,
			want: ActionPush, wantRev: 6,
			check: func(t *testing.T, res MergeResult) {
				// A normal delete leaves a tombstone, so a file that is simply
				// gone is the same event as a rewound history (§6.4).
				if len(res.Warnings) != 1 || res.Warnings[0].Kind != WarnVanished {
					t.Errorf("warnings = %+v", res.Warnings)
				}
			},
		},
		{
			name: "deleted elsewhere, untouched here", base: base, local: rec(nil),
			remote: func() *Record { r := Tombstone(*base, "device-c", t1); return &r }(),
			want:   ActionAccept,
			check: func(t *testing.T, res MergeResult) {
				if !res.Record.Deleted {
					t.Error("the delete was not accepted")
				}
			},
		},
		{
			name: "deleted elsewhere, changed here", base: base,
			local:  rec(func(r *Record) { r.Host.User = "someone-else" }),
			remote: func() *Record { r := Tombstone(*base, "device-c", t1); return &r }(),
			want:   ActionNone,
			check: func(t *testing.T, res MergeResult) {
				// Not deleted. The change may be the reason somebody wants it, and
				// a delete that takes an unseen edit with it cannot be undone from
				// this side.
				if res.Record.Deleted {
					t.Error("a host changed on this device was deleted by another one")
				}
				if len(res.Warnings) != 1 || res.Warnings[0].Kind != WarnDeletedButModified {
					t.Errorf("warnings = %+v", res.Warnings)
				}
			},
		},
		{
			name: "deleted here", base: base,
			local:  rec(func(r *Record) { r.Deleted = true }),
			remote: rec(nil),
			want:   ActionPush, wantRev: 6,
			check: func(t *testing.T, res MergeResult) {
				if !res.Record.Deleted {
					t.Error("the tombstone was not pushed")
				}
			},
		},
		{
			name: "gone from both sides", base: base, local: nil, remote: nil,
			want: ActionNone,
		},
		{
			name: "gone here, tombstoned there", base: base, local: nil,
			remote: func() *Record { r := Tombstone(*base, "device-c", t1); return &r }(),
			want:   ActionNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Merge(tc.base, tc.local, tc.remote, "device-b", t2)
			if res.Action != tc.want {
				t.Fatalf("action = %v, want %v", res.Action, tc.want)
			}
			if tc.wantRev != 0 && res.Record.Rev != tc.wantRev {
				t.Errorf("rev = %d, want %d", res.Record.Rev, tc.wantRev)
			}
			if tc.check != nil {
				tc.check(t, res)
			}
		})
	}
}

// Both sides changed: the clock settles the ordinary fields and nothing else.
func TestMergeBothChanged(t *testing.T) {
	base := rec(nil)

	t.Run("the later change wins the name, and the loser is kept for the history", func(t *testing.T) {
		local := rec(func(r *Record) { r.Host.Name = "here"; r.UpdatedAt = t1; r.UpdatedBy = "device-b" })
		remote := rec(func(r *Record) { r.Host.Name = "there"; r.UpdatedAt = t2; r.UpdatedBy = "device-c"; r.Rev = 6 })

		res := Merge(base, local, remote, "device-b", t2)
		if res.Action != ActionMerge {
			t.Fatalf("action = %v", res.Action)
		}
		if res.Record.Host.Name != "there" {
			t.Errorf("name = %q, want the later change", res.Record.Host.Name)
		}
		if res.Record.Rev != 7 {
			t.Errorf("rev = %d, want max(5,6)+1", res.Record.Rev)
		}
		// The value that lost is not silently gone: clocks are not reliable enough
		// for that (§6.7 ⑦).
		if len(res.Conflicts) != 1 || res.Conflicts[0].Field != FieldConnection {
			t.Fatalf("conflicts = %+v", res.Conflicts)
		}
		if got := res.Conflicts[0]; got.Kept == "" || got.Dropped == "" || got.By != "device-b" {
			t.Errorf("the conflict record is not usable: %+v", got)
		}
	})

	t.Run("equal timestamps are settled by device id, not by who is asking", func(t *testing.T) {
		local := rec(func(r *Record) { r.Host.Name = "here"; r.UpdatedAt = t1; r.UpdatedBy = "device-b" })
		remote := rec(func(r *Record) { r.Host.Name = "there"; r.UpdatedAt = t1; r.UpdatedBy = "device-c" })

		fromB := Merge(base, local, remote, "device-b", t2)
		// The same pair seen from the other machine: local and remote swap.
		fromC := Merge(base, remote, local, "device-c", t2)
		if fromB.Record.Host.Name != fromC.Record.Host.Name {
			t.Errorf("the two machines disagree: %q vs %q — they would push back and forth forever",
				fromB.Record.Host.Name, fromC.Record.Host.Name)
		}
		if fromB.Record.Host.Name != "there" {
			t.Errorf("name = %q, want the higher device id", fromB.Record.Host.Name)
		}
	})

	t.Run("policy is settled by strictness, whenever it was written", func(t *testing.T) {
		// The loose side is the later one. Under the clock rule it would win; the
		// policy rule does not use the clock.
		local := rec(func(r *Record) {
			r.Policy = RecordPolicy{Shared: true, MCPApproval: ApprovalStrict}
			r.UpdatedAt = t1
			r.Host.Name = "here"
		})
		remote := rec(func(r *Record) {
			r.Policy = RecordPolicy{Shared: true, MCPApproval: ApprovalBypass, ExecEnabled: true, DeleteEnabled: true}
			r.UpdatedAt = t2
			r.Host.Name = "there"
		})

		res := Merge(base, local, remote, "device-b", t2)
		if res.Record.Host.Name != "there" {
			t.Errorf("the ordinary field did not go by the clock: %q", res.Record.Host.Name)
		}
		want := RecordPolicy{Shared: true, MCPApproval: ApprovalStrict}
		if res.Record.Policy != want {
			t.Errorf("policy = %+v, want the stricter of each field %+v", res.Record.Policy, want)
		}
	})

	t.Run("host keys stay as this machine has them", func(t *testing.T) {
		mine := RecordHostKey{Alg: "ssh-ed25519", Key: "AAAA", Fingerprint: "SHA256:aaa"}
		theirs := RecordHostKey{Alg: "ssh-ed25519", Key: "BBBB", Fingerprint: "SHA256:bbb"}
		local := rec(func(r *Record) { r.HostKeys = []RecordHostKey{mine}; r.UpdatedAt = t1 })
		remote := rec(func(r *Record) { r.HostKeys = []RecordHostKey{theirs}; r.UpdatedAt = t2 })

		res := Merge(base, local, remote, "device-b", t2)
		if len(res.Record.HostKeys) != 1 || res.Record.HostKeys[0].Key != "AAAA" {
			t.Errorf("host keys = %+v — known_hosts is what this machine saw on the wire",
				res.Record.HostKeys)
		}
		found := false
		for _, w := range res.Warnings {
			if w.Kind == WarnHostKeyMismatch {
				found = true
			}
		}
		if !found {
			t.Errorf("no host key warning: %+v", res.Warnings)
		}
	})
}

// A tombstone keeps nothing about the host.
func TestTombstoneKeepsOnlyTheIdentity(t *testing.T) {
	tomb := Tombstone(sampleRecord(), "device-b", t1)
	if tomb.ID != testID || !tomb.Deleted {
		t.Errorf("tombstone = %+v", tomb)
	}
	if tomb.Rev != sampleRecord().Rev+1 {
		t.Errorf("rev = %d", tomb.Rev)
	}
	if tomb.Host != (RecordHost{}) || len(tomb.HostKeys) != 0 || tomb.Auth.KeyFingerprint != "" {
		t.Errorf("the tombstone still describes a host the user asked to be rid of: %+v", tomb)
	}
	// It has to survive the round trip, because it lives in the repository
	// forever: removing the file would make an ordinary delete look like a
	// rewound history (§6.4).
	b, err := tomb.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := UnmarshalRecord(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.Deleted {
		t.Error("the tombstone lost its flag in the repository")
	}
}

// Merge and Gate compose: what merge decides to apply still has to get past the
// gate.
//
// This is the case the two of them could disagree about. Merge takes the stricter
// policy, so its result can only be stricter than or equal to what this machine
// has — which means Gate has nothing to hold back, and a host does not need two
// approvals for one change.
func TestAMergedRecordNeedsNoSecondApproval(t *testing.T) {
	base := rec(nil)
	local := rec(func(r *Record) {
		r.Policy = RecordPolicy{Shared: true, MCPApproval: ApprovalAsk}
		r.Host.Name = "here"
		r.UpdatedAt = t1
	})
	remote := rec(func(r *Record) {
		r.Policy = RecordPolicy{Shared: true, MCPApproval: ApprovalBypass, ExecEnabled: true}
		r.Host.Name = "there"
		r.UpdatedAt = t2
	})

	res := Merge(base, local, remote, "device-b", t2)
	d := Gate(local, res.Record, local.Rev, nil)
	if len(d.Pending) != 0 {
		t.Errorf("a merged record still needed approval: %+v", d.Pending)
	}
	if d.Apply.Policy.MCPApproval != ApprovalAsk || d.Apply.Policy.ExecEnabled {
		t.Errorf("the merged policy loosened after the gate: %+v", d.Apply.Policy)
	}
}
