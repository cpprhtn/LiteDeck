package app

import (
	"context"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// "What happened since you last looked" (T-29).
//
// LiteDeck is opened when something is already wrong. Nothing in it gives
// somebody a reason to open it when nothing is, and a tool nobody opens is one
// nobody has running when they need it. This is the answer to a question they
// cannot get any other way: the box was running while they were not watching.
//
// The mark is local, per host, and belongs to the person rather than to the
// server (config.Settings.LastSeen). Two people watching the same machine last
// looked at different times.

// DigestView is what the strip renders.
type DigestView struct {
	adapter.Digest
	// Since is the mark this was counted from. Zero on a first visit.
	Since int64 `json:"since"`
	// First is true when there is no mark yet. The counts are then meaningless
	// — everything is "new" — so the strip says hello instead of reporting a
	// year of failures as if they happened while the user was away.
	First bool `json:"first"`
	// Quiet is true when nothing happened. Kept as a field rather than left for
	// the UI to work out, so both sides agree on what "nothing" means.
	Quiet bool `json:"quiet"`
	// Window is what was actually asked of journalctl, so the strip cannot say
	// "since Tuesday" over a day's worth of counting.
	Window string `json:"window"`
	// Readable is false where there is no journal, or none this user can read.
	// Then the counts are absent rather than zero, and the strip must not draw
	// the conclusion that nothing happened.
	Readable bool `json:"readable"`
}

// digestFloor bounds how far back a digest reaches.
//
// Somebody returning after three months does not want a three-month count, and
// the journal probably does not go back that far anyway — it would report the
// retention limit as if it were the age of the news. A week is long enough to
// cover a holiday and short enough to stay one cheap read.
const digestFloor = 7 * 24 * time.Hour

// HostDigest counts what happened since this person last looked at the host.
func (a *App) HostDigest(hostID string) (DigestView, error) {
	info, err := a.requireCapability(hostID, adapter.CapEvents, i18n.S("변경 요약"))
	if err != nil {
		return DigestView{}, err
	}
	var since int64
	if a.settings != nil {
		since = a.settings.Get().LastSeen[hostID]
	}
	view := DigestView{Since: since, First: since == 0}

	if !info.HasSystemd || !info.CanReadJournal {
		// No journal, or none this user can read. There is nothing to say —
		// but "nothing to say" is not "first visit", and reporting it as one
		// would tell somebody who has been here for months that they are new.
		view.Quiet = true
		return view, nil
	}

	from := time.Now().Add(-digestFloor)
	if !view.First {
		if seen := time.Unix(since, 0); seen.After(from) {
			from = seen
		}
	}
	// journalctl's own format, in the server's local time. A relative window
	// ("-3h") would drift by however long this call took to arrive.
	view.Window = from.Format("2006-01-02 15:04:05")

	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return DigestView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	// The window is an argument, not part of the script — see DigestScript.
	res, err := conn.Exec(ctx, "sh", "-c", adapter.DigestScript, "sh", view.Window)
	if err != nil {
		return DigestView{}, err
	}
	view.Digest = adapter.ParseDigest(string(res.Stdout))
	view.Readable = true
	view.Quiet = view.Digest.Quiet()
	return view, nil
}

// MarkHostSeen moves the mark to now.
//
// Called when the digest has been read, not when the host connects. Moving it
// on connect would consume the answer before anybody had a chance to see it,
// which is how this kind of feature quietly becomes a strip that always says
// nothing happened.
func (a *App) MarkHostSeen(hostID string) error {
	if a.settings == nil {
		return nil
	}
	return a.settings.SetLastSeen(hostID, time.Now().Unix())
}
