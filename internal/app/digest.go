package app

import (
	"context"
	"sync"
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

// digestCache holds one answer per host.
//
// The digest is the most expensive single thing this app runs. Measured through
// the Command Log on a real server: **2.7 to 4.1 seconds a call, four calls in
// one session.** It reads up to a week of journal, and on a box facing the
// internet that is tens of thousands of lines — the one this was measured on
// takes over three thousand failed logins a day. The strip that shows it
// re-mounts whenever the active host changes, so switching away and back paid
// the whole cost again for an answer that had not moved.
//
// There is no TTL, and that is deliberate. The question is "what happened since
// *you last looked*", so the answer only changes when the mark moves — which is
// exactly what `since` in the key catches. A timer here would put the four
// seconds back on a schedule, which is the thing being fixed.
type digestCache struct {
	mu   sync.Mutex
	byID map[string]digestEntry
}

type digestEntry struct {
	// gen is the connection this was counted through. A reconnect can be a
	// rebooted machine, and its journal is not the one counted here.
	gen uint64
	// since is the mark it was counted from. Dismissing the strip moves the
	// mark, and a cache that ignored that would answer the old question
	// forever.
	since int64
	view  DigestView
}

func newDigestCache() *digestCache { return &digestCache{byID: map[string]digestEntry{}} }

func (c *digestCache) get(id string, gen uint64, since int64) (DigestView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok || e.gen != gen || e.since != since {
		return DigestView{}, false
	}
	return e.view, true
}

func (c *digestCache) put(id string, gen uint64, since int64, view DigestView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = digestEntry{gen: gen, since: since, view: view}
}

func (c *digestCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
}

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
	// Keyed on the mark as well as the connection, so dismissing the strip is
	// what reopens the question — see digestCache for why there is no timer.
	gen := a.mgr.Generation(hostID)
	if cached, ok := a.digests.get(hostID, gen, since); ok {
		return cached, nil
	}
	view := DigestView{Since: since, First: since == 0}

	if !info.HasSystemd || !info.CanReadJournal {
		// No journal, or none this user can read. There is nothing to say —
		// but "nothing to say" is not "first visit", and reporting it as one
		// would tell somebody who has been here for months that they are new.
		view.Quiet = true
		a.digests.put(hostID, gen, since, view)
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
	a.digests.put(hostID, gen, since, view)
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
