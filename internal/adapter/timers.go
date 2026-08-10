package adapter

// Scheduled jobs (v1.x): systemd timers.
//
// Read-only for now. Editing a schedule means writing a unit file and running
// `systemctl daemon-reload`, and getting that half-right leaves a server with a
// job that silently never runs — the same class of failure this project keeps
// finding. Listing is the useful half and carries no such risk.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
)

// Timer is one systemd timer unit.
type Timer struct {
	Unit      string `json:"unit"`
	Activates string `json:"activates"`
	// Next and Last are unix seconds; 0 means "never" — systemd reports that as
	// a zero timestamp, and rendering it as 1970 would be worse than a dash.
	Next int64 `json:"next"`
	Last int64 `json:"last"`
	// Description comes from the unit, which list-timers does not include, so
	// it is filled in from the service listing when available.
	Description string `json:"description,omitempty"`
}

// NeverRun reports a timer that has not fired yet.
func (t Timer) NeverRun() bool { return t.Last == 0 }

// ListTimersArgs returns the argv for listing timers.
//
// --all includes inactive timers: a timer that is installed but not running is
// exactly what someone debugging "why didn't the backup happen" needs to see.
func ListTimersArgs(json bool) []string {
	args := []string{"list-timers", "--all", "--no-pager"}
	if json {
		return append(args, "--output=json")
	}
	return args
}

// timerRow mirrors one element of `systemctl list-timers --output=json`.
//
// The timestamps are microseconds since the epoch, not seconds — a 16-digit
// number where a unix timestamp has 10. Treating them as seconds puts every
// timer fifty thousand years in the future.
type timerRow struct {
	Unit      string `json:"unit"`
	Activates string `json:"activates"`
	Next      int64  `json:"next"`
	Last      int64  `json:"last"`
}

// ParseTimers parses the JSON form.
func ParseTimers(data []byte) ([]Timer, error) {
	out := []Timer{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return out, nil
	}

	var rows []timerRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("adapter: parse list-timers: %w", err)
	}
	for _, r := range rows {
		if r.Unit == "" {
			continue
		}
		out = append(out, Timer{
			Unit:      r.Unit,
			Activates: r.Activates,
			Next:      microsToUnix(r.Next),
			Last:      microsToUnix(r.Last),
		})
	}
	return out, nil
}

// microsToUnix converts systemd's microsecond timestamps, preserving 0 as
// "never" rather than turning it into 1970.
func microsToUnix(micros int64) int64 {
	if micros <= 0 {
		return 0
	}
	return micros / 1_000_000
}

// ParseTimersTable parses `systemctl list-timers --all --no-pager` for systemd
// versions without JSON output (below 246 — see the note in linuxsystemd).
//
// The columns are NEXT, LEFT, LAST, PASSED, UNIT, ACTIVATES, and the timestamp
// columns contain spaces ("Fri 2026-08-07 01:29:01 UTC") while also being
// literally "n/a" when unset. Parsing from the left is therefore hopeless; the
// unit name is found by looking for the ".timer" token instead.
func ParseTimersTable(data []byte) ([]Timer, error) {
	out := []Timer{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NEXT") || strings.Contains(line, "timers listed") {
			continue
		}
		fields := strings.Fields(line)

		unitIdx := -1
		for i, f := range fields {
			if strings.HasSuffix(f, ".timer") {
				unitIdx = i
				break
			}
		}
		if unitIdx < 0 {
			continue
		}
		t := Timer{Unit: fields[unitIdx]}
		if unitIdx+1 < len(fields) {
			t.Activates = fields[unitIdx+1]
		}
		// The table gives human dates, not timestamps. Rather than parse a
		// locale-dependent format badly, the numeric fields are left at zero
		// and the UI falls back to what the table itself said.
		out = append(out, t)
	}
	return out, nil
}

// TimersSupportJSON reports whether this systemd emits JSON for list-timers.
// Same gate as the service listing.
func TimersSupportJSON(systemdVersion int) bool {
	return linuxsystemd.SupportsJSONOutput(systemdVersion)
}
