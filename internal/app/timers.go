package app

import (
	"context"
	"fmt"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
	"sync"
)

// Scheduled jobs (v1.x): systemd timers, read-only.
//
// Editing a schedule means writing a unit file and reloading the daemon, and
// getting that half-right leaves a server with a job that silently never runs.
// Listing carries no such risk and answers the question people actually have:
// "was this supposed to run, and did it?"

// ListTimers returns the timer list, joined with unit descriptions.
func (a *App) ListTimers(hostID string) ([]adapter.Timer, error) {
	info, err := a.DetectHost(hostID)
	if err != nil {
		return nil, err
	}
	if !info.HasSystemd {
		return nil, fmt.Errorf("app: %s has no systemd timers", hostID)
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	useJSON := adapter.TimersSupportJSON(info.SystemdVersion)
	res, err := conn.Poll(ctx, "systemctl", adapter.ListTimersArgs(useJSON)...)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, res.Err()
	}

	var timers []adapter.Timer
	if useJSON {
		timers, err = adapter.ParseTimers(res.Stdout)
	} else {
		timers, err = adapter.ParseTimersTable(res.Stdout)
	}
	if err != nil {
		return nil, err
	}

	// list-timers carries no description. The service listing does, and
	// "litedeck-backup.timer" alone tells the user much less than the sentence
	// the unit author wrote.
	//
	// "Already being fetched for the services tab" was the reasoning and it was
	// wrong: nothing shares that result, so this ran the whole two-command
	// service listing again — and it takes two of the three Exec slots, which
	// this timer read then held on top of its own. Cached for the life of the
	// connection instead; unit descriptions do not change while somebody is
	// logged in.
	if units, err := a.describeUnits(hostID); err == nil {
		desc := make(map[string]string, len(units))
		for _, u := range units {
			desc[u.Name] = u.Description
		}
		for i := range timers {
			if d := desc[timers[i].Activates]; d != "" {
				timers[i].Description = d
			}
		}
	}
	return timers, nil
}

// describeUnits is ListServices, remembered for this connection.
//
// Only the descriptions are wanted here, and those are static: a unit file's
// Description= is read at load time and changes when somebody edits the file
// and reloads systemd. Getting a stale sentence next to a live schedule is a
// smaller cost than a second full service listing on every visit to the tab.
func (a *App) describeUnits(hostID string) ([]linuxsystemd.ServiceUnit, error) {
	gen := a.connGeneration(hostID)
	if units, ok := a.unitDescs.get(hostID, gen); ok {
		return units, nil
	}
	units, err := a.ListServices(hostID)
	if err != nil {
		return nil, err
	}
	a.unitDescs.put(hostID, gen, units)
	return units, nil
}

// unitDescCache holds one service listing per connection, for its descriptions.
type unitDescCache struct {
	mu   sync.Mutex
	byID map[string]unitDescEntry
}

type unitDescEntry struct {
	gen   uint64
	units []linuxsystemd.ServiceUnit
}

func newUnitDescCache() *unitDescCache { return &unitDescCache{byID: map[string]unitDescEntry{}} }

func (c *unitDescCache) get(id string, gen uint64) ([]linuxsystemd.ServiceUnit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok || e.gen != gen {
		return nil, false
	}
	return e.units, true
}

func (c *unitDescCache) put(id string, gen uint64, units []linuxsystemd.ServiceUnit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = unitDescEntry{gen: gen, units: units}
}
