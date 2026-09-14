package app

import (
	"context"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
	"sync"
	"time"
)

// Who got in, and who kept trying (T-26).
//
// One round trip for both halves, and they do not share a permission. wtmp is
// world-readable, so the successes come back for anybody; the failures live in
// the journal, and a user outside systemd-journal/adm sees an empty one with no
// error. So the two halves carry their own answer: a screen that showed "0
// failed logins" to somebody who simply cannot read them would be telling them
// the opposite of the truth about a machine under attack.

// LoginsView is what the sessions pane renders behind the live list.
type LoginsView struct {
	// Logins is wtmp, newest first as `last` prints it. Always readable.
	Logins []adapter.Login `json:"logins"`
	// Auth is the failure summary, and it is only meaningful when Access is ok.
	Auth adapter.AuthSummary `json:"auth"`
	// Access is about the failures alone. The successes above do not need the
	// journal and arrive whatever this says.
	Access EventAccess `json:"access"`
	// Window is how far back the failures were counted, echoed so the screen
	// cannot label a day's worth as an hour's.
	Window string `json:"window"`
	// Since is the oldest record the source still holds, when that is later
	// than the window asked for. Zero on a host whose log covers the window.
	//
	// Windows needs this and Linux does not. The OpenSSH event log is circular
	// and 1 MB: on a box taking a routine password attack it held 77 minutes,
	// and labelling that "the last 24 hours" turns an attack that has been
	// running all day into one that just started.
	Since *time.Time `json:"since,omitempty"`
}

// authWindow is how far back the failure count reaches.
//
// A day, because that is the unit the answer is used in — "is it being hit
// right now" — and because it is what was measured: 3,032 failures over 24
// hours took 583 ms to count on the server. A week of the same would be seven
// times the scan for a number nobody reads differently.
const authWindow = "-24h"

// HostLogins reads the login history and summarises the failures.
//
// elevate is the user answering the "retry as administrator" the pane offers
// after a needs-sudo answer, exactly as the event timeline does. Never chosen
// automatically (§7.2). It is threaded through rather than ignored: a button
// that cannot do the thing it names is worse than no button.
func (a *App) HostLogins(hostID string, elevate bool) (LoginsView, error) {
	info, err := a.requireCapability(hostID, adapter.CapSessions, i18n.S("접속 이력"))
	if err != nil {
		return LoginsView{}, err
	}
	// Cached for the same reason the security read is: this is a 24-hour
	// journal grep — 583 ms on the measured server — and two tabs ask for it.
	// The security tab reads it on every load and the session tab reads it
	// again, so going back and forth between them ran the same scan every time
	// and left a line in the Command Log for each.
	gen := a.connGeneration(hostID)
	if cached, ok := a.logins.get(hostID, gen, elevate); ok {
		return cached, nil
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return LoginsView{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	if info.Platform == adapter.PlatformWindows {
		view, err := a.windowsLogins(ctx, conn)
		if err == nil {
			a.logins.put(hostID, gen, elevate, view)
		}
		return view, err
	}

	// One script, the way the metrics snapshot is one script (§3.2b's stated
	// exception). Nothing is interpolated into it.
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate, "sh", "-c", adapter.LoginsScript)
	if err != nil {
		return LoginsView{}, err
	}

	view := LoginsView{Logins: []adapter.Login{}, Window: authWindow}
	last, auth := splitLoginsOutput(string(res.Stdout))
	view.Logins = adapter.ParseLast(last, a.serverLocation(hostID))

	switch {
	case !info.HasSystemd:
		view.Access = EventAccessNoJournal
	case !info.CanReadJournal && !elevate:
		// Asked before reading, not inferred from an empty answer — those two
		// need opposite things said about them, and here the wrong one reads as
		// "nobody is trying to get in".
		if info.HasSudo {
			view.Access = EventAccessNeedsSudo
		} else {
			view.Access = EventAccessDenied
		}
	default:
		view.Access = EventAccessOK
		view.Auth = adapter.ParseAuthAggregate(auth)
	}
	return view, nil
}

// splitLoginsOutput cuts the script's two sections apart.
//
// Markers rather than line counts: `last` prints a variable number of rows and
// a trailing "wtmp begins" line, so anything positional would drift the moment
// a server had one login more than the one it was written against.
func splitLoginsOutput(out string) (last, auth string) {
	_, rest, found := strings.Cut(out, "#last\n")
	if !found {
		return "", ""
	}
	last, auth, found = strings.Cut(rest, "#auth\n")
	if !found {
		return rest, ""
	}
	return last, auth
}

// windowsLogins reads both halves out of the OpenSSH event log.
//
// There is no elevate branch and no access notice: the log is readable by the
// account that logged in over it, so the failures either come back or the log
// is not there at all. The POSIX path needs those because the journal is
// privileged and wtmp is not — one source, two permissions. Here it is one
// source with one permission.
func (a *App) windowsLogins(ctx context.Context, conn *sshcore.Conn) (LoginsView, error) {
	raw, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll, adapter.WindowsLoginsScript())
	if err != nil {
		return LoginsView{}, err
	}
	got := adapter.ParseWindowsLogins(string(raw))
	view := LoginsView{Logins: got.Logins, Auth: got.Auth, Window: authWindow, Access: EventAccessOK}
	if !got.HasLog {
		view.Access = EventAccessNoSSHLog
		return view, nil
	}
	if !got.Since.IsZero() && time.Since(got.Since) < 24*time.Hour {
		since := got.Since
		view.Since = &since
	}
	return view, nil
}

// loginsCache holds one login-history read per connection.
//
// Keyed on the elevation as well, because the elevated read answers a different
// question — an unelevated one comes back with Access=needs-sudo and no
// failures at all, and serving that to somebody who just pressed "read as
// administrator" would look like the button did nothing.
//
// No TTL. A tab switch must not re-run a 24-hour journal grep, and the refresh
// that does exist is the connection changing. The security tab's own cache uses
// a TTL because its numbers include a delta; these are a window on the past.
type loginsCache struct {
	mu   sync.Mutex
	byID map[string]loginsEntry
}

type loginsEntry struct {
	gen      uint64
	elevated bool
	view     LoginsView
}

func newLoginsCache() *loginsCache { return &loginsCache{byID: map[string]loginsEntry{}} }

func (c *loginsCache) get(id string, gen uint64, elevated bool) (LoginsView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok || e.gen != gen || e.elevated != elevated {
		return LoginsView{}, false
	}
	return e.view, true
}

func (c *loginsCache) put(id string, gen uint64, elevated bool, view LoginsView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = loginsEntry{gen: gen, elevated: elevated, view: view}
}
