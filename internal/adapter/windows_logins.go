package adapter

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Who got in and who kept knocking, on Windows (§4.5).
//
// The POSIX path reads wtmp with `last` for the successes and the journal for
// the failures. Windows writes neither. Both halves are in one place instead —
// the OpenSSH/Operational event log — which makes this simpler than the POSIX
// version and adds one problem it does not have.
//
// # Every event is id 4
//
// The obvious way to separate a login from a failure is the event id, and there
// is no separation to be had: measured on Windows 10 19045, all 2,343 records in
// the log were id 4. The whole of the meaning is in the rendered message, which
// is sshd's own text and is not localised. So this matches on the message, the
// same strings sshd writes to syslog on Linux.
//
// # The log rolls over, and fast
//
// It is circular and 1 MB by default. On the measured box — one facing the
// internet, taking a routine password attack — 2,343 records covered **77
// minutes**, not the 24 hours asked for. Asking for a day and labelling the
// answer "the last day" would report a fraction of an hour as a day and make
// an attack look like it had just started. So the oldest record still in the
// log is reported alongside the counts, and the screen says that instead.
//
// # "Still logged in" is not something this log knows
//
// On Linux `last` prints "still logged in" from wtmp, which is written on the
// way out. Windows has no such record, and the absence of a disconnect does not
// mean the session is live: measured on the same box, three logins from the app
// itself had an Accepted record and no close record of any kind, long after the
// connections were gone. So a login with no end gets no duration and no claim
// either way. The session table directly above this pane answers "who is here
// now" from the process list, which is the thing that actually knows.
//
// # A failed attempt is written twice
//
// "Failed password for invalid user root from ..." and "Invalid user root
// from ..." are one attempt, logged as two records: 734 and 735 of them in the
// same window. Only the first is counted.

// windowsLoginHours matches the POSIX window. What actually comes back is
// bounded by the log's own size, which is why the answer carries its own span.
const windowsLoginHours = 24

// windowsLoginRows caps the successes reported. The failures are counted rather
// than listed, so only this half can grow without bound.
const windowsLoginRows = 200

// WindowsLoginsScript reads the log once and emits both halves.
//
// The successes come out as rows and the failures come out already counted, in
// the shape the POSIX awk pass emits, so ParseAuthAggregate reads both platforms.
func WindowsLoginsScript() string {
	return strings.Join([]string{
		`$ErrorActionPreference = 'SilentlyContinue'`,
		`$epoch = (Get-Date '1970-01-01Z').ToUniversalTime()`,
		`function Unix($d) { [int64]($d.ToUniversalTime() - $epoch).TotalSeconds }`,
		// The boot record, which `last` puts in the same list and which is the
		// most useful row in it.
		`$b = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime`,
		`if ($b) { Write-Output ("#boot|" + (Unix $b)) }`,
		`$since = (Get-Date).AddHours(-` + strconv.Itoa(windowsLoginHours) + `)`,
		`$es = @(Get-WinEvent -FilterHashtable @{LogName='OpenSSH/Operational'; StartTime=$since} -EA SilentlyContinue)`,
		`if ($es.Count -eq 0) { Write-Output '#nolog'; return }`,
		// The oldest record still in the log. Everything before it was
		// overwritten, and the screen has to say so rather than claim a day.
		`Write-Output ("#span|" + (Unix $es[$es.Count-1].TimeCreated))`,
		`$ips = @{}; $users = @{}; $failed = 0; $accepted = 0; $rows = 0`,
		`foreach ($e in $es) {`,
		`  $m = ($e.Message -replace "\s+", " ")`,
		`  if ($m -match 'Accepted \w+ for (\S+) from (\S+) port (\d+)') {`,
		`    $accepted++`,
		`    if ($rows -lt ` + strconv.Itoa(windowsLoginRows) + `) {`,
		`      Write-Output ("#ok|" + (Unix $e.TimeCreated) + "|" + $matches[1] + "|" + $matches[2] + "|" + $matches[3])`,
		`      $rows++`,
		`    }`,
		`  } elseif ($m -match 'Disconnected from (?:\w+ user \S+ )?(\S+) port (\d+)') {`,
		// How a session ended. Paired to its login by address and client port,
		// which is the only thing the two records share.
		`    Write-Output ("#end|" + (Unix $e.TimeCreated) + "|" + $matches[1] + "|" + $matches[2])`,
		`  } elseif ($m -match 'Failed \S+ for (?:invalid user )?(\S+) from (\S+) port') {`,
		// Not 'Invalid user ...', which is the same attempt written a second
		// time. Counting both doubles every number on the screen.
		`    $failed++`,
		`    $users[$matches[1]] = 1 + $users[$matches[1]]`,
		`    $ips[$matches[2]] = 1 + $ips[$matches[2]]`,
		`  }`,
		`}`,
		// The aggregate, in the shape the POSIX pass emits. Every distinct
		// source and account, not the top few: the cut is Go's, because the
		// screen says "8 of 36" and cannot count the 36 from a list of 8.
		`Write-Output ("total " + $failed + " " + $accepted)`,
		`foreach ($k in $ips.Keys) { Write-Output ("ip " + $ips[$k] + " " + $k) }`,
		`foreach ($k in $users.Keys) { Write-Output ("user " + $users[$k] + " " + $k) }`,
	}, "\n")
}

