package cfgsync

import (
	"fmt"
	"time"
)

// Deciding what the repository should say about one host (§8.3).
//
// # No git merge
//
// git never merges anything here. Every sync makes a new commit on top of the
// remote HEAD and fast-forward pushes it; if somebody got there first the push is
// refused and the whole thing starts again. Conflicts are resolved on decrypted
// records, by the rules below, where "conflict" means something a person would
// recognise — two machines renamed one host — rather than two ciphertexts that
// differ.
//
// # base
//
// The record as this machine last saw it. It is what makes "changed" answerable:
// without it, a record that differs from the remote one could be a local edit or a
// remote edit, and the only safe reading of that is to ask about everything.

// Action is what a sync should do with one record.
type Action int

const (
	// ActionNone: the two sides agree.
	ActionNone Action = iota
	// ActionPush: this machine has something the repository does not.
	ActionPush
	// ActionAccept: the repository has something this machine does not.
	ActionAccept
	// ActionMerge: both changed. Merged holds the result, to apply here and push.
	ActionMerge
)

func (a Action) String() string {
	switch a {
	case ActionPush:
		return "push"
	case ActionAccept:
		return "accept"
	case ActionMerge:
		return "merge"
	default:
		return "none"
	}
}

// Conflict is a value that lost, kept so the person can see what was overwritten
// (§8.3).
//
// Clocks are not reliable enough to settle an edit war silently (§6.7 ⑦): two
// machines minutes apart can resolve the wrong way round. What makes that
// acceptable is that the losing value is written to the sync history and shown,
// rather than being quietly gone.
type Conflict struct {
	RecordID string    `json:"recordId"`
	Field    string    `json:"field"`
	Kept     string    `json:"kept"`
	Dropped  string    `json:"dropped"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
}

// MergeResult is what to do with one record.
type MergeResult struct {
	Action Action
	// Record is the value to apply locally and, for push and merge, to write to
	// the repository. Gate still has a say over what reaches this machine.
	Record    Record
	Conflicts []Conflict
	Warnings  []Warning
}

// Merge settles one record (§8.3).
//
// base is the record as this machine last synced it (nil if never), local is what
// this machine has now (nil if the host does not exist here), remote is what the
// repository holds (nil if there is no file for it). deviceID and now stamp a
// record this machine is about to push — passed in rather than read, so every row
// of the table is reproducible.
func Merge(base, local, remote *Record, deviceID string, now time.Time) MergeResult {
	switch {
	case local == nil && remote == nil:
		return MergeResult{Action: ActionNone}

	case remote == nil:
		// Only here. Either a host added on this machine and never synced, or —
		// if this machine has synced it before — a record that vanished from the
		// repository without a tombstone, which is the same event as a rollback
		// and is not a reason to delete anything (§6.4).
		res := MergeResult{Action: ActionPush, Record: stamp(*local, local.Rev+1, deviceID, now)}
		if base != nil {
			res.Warnings = append(res.Warnings, Warning{
				RecordID: local.ID, Kind: WarnVanished,
				Detail: "the record is gone from the repository with no tombstone; " +
					"this device is pushing its own copy back",
			})
		}
		return res

	case local == nil:
		// A host this machine does not have. Three different situations, and
		// telling them apart is what base is for.
		if remote.Deleted {
			// Deleted somewhere and already gone here. Nothing to do.
			return MergeResult{Action: ActionNone}
		}
		if base != nil {
			// This machine had it and no longer does: somebody deleted it here.
			// Without this the record would be accepted straight back and the
			// host would reappear at the next sync, which is the deletion being
			// silently undone by the thing that was supposed to carry it.
			return MergeResult{Action: ActionPush, Record: Tombstone(*remote, deviceID, now)}
		}
		return MergeResult{Action: ActionAccept, Record: *remote}
	}

	localChanged := base == nil || !local.SameContent(*base)
	remoteChanged := base == nil || !remote.SameContent(*base)

	// A delete from another machine, for a host changed here since. Not honoured:
	// the change may be the reason somebody wants the host, and a delete that
	// takes an unseen edit with it is not recoverable from this side.
	if remote.Deleted && !local.Deleted {
		if localChanged {
			return MergeResult{
				Action: ActionNone,
				Record: *local,
				Warnings: []Warning{{
					RecordID: local.ID, Kind: WarnDeletedButModified,
					Detail: fmt.Sprintf("deleted on device %s, but changed here since", remote.UpdatedBy),
				}},
			}
		}
		return MergeResult{Action: ActionAccept, Record: *remote}
	}

	switch {
	case !localChanged && !remoteChanged:
		return MergeResult{Action: ActionNone, Record: *local}
	case localChanged && !remoteChanged:
		return MergeResult{Action: ActionPush, Record: stamp(*local, maxRev(*local, *remote)+1, deviceID, now)}
	case !localChanged && remoteChanged:
		return MergeResult{Action: ActionAccept, Record: *remote}
	}

	return mergeBoth(*local, *remote, deviceID, now)
}

// mergeBoth resolves a record both sides changed (§8.3).
func mergeBoth(local, remote Record, deviceID string, now time.Time) MergeResult {
	res := MergeResult{Action: ActionMerge}

	// Whose ordinary fields win: the later timestamp, and the device ID
	// alphabetically where the two are equal. A tie-break that depends on which
	// machine is asking would let the two of them push back and forth forever.
	remoteWins := remote.UpdatedAt.After(local.UpdatedAt) ||
		(remote.UpdatedAt.Equal(local.UpdatedAt) && remote.UpdatedBy > local.UpdatedBy)

	winner, loser := local, remote
	if remoteWins {
		winner, loser = remote, local
	}
	out := winner

	if winner.Host != loser.Host {
		res.Conflicts = append(res.Conflicts, Conflict{
			RecordID: local.ID, Field: FieldConnection,
			Kept:    describeHost(winner.Host),
			Dropped: describeHost(loser.Host),
			By:      loser.UpdatedBy, At: loser.UpdatedAt,
		})
	}
	if !sameStrings(winner.Auth.Methods, loser.Auth.Methods) ||
		winner.Auth.KeyFingerprint != loser.Auth.KeyFingerprint {
		res.Conflicts = append(res.Conflicts, Conflict{
			RecordID: local.ID, Field: FieldAuthMethods,
			Kept:    describeAuth(winner.Auth),
			Dropped: describeAuth(loser.Auth),
			By:      loser.UpdatedBy, At: loser.UpdatedAt,
		})
	}

	// Policy does not go by the clock. Field by field, the stricter value wins,
	// whichever machine holds it and whenever it was written (§6.2).
	out.Policy = strictestOf(local.Policy, remote.Policy)

	// Host keys stay as this machine has them. known_hosts is this machine's
	// record of what it has actually seen on the wire; a repository is not
	// evidence about that (§6.3). Gate turns the difference into something a
	// person decides.
	out.HostKeys = local.HostKeys
	if !sameKeys(local.HostKeys, remote.HostKeys) {
		res.Warnings = append(res.Warnings, Warning{
			RecordID: local.ID, Kind: WarnHostKeyMismatch,
			Detail: fmt.Sprintf("%s: the repository and this device list different host keys", local.Addr()),
		})
	}

	// Deleting wins over editing when both machines were explicit about it: one
	// of them said delete and the other did not say keep.
	out.Deleted = local.Deleted || remote.Deleted

	res.Record = stamp(out, maxRev(local, remote)+1, deviceID, now)
	return res
}

// strictestOf takes the stricter value of each policy field (§6.2).
func strictestOf(a, b RecordPolicy) RecordPolicy {
	out := RecordPolicy{
		Shared:        a.Shared && b.Shared,
		ExecEnabled:   a.ExecEnabled && b.ExecEnabled,
		DeleteEnabled: a.DeleteEnabled && b.DeleteEnabled,
		MCPApproval:   a.MCPApproval,
	}
	if Strictness(b.MCPApproval) > Strictness(a.MCPApproval) {
		out.MCPApproval = b.MCPApproval
	}
	if out.MCPApproval == "" {
		out.MCPApproval = ApprovalAsk
	}
	return out
}

// Tombstone is the record that marks a host deleted (§8.3).
//
// The file stays in the repository. Removing it would make an ordinary delete
// indistinguishable from a history rewrite, and one of those is an attack (§6.4).
// The host's details go with it: a tombstone is not a place to keep a copy of
// something the user asked to be rid of.
func Tombstone(r Record, deviceID string, now time.Time) Record {
	return Record{
		ID:        r.ID,
		Rev:       r.Rev + 1,
		UpdatedAt: now.UTC().Truncate(time.Second),
		UpdatedBy: deviceID,
		Deleted:   true,
		Policy:    StrictestPolicy(),
	}
}

func stamp(r Record, rev int64, deviceID string, now time.Time) Record {
	r.Rev = rev
	r.UpdatedAt = now.UTC().Truncate(time.Second)
	r.UpdatedBy = deviceID
	r.normalise()
	return r
}

func maxRev(a, b Record) int64 {
	if a.Rev > b.Rev {
		return a.Rev
	}
	return b.Rev
}

func describeHost(h RecordHost) string {
	s := fmt.Sprintf("%s (%s@%s:%d)", h.Name, h.User, h.Hostname, h.Port)
	if h.ProxyJump != "" {
		s += " via " + h.ProxyJump
	}
	if h.Group != "" {
		s += " [" + h.Group + "]"
	}
	return s
}

func describeAuth(a RecordAuth) string {
	s := fmt.Sprint(a.Methods)
	if a.KeyLabel != "" {
		s += " " + a.KeyLabel
	}
	if a.KeyFingerprint != "" {
		s += " " + a.KeyFingerprint
	}
	return s
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameKeys(a, b []RecordHostKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Alg != b[i].Alg || a[i].Key != b[i].Key {
			return false
		}
	}
	return true
}
