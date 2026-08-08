package app

import (
	"context"
	"fmt"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
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

	// list-timers carries no description. The service listing does, and it is
	// already being fetched for the services tab, so joining is nearly free —
	// and "litedeck-backup.timer" alone tells the user much less than the
	// sentence the unit author wrote.
	if units, err := a.ListServices(hostID); err == nil {
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