// WindowsLogins is what the log could say.
type WindowsLogins struct {
	Logins []Login
	Auth   AuthSummary
	// Since is the oldest record the log still holds. Zero when the log is
	// empty or unreadable; never earlier than the window that was asked for.
	Since time.Time
	// HasLog is false when OpenSSH/Operational answered with nothing at all,
	// which is a different thing from "nobody logged in".
	HasLog bool
}

type winEnd struct {
	at   int64
	addr string
	port string
}

// ParseWindowsLogins reads what WindowsLoginsScript printed.
func ParseWindowsLogins(raw string) WindowsLogins {
	out := WindowsLogins{Logins: []Login{}, HasLog: true}
	var ends []winEnd
	// Client port to the login it belongs to, so an end can find its start.
	openBy := map[string][]int{}
	var rest strings.Builder
	var boot int64

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if !strings.HasPrefix(line, "#") {
			rest.WriteString(line)
			rest.WriteByte('\n')
			continue
		}
		f := strings.Split(line, "|")
		switch {
		case line == "#nolog":
			out.HasLog = false
		case len(f) == 2 && f[0] == "#boot":
			boot, _ = strconv.ParseInt(f[1], 10, 64)
		case len(f) == 2 && f[0] == "#span":
			if at, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				out.Since = time.Unix(at, 0)
			}
		case len(f) == 5 && f[0] == "#ok":
			at, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				continue
			}
			key := f[3] + ":" + f[4]
			// Open stays false: see "Still logged in" above. A row with no end
			// shows an empty duration, which is what the log actually knows.
			openBy[key] = append(openBy[key], len(out.Logins))
			out.Logins = append(out.Logins, Login{
				User: f[2],
				From: f[3],
				At:   time.Unix(at, 0),
			})
		case len(f) == 4 && f[0] == "#end":
			at, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				continue
			}
			ends = append(ends, winEnd{at: at, addr: f[2], port: f[3]})
		}
	}

	closeSessions(out.Logins, openBy, ends)

	if boot > 0 {
		// Where `last` would put it: in the same list, in time order.
		out.Logins = append(out.Logins, Login{At: time.Unix(boot, 0), Boot: true})
	}
	// Newest first, the order `last` prints and the list is read in.
	sort.SliceStable(out.Logins, func(i, j int) bool {
		return out.Logins[i].At.After(out.Logins[j].At)
	})
	out.Auth = ParseAuthAggregate(rest.String())
	return out
}

// closeSessions gives each login its end time.
//
// A client port is reused, so an end belongs to the newest login on that
// address and port that started before it and has not been closed yet. Matching
// on the address alone would close somebody else's session every time one person
// opened two.
func closeSessions(logins []Login, openBy map[string][]int, ends []winEnd) {
	// Oldest first, so each end takes the login it actually belongs to rather
	// than the newest one that happens to share the port.
	sort.Slice(ends, func(i, j int) bool { return ends[i].at < ends[j].at })
	for _, e := range ends {
		best := -1
		for _, idx := range openBy[e.addr+":"+e.port] {
			if logins[idx].Until != nil || logins[idx].At.Unix() > e.at {
				continue
			}
			if best < 0 || logins[idx].At.After(logins[best].At) {
				best = idx
			}
		}
		if best < 0 {
			// A disconnect for a login the log no longer holds. Nothing to
			// close, and inventing a row for it would put a session on the
			// screen that this parser never saw start.
			continue
		}
		at := time.Unix(e.at, 0)
		logins[best].Until = &at
	}
}
