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
	Kind   string `json:"kind,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	// QueuedMs is time spent waiting for one of the Exec channels. Shown only
	// when it is long enough to matter, because a command that sat in a queue
	// and a command that was genuinely slow look identical without it.
	QueuedMs int64 `json:"queuedMs,omitempty"`
	// Origin is "ai" for a tool call an MCP client made, empty for the user's
	// own actions. The panel marks these so a command nobody remembers asking
	// for is attributable at a glance (§4.6).
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
	running map[string]int // line+host → index, to match a finish to its start
}

func newCommandLog(a *App) *commandLog {
	return &commandLog{app: a, running: make(map[string]int)}
}

func (l *commandLog) CommandStarted(info sshcore.CommandInfo) {
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
	if len(l.entries) > commandLogLimit {
		l.entries = l.entries[len(l.entries)-commandLogLimit:]
	}
	l.running[info.HostID+"\x00"+info.Line] = e.Seq
	l.mu.Unlock()

	l.emit("cmd:started", e)
}

func (l *commandLog) CommandFinished(info sshcore.CommandInfo, res *sshcore.Result, err error) {
	l.mu.Lock()
	key := info.HostID + "\x00" + info.Line
	seq, ok := l.running[key]
	delete(l.running, key)

	var updated CommandEntry
	for i := range l.entries {
		if !ok || l.entries[i].Seq != seq {
			continue
		}
		e := &l.entries[i]
		switch {
		case err != nil:
			e.Status = "error"
			e.Stderr = err.Error()
		case res != nil && !res.OK() && info.Probe():
			// A probe answering "no" is information, not a fault. Counting it
			// as a failure would put two red rows in the log on every single
			// connection, and a failure count that is always wrong is one the
			// user stops reading.
			e.Status = "probe"
			e.ExitCode = res.ExitCode
		case res != nil && !res.OK():
			e.Status = "failed"
			e.ExitCode = res.ExitCode
			e.Stderr = truncate(string(res.Stderr), 4000)
		default:
			e.Status = "ok"
		}
		if res != nil {
			e.Duration = res.Duration.Milliseconds()
			if res.Queued > queueNoteFloor {
				e.QueuedMs = res.Queued.Milliseconds()
			}
		}
		updated = *e
		break
	}
	l.mu.Unlock()

	if updated.Seq != 0 {
		l.emit("cmd:finished", updated)
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
	a.log.mu.Unlock()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
	if len(l.entries) > commandLogLimit {
		l.entries = l.entries[len(l.entries)-commandLogLimit:]
	}
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
	if len(l.entries) > commandLogLimit {
		l.entries = l.entries[len(l.entries)-commandLogLimit:]
	}
	l.mu.Unlock()

	l.emit("cmd:started", e)
}
