package app

import (
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The Command Log (§4.6) — the panel that shows every command the GUI runs on
// the user's behalf, as it runs.
//
// It is the differentiator, and the reason is not convenience. A GUI that talks
// to your production server is asking for trust; showing exactly what it just
// executed is how it earns that, and it teaches the CLI along the way. Being
// able to copy a line out is the point, not a bonus.
//
// The log is local only. Nothing here is ever transmitted (§7.4).

// CommandEntry is one executed command.
type CommandEntry struct {
	Seq      int    `json:"seq"`
	HostID   string `json:"hostId"`
	Line     string `json:"line"`
	At       string `json:"at"`     // RFC3339, for display
	Status   string `json:"status"` // running, ok, probe, failed, error
	ExitCode int    `json:"exitCode"`
	Duration int64  `json:"durationMs"`
	// Kind is "", "poll" or "probe" — see sshcore.CommandKind.
	Kind string `json:"kind,omitempty"`
	// Repeat counts how many times this background read has run. A view that
	// refreshes every two seconds produces the same line forever, and a
	// hundred identical rows say nothing a count does not say better.
	Repeat int    `json:"repeat,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	// QueuedMs is time spent waiting for one of the Exec channels. Shown only
	// when it is long enough to matter, because a command that sat in a queue
	// and a command that was genuinely slow look identical without it.
	QueuedMs int64 `json:"queuedMs,omitempty"`
	// Origin is "ai" for a tool call an MCP client made, "tunnel" for a port
	// forward, and empty for a command the user's click ran directly. The panel
	// marks the non-empty ones, so a line nobody remembers asking for is
	// attributable at a glance — and so a row that shows a command LiteDeck did
	// not literally execute never passes for one it did (§4.6).
	Origin string `json:"origin,omitempty"`
}

// commandLogLimit bounds the in-memory history. The panel is a live view, not
// an audit trail; a long session should not grow without limit.
const commandLogLimit = 500

// queueNoteFloor is how long a wait has to be before it is worth reporting.
// Every command queues for a moment; only a wait long enough to explain a slow
// response is information.
const queueNoteFloor = 250 * time.Millisecond

type commandLog struct {
	app *App

	mu      sync.Mutex
	seq     int
	entries []CommandEntry
	running map[string]int // line+host → seq, to match a finish to its start
	// folded is host+kind+outcome+line → seq of the row that absorbs repeats
	// of one background read. The outcome is part of the key so that a poll
	// which starts failing gets a row of its own rather than changing the
	// colour of the row that had been counting its successes.
	folded map[string]int
}

func newCommandLog(a *App) *commandLog {
	return &commandLog{
		app:     a,
		running: make(map[string]int),
		folded:  make(map[string]int),
	}
}

func (l *commandLog) CommandStarted(info sshcore.CommandInfo) {
	// A background read gets no row while it runs. It finishes in under a
	// second, nobody watches it happen, and one row per run is what filled this
	// panel with hundreds of identical lines — the panel holds 500 entries, and
	// at two pollers on a two second cadence the one restart the user actually
	// performed was evicted within about eight minutes. They fold into a single
	// counted row on completion instead.
	if info.Background() {
		return
	}

	l.mu.Lock()
	l.seq++
	e := CommandEntry{
		Seq:    l.seq,
		HostID: info.HostID,
		Line:   info.Line,
		At:     time.Now().Format(time.RFC3339),
		Status: "running",
		Kind:   string(info.Kind),
	}
	l.entries = append(l.entries, e)
	l.trim()
	l.running[info.HostID+"\x00"+info.Line] = e.Seq
	l.mu.Unlock()

	l.emit("cmd:started", e)
}

func (l *commandLog) CommandFinished(info sshcore.CommandInfo, res *sshcore.Result, err error) {
	status, exit, stderr := classify(info, res, err)

	l.mu.Lock()
	var updated CommandEntry
	if info.Background() {
		updated = l.fold(info, status, exit, stderr, res)
	} else {
		key := info.HostID + "\x00" + info.Line
		seq, ok := l.running[key]
		delete(l.running, key)
		if i := l.indexOf(seq); ok && i >= 0 {
			e := &l.entries[i]
			e.Status, e.ExitCode, e.Stderr = status, exit, stderr
			if res != nil {
				e.Duration = res.Duration.Milliseconds()
				if res.Queued > queueNoteFloor {
					e.QueuedMs = res.Queued.Milliseconds()
				}
			}
			updated = *e
		}
	}
	l.mu.Unlock()

	if updated.Seq != 0 {
		l.emit("cmd:finished", updated)
	}
}

// classify turns an outcome into the row's status.
func classify(info sshcore.CommandInfo, res *sshcore.Result, err error) (status string, exit int, stderr string) {
	switch {
	case err != nil:
		return "error", 0, err.Error()
	case res != nil && !res.OK() && info.Probe():
		// A probe answering "no" is information, not a fault. Counting it as a
		// failure would put two red rows in the log on every single connection,
		// and a failure count that is always wrong is one the user stops
		// reading.
		return "probe", res.ExitCode, ""
	case res != nil && !res.OK():
		return "failed", res.ExitCode, truncate(string(res.Stderr), 4000)
	}
	return "ok", 0, ""
}

// fold merges one background read into the single row that counts it.
//
// The outcome is part of the key, so a poll that starts failing gets a row of
// its own instead of recolouring the row that had been counting its successes.
// A failure hidden behind a repeat count is a failure nobody sees, and the
// count would stop meaning what it says.
func (l *commandLog) fold(
	info sshcore.CommandInfo, status string, exit int, stderr string, res *sshcore.Result,
) CommandEntry {
	outcome := "ok"
	if status == "failed" || status == "error" {
		outcome = "bad"
	}
	key := info.HostID + "\x00" + string(info.Kind) + "\x00" + outcome + "\x00" + info.Line

	i := -1
	if seq, ok := l.folded[key]; ok {
		if i = l.indexOf(seq); i < 0 {
			// Trimmed out from under us. Start counting again rather than
			// leave the map pointing at a row that no longer exists.
			delete(l.folded, key)
		}
	}
	if i < 0 {
		l.seq++
		l.entries = append(l.entries, CommandEntry{
			Seq:    l.seq,
			HostID: info.HostID,
			Line:   info.Line,
			Kind:   string(info.Kind),
		})
		l.trim()
		i = len(l.entries) - 1
		l.folded[key] = l.entries[i].Seq
	}

	e := &l.entries[i]
	e.At = time.Now().Format(time.RFC3339)
	e.Status, e.ExitCode, e.Stderr = status, exit, stderr
	e.Repeat++
	e.QueuedMs = 0
	if res != nil {
		e.Duration = res.Duration.Milliseconds()
		if res.Queued > queueNoteFloor {
			e.QueuedMs = res.Queued.Milliseconds()
		}
	}
	return *e
}

// indexOf finds an entry by sequence number, or -1 once it has been trimmed.
func (l *commandLog) indexOf(seq int) int {
	for i := range l.entries {
		if l.entries[i].Seq == seq {
			return i
		}
	}
	return -1
}

func (l *commandLog) trim() {
	if len(l.entries) > commandLogLimit {
		l.entries = l.entries[len(l.entries)-commandLogLimit:]
	}
}

func (l *commandLog) emit(event string, e CommandEntry) {
	l.app.emit(event, e)
}

// CommandLog returns the recent history, oldest first.
func (a *App) CommandLog() []CommandEntry {
	a.log.mu.Lock()
	defer a.log.mu.Unlock()
	out := make([]CommandEntry, len(a.log.entries))
	copy(out, a.log.entries)
	return out
}

// ClearCommandLog empties the panel.
func (a *App) ClearCommandLog() {
	a.log.mu.Lock()
	a.log.entries = nil
	// The fold map points at rows that no longer exist; leaving it would make
	// the next background read update nothing.
	a.log.folded = make(map[string]int)
	a.log.mu.Unlock()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// forwardOpened records a port forward, and returns the row to complete later.
//
// A forward is not a command — no shell runs, and the bytes ride a channel on
// the session that is already open. It belongs in this panel anyway: it is the
// app reaching a service on the user's behalf, which is the entire question
// §4.6 exists to answer. The line is the `ssh -L` somebody would have typed to
// do the same thing by hand, so it is worth copying, and the "tunnel" origin is
// what stops it from reading as a command that ran.
func (l *commandLog) forwardOpened(hostID, line string) int {
	l.mu.Lock()
	l.seq++
	e := CommandEntry{
		Seq:    l.seq,
		HostID: hostID,
		Line:   line,
		At:     time.Now().Format(time.RFC3339),
		Status: "running", // the panel says "실행 중", which is true while it carries traffic
		Origin: "tunnel",
	}
	l.entries = append(l.entries, e)
	l.trim()
	l.mu.Unlock()

	l.emit("cmd:started", e)
	return e.Seq
}

// forwardClosed completes a forward's row with how long it stayed open.
//
// reason is empty when the user closed it and carries the cause otherwise —
// "the connection dropped" is the difference between a tunnel that ended and
// one that was taken away, and only one of those is worth investigating.
func (l *commandLog) forwardClosed(seq int, open time.Duration, reason string) {
	l.mu.Lock()
	var updated CommandEntry
	if i := l.indexOf(seq); i >= 0 {
		e := &l.entries[i]
		e.Status = "ok"
		e.Duration = open.Milliseconds()
		if reason != "" {
			e.Status, e.Stderr = "failed", reason
		}
		updated = *e
	}
	l.mu.Unlock()

	if updated.Seq != 0 {
		l.emit("cmd:finished", updated)
	}
}

// probed records a read that crossed the connection without being a command —
// an HTTP request over a forwarded channel.
//
// Folded and classified like a background probe, because it is one: asking a
// port whether it is Uptime Kuma is a capability check, and "nothing there" is
// an answer rather than a fault. Counting those as failures would put a
// permanent red number on the panel and train the user to stop reading it.
func (l *commandLog) probed(hostID, line string, answered bool, detail string, d time.Duration) {
	status := "probe"
	if answered {
		status = "ok"
	}
	info := sshcore.CommandInfo{HostID: hostID, Line: line, Kind: sshcore.CommandProbe}

	l.mu.Lock()
	e := l.fold(info, status, 0, truncate(detail, 4000), &sshcore.Result{Duration: d})
	l.mu.Unlock()

	l.emit("cmd:finished", e)
}

// AICall records a tool call an MCP client made.
//
// The entry is the AI's request, not the SSH commands it produces — those
// arrive on their own lines a moment later, indistinguishable from a click,
// which is correct: the server ran them the same way. What the user needs to
// see is that something other than their own hands asked.
func (l *commandLog) AICall(hostID, line string) {
	l.mu.Lock()
	l.seq++
	e := CommandEntry{
		Seq:    l.seq,
		HostID: hostID,
		Line:   line,
		At:     time.Now().Format(time.RFC3339),
		Status: "ok",
		Origin: "ai",
	}
	l.entries = append(l.entries, e)
	l.trim()
	l.mu.Unlock()

	l.emit("cmd:started", e)
}

// AIWrite records a change an MCP client asked for, and what became of it.
//
// Logged whatever the outcome, including declined and timed out. "The AI tried
// to restart nginx and I said no" is exactly as much a part of the record as
// the ones that ran — more so, if the reason it asked was an instruction
// somebody hid in a log file.
func (l *commandLog) AIWrite(hostID, summary, outcome string) {
	status := "ok"
	if outcome != "approved" && outcome != "auto-approved" {
		status = "failed"
	}
	l.mu.Lock()
	l.seq++
	e := CommandEntry{
		Seq:    l.seq,
		HostID: hostID,
		Line:   summary + "  [" + outcome + "]",
		At:     time.Now().Format(time.RFC3339),
		Status: status,
		Origin: "ai",
	}
	l.entries = append(l.entries, e)
	l.trim()
	l.mu.Unlock()

	l.emit("cmd:started", e)
}
