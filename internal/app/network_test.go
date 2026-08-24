package app

import (
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

func iface(name string) adapter.Interface {
	return adapter.Interface{Name: name, State: "UP"}
}

// The network tab polls for listeners, which change while you watch. Interfaces
// do not, and re-reading them on the same tick cost a second Exec channel out
// of the three a whole connection has.
func TestIfaceCacheServesWithinTheWindow(t *testing.T) {
	c := newIfaceCache()
	c.put("h", 1, []adapter.Interface{iface("eth0")})

	got, ok := c.get("h", 1)
	if !ok {
		t.Fatal("a listing read a moment ago was not reused")
	}
	if len(got) != 1 || got[0].Name != "eth0" {
		t.Errorf("got %+v", got)
	}
}

// Held for thirty seconds, not for the life of the connection: an address
// appears when a VPN comes up, and a cache with no expiry would show the old
// one until the user reconnected.
func TestIfaceCacheExpires(t *testing.T) {
	c := newIfaceCache()
	c.byID["h"] = ifaceEntry{gen: 1, at: time.Now().Add(-ifaceTTL - time.Second),
		items: []adapter.Interface{iface("eth0")}}

	if _, ok := c.get("h", 1); ok {
		t.Error("a listing older than the window was still served")
	}
}

// A reconnect can be a rebooted machine, and its addresses are not the ones
// cached against the connection that came before it.
func TestIfaceCacheIsPerConnection(t *testing.T) {
	c := newIfaceCache()
	c.put("h", 1, []adapter.Interface{iface("eth0")})

	if _, ok := c.get("h", 2); ok {
		t.Error("a listing from the previous connection was served to the new one")
	}
	// And the stale entry does not shadow the new connection's own read.
	c.put("h", 2, []adapter.Interface{iface("ens3")})
	got, ok := c.get("h", 2)
	if !ok || got[0].Name != "ens3" {
		t.Errorf("got %+v (ok=%v)", got, ok)
	}
}

func TestIfaceCacheForgets(t *testing.T) {
	c := newIfaceCache()
	c.put("h", 1, []adapter.Interface{iface("eth0")})
	c.forget("h")
	if _, ok := c.get("h", 1); ok {
		t.Error("forget left the listing in place")
	}
}

// The caller hands this straight to the frontend and to the MCP tool. One of
// them appending to it would corrupt what every later reader sees.
func TestIfaceCacheHandsOutACopy(t *testing.T) {
	c := newIfaceCache()
	c.put("h", 1, []adapter.Interface{iface("eth0")})

	first, _ := c.get("h", 1)
	first[0].Name = "clobbered"
	first = append(first, iface("extra"))
	_ = first

	second, _ := c.get("h", 1)
	if len(second) != 1 {
		t.Fatalf("an appending caller changed the cached length: %+v", second)
	}
	if second[0].Name != "eth0" {
		t.Errorf("cached entry was mutated through the returned slice: %+v", second)
	}
}

/* ------------------------------------------------- against a real server */

// countLine returns how many times a Command Log line has run, across the row
// that folded it and any rows that recorded a different outcome.
func countLine(a *App, substr string) int {
	n := 0
	for _, e := range a.CommandLog() {
		if !strings.Contains(e.Line, substr) {
			continue
		}
		if e.Repeat > 0 {
			n += e.Repeat
			continue
		}
		n++
	}
	return n
}

// The saving, measured rather than asserted about the cache in isolation: two
// polls of the network tab must run `ss` twice and `ip` once.
//
// Counted out of the Command Log because that is the record of what the server
// was actually asked, and it is the same surface the user reads.
func TestNetworkPollStopsRereadingInterfaces(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)
	defer stop()

	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	defer a.DisconnectHost("fixture")

	if _, err := a.HostNetwork("fixture"); err != nil {
		t.Fatalf("HostNetwork: %v", err)
	}
	a.ClearCommandLog()

	for range 3 {
		if _, err := a.HostNetwork("fixture"); err != nil {
			t.Fatalf("HostNetwork: %v", err)
		}
	}

	// The listener list is why this view polls at all.
	if got := countLine(a, "ss -tulnp"); got != 3 {
		t.Errorf("`ss` ran %d times across 3 polls, want 3 — the listener list "+
			"has to stay live", got)
	}
	// The interface list is not, and re-reading it is what cost a second Exec
	// channel on every tick.
	if got := countLine(a, "ip -j addr"); got != 0 {
		t.Errorf("`ip` ran %d more times within the cache window, want 0", got)
	}
}

// The refresh button means "something changed". Answering it from the cache
// would be the app deciding it knows better.
func TestRefreshHostNetworkRereadsInterfaces(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)
	defer stop()

	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	defer a.DisconnectHost("fixture")

	if _, err := a.HostNetwork("fixture"); err != nil {
		t.Fatalf("HostNetwork: %v", err)
	}
	a.ClearCommandLog()

	if _, err := a.RefreshHostNetwork("fixture"); err != nil {
		t.Fatalf("RefreshHostNetwork: %v", err)
	}
	if got := countLine(a, "ip -j addr"); got != 1 {
		t.Errorf("`ip` ran %d times on an explicit refresh, want 1", got)
	}
}
