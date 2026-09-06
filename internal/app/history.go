package app

import (
	"context"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// Command history (arch/07, 명령 이력) — the privileged half of it.
//
// "I was on this server three months ago and I cannot remember what I did."
// The answer people reach for is their shell history, and the reason they have
// to reach for it is that the file says what was typed but not where. This
// reads sudo's journal instead, where the working directory is written down at
// the moment the command runs.
//
// The trade is coverage for certainty: only what went through sudo appears
// here, and that is a slice — but it is the slice that changed something, which
// is what the question is actually about. The shell files are a separate source
// with separate problems (arch/07 C-1) and they are not read yet.
//
// One read per open, like the event timeline. The past does not change.

// CommandHistoryView is what the history pane renders.
type CommandHistoryView struct {
	Runs []adapter.SudoRun `json:"runs"`
	// Access reuses the timeline's three-way answer. The journal is the same
	// journal, and an empty list means the same three different things.
	Access EventAccess        `json:"access"`
	Range  adapter.EventRange `json:"range"`
	// Truncated reports that the read hit its line cap, so the oldest row shown
	// is not the oldest row there is.
	Truncated bool `json:"truncated"`
	// Secrets counts rows whose command looked like it carried a credential.
	// A count rather than a list: it is worth saying out loud that this server's
	// history has passwords in it, and worth saying it without printing them.
	Secrets int `json:"secrets"`
}

// HostCommandHistory reads what was run through sudo on this host.
//
// Commands arrive **masked**. A shell history is the densest credential file on
// most servers and this is a slice of the same thing, so the raw text does not
// leave Go — in server mode it would otherwise cross to a browser, and one
// stolen login would be worth every password ever typed on the box. Revealing a
// row is a deliberate act and does not exist yet; when it does it should be its
// own call, not a field that ships by default.
//
// elevate is the user answering the "retry as administrator" the pane offers,
// exactly as the timeline does. Never chosen automatically (§7.2).
func (a *App) HostCommandHistory(hostID string, rng adapter.EventRange, elevate bool) (CommandHistoryView, error) {
	info, err := a.requireCapability(hostID, adapter.CapEvents, i18n.S("명령 이력"))
	if err != nil {
		return CommandHistoryView{}, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return CommandHistoryView{}, err
	}

	view := CommandHistoryView{Runs: []adapter.SudoRun{}, Range: rng}

	if !info.HasSystemd {
		view.Access = EventAccessNoJournal
		return view, nil
	}
	// Asked before reading, not inferred afterwards. sudo's records belong to
	// root, so a user outside systemd-journal/adm gets an empty list and no
	// error — which reads as "nothing was done here", the one thing this must
	// never say by accident.
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

	args := adapter.SudoHistoryArgs(rng.Since(), 0)
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate, "journalctl", args...)
	if err != nil {
		return CommandHistoryView{}, err
	}
	if !res.OK() && len(res.Stdout) == 0 {
		return CommandHistoryView{}, res.Err()
	}

	runs := adapter.ParseSudoHistory(res.Stdout)
	for i := range runs {
		masked, found := adapter.MaskSecrets(runs[i].Command)
		runs[i].Command = masked
		if found {
			view.Secrets++
		}
	}
	view.Runs = runs
	view.Access = EventAccessOK
	view.Truncated = len(runs) >= adapter.JournalMaxLines
	return view, nil
}
