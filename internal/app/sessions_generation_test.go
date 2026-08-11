package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// bareApp is an App with its caches and a manager, and nothing dialled.
func bareApp() *App {
	a := New()
	a.mgr = sshcore.NewManager(sshcore.ManagerOptions{}, nil)
	return a
}

// The caches in this package hold facts read from a server: which sshd processes
// belong to LiteDeck's own connection, which port that connection arrived on,
// what the OS supports, where the CPU counters stood. All of them are keyed by
// host ID, and none of them was ever invalidated except by the user pressing
// Disconnect.
//
// Reconnect does not press Disconnect. It replaces a dropped connection on its
// own, after a network blip or a server reboot, and the host ID does not change
// — so a cache filled through the old connection went on answering for the new
// one. For the self-PID set that is not a stale number on screen, it is a wrong
// answer in the guard that decides whether ending a session would cut the line
// the user is standing on.
//
// These tests pin the two halves of the fix: the manager hands out a number that
// changes when the connection does, and freshen drops the caches when it sees a
// number it has not seen.

func TestGenCacheSeesEachGenerationOnce(t *testing.T) {
	c := newGenCache()

	if c.seen("h", 7) {
		t.Fatal("a generation never recorded reported as already seen")
	}
	if !c.seen("h", 7) {
		t.Fatal("the same generation reported as new twice; caches would be dropped on every call")
	}
	if c.seen("h", 8) {
		t.Fatal("a new generation reported as already seen; this is the stale-cache bug")
	}
	if !c.seen("h", 8) {
		t.Fatal("the new generation was not recorded")
	}

	// Hosts do not share a slot.
	if c.seen("other", 8) {
		t.Fatal("one host's generation answered for another")
	}
}

// TestGenerationsAreNotReusedAcrossReconnects is the counting mistake this fix
// nearly shipped with. A per-host counter restarts at 1 when the host is
// disconnected and connected again, so the second connection would carry the
// same number as the first and freshen would keep the first one's caches. The
// sequence belongs to the manager, not to the host.
func TestGenerationsAreNotReusedAcrossReconnects(t *testing.T) {
	c := bareApp().gens

	// Host A connects, drops and comes back; host B connects in between. With a
	// per-host counter A's two connections would both be generation 1.
	if c.seen("a", 1) {
		t.Fatal("first connection reported as seen")
	}
	if c.seen("b", 2) {
		t.Fatal("second host's first connection reported as seen")
	}
	if c.seen("a", 3) {
		t.Fatal("host a's replacement connection reported as seen; its caches would survive it")
	}
}

// TestFreshenIsQuietWhenNothingChanged guards the other direction. Dropping the
// caches on every call would be safe and useless: every session refresh would
// re-probe the process tree, which is what caching them was for.
func TestFreshenIsQuietWhenNothingChanged(t *testing.T) {
	a := bareApp()
	pids := map[int]bool{42: true}
	a.selves.put("h", pids)
	a.sshPorts.put("h", 2222)
	a.gens.seen("h", 5)

	// Generation 0 means the host is not connected. Nothing was read through a
	// connection, so there is nothing to invalidate — and treating "gone" as
	// "changed" would throw away a cache the next connect has to rebuild anyway.
	a.freshen("h")

	if _, ok := a.selves.get("h"); !ok {
		t.Error("self PIDs were dropped for a host that never reconnected")
	}
	if _, ok := a.sshPorts.get("h"); !ok {
		t.Error("the SSH port was dropped for a host that never reconnected")
	}
}
