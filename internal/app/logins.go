package app

import (
	"context"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
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
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return LoginsView{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	// One script, the way the metrics snapshot is one script (§3.2b's stated
	// exception). Nothing is interpolated into it.
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate, "sh", "-c", adapter.LoginsScript)
	if err != nil {
		return LoginsView{}, err
	}

	view := LoginsView{Logins: []adapter.Login{}, Window: authWindow}
	last, auth := splitLoginsOutput(string(res.Stdout))
	view.Logins = adapter.ParseLast(last)

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
