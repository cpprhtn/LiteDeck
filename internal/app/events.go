package app

import (
	"context"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// The event timeline (§4.7) — see arch/07.
//
// One read when the tab opens or the range changes. Not a poller: the past does
// not change, and a timeline that refreshes every two seconds would be a log
// viewer with extra steps.

// EventAccess says why the list may be empty, which matters more here than
// almost anywhere else in the app.
//
// A user outside systemd-journal/adm gets an empty journal with **no error** —
// journalctl answers successfully and says nothing. Rendered plainly that reads
// as "nothing has gone wrong on this server", which is the most expensive lie
// this feature could tell. So emptiness always arrives labelled.
type EventAccess string

const (
	// EventAccessOK means the list is the truth: empty here means quiet.
	EventAccessOK EventAccess = "ok"
	// EventAccessNeedsSudo means this user cannot read the system journal but
	// sudo is available, so the view can offer to retry elevated.
	EventAccessNeedsSudo EventAccess = "needs-sudo"
	// EventAccessDenied means neither group membership nor sudo. Nothing the
	// app can do; the view says which group to be added to.
	EventAccessDenied EventAccess = "denied"
	// EventAccessNoJournal means no systemd. The tab is not offered.
	EventAccessNoJournal EventAccess = "no-journal"
)

// EventsView is what the timeline renders.
type EventsView struct {
	Events []adapter.Event `json:"events"`
	Access EventAccess     `json:"access"`
	// Range echoes what was actually read, so the view cannot label a day of
	// events as a week's.
	Range adapter.EventRange `json:"range"`
	// Truncated reports that the read hit its line cap, so the window shown is
	// narrower than the one asked for. Without this the oldest visible event
	// looks like the oldest event.
	Truncated bool `json:"truncated"`
}

// HostEvents reads the timeline (§4.7).
//
// elevate is the user answering the "retry as administrator" the view offers
// after EventAccessNeedsSudo. It is never chosen automatically — §7.2.
func (a *App) HostEvents(hostID string, rng adapter.EventRange, elevate bool) (EventsView, error) {
	info, err := a.requireCapability(hostID, adapter.CapEvents, i18n.S("사건 기록"))
	if err != nil {
		return EventsView{}, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return EventsView{}, err
	}

	view := EventsView{Events: []adapter.Event{}, Range: rng}

	if !info.HasSystemd {
		view.Access = EventAccessNoJournal
		return view, nil
	}
	// Ask before reading rather than after. Reading first and inferring from an
	// empty result cannot tell a quiet server from an unreadable one, and those
	// two need opposite things said about them.
	if !info.CanReadJournal && !elevate {
		if info.HasSudo {
			view.Access = EventAccessNeedsSudo
		} else {
			view.Access = EventAccessDenied
		}
		return view, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	args := adapter.JournalArgs(rng.Since(), journalPriority, 0)
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate, "journalctl", args...)
	if err != nil {
		return EventsView{}, err
	}
	if !res.OK() && len(res.Stdout) == 0 {
		return EventsView{}, res.Err()
	}

	view.Events = adapter.ParseJournal(res.Stdout)
	view.Access = EventAccessOK
	view.Truncated = len(view.Events) >= adapter.JournalMaxLines
	return view, nil
}

// journalPriority is the severity floor: warning and worse.
//
// Notice and info are where a healthy server does almost all of its talking,
// and including them turns a timeline into the log viewer this deliberately is
// not. Boot and shutdown are the exception — they are informational and they
// are the boundaries the timeline draws lines across — so they arrive by way of
// their own priority, which systemd already sets above this floor.
const journalPriority = 4
