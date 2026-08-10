package app

import (
	"fmt"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// Undoing what an AI changed.
//
// Restoring is a person's remedy, so it is a GUI binding and not an MCP tool.
// Handing the AI the ability to revert would let one confused turn undo the
// fix made in the previous one, and the list is meant to be the record a human
// checks against — not another surface for the thing being checked.

// AIChange is one recorded change, as the panel shows it.
type MCPChange struct {
	ID       string `json:"id"`
	HostID   string `json:"hostId"`
	Path     string `json:"path"`
	At       string `json:"at"`
	Action   string `json:"action"`
	Created  bool   `json:"created"`
	Bytes    int64  `json:"bytes"`
	Undoable bool   `json:"undoable"`
}

// MCPChanges lists what an AI changed on a host, newest first.
func (a *App) MCPChanges(hostID string) []MCPChange {
	if a.rollback == nil {
		return nil
	}
	entries := a.rollback.List(hostID)
	out := make([]MCPChange, 0, len(entries))
	for _, e := range entries {
		out = append(out, MCPChange{
			ID: e.ID, HostID: e.HostID, Path: e.Path,
			At: e.At.Format("2006-01-02 15:04:05"), Action: e.Action,
			Created: e.Created, Bytes: e.Bytes, Undoable: e.Undoable(),
		})
	}
	return out
}

// RestoreMCPChange puts a file back the way it was.
//
// The restore goes through the same atomic write the editor uses, so undoing a
// change cannot itself leave a half-written file. Undoing a *creation* deletes
// the file instead: there were no previous contents to put back.
func (a *App) RestoreMCPChange(id string) ActionResult {
	if a.rollback == nil {
		return failResult(fmt.Errorf("app: no history"))
	}
	entry, ok := a.rollback.Get(id)
	if !ok {
		return failResult(i18n.Errorf("되돌릴 기록을 찾을 수 없습니다: %s", id))
	}
	if !entry.Undoable() {
		return failResult(i18n.Errorf("%s 는 사본을 남기기에 너무 커서 되돌릴 수 없습니다", entry.Path))
	}

	if entry.Created {
		// It did not exist before, so putting it back means removing it.
		res := a.DeletePaths(entry.HostID, []string{entry.Path}, false, entry.Path)
		if !res.OK {
			return res
		}
	} else {
		body, err := a.rollback.Contents(id)
		if err != nil {
			return failResult(err)
		}
		res := a.WriteTextFile(entry.HostID, entry.Path, string(body))
		if !res.OK {
			return res
		}
	}

	// Dropped once it is dealt with, so the list shows outstanding work rather
	// than a pile of things already handled.
	if err := a.rollback.Forget(id); err != nil {
		a.emit("log:warning", err.Error())
	}
	a.log.AIWrite(entry.HostID, i18n.T("%s 되돌림", entry.Path), "restored")
	return okResult()
}

// recordAIChange saves what is about to be overwritten.
//
// A failure is a warning, never a refusal. Losing the ability to undo is bad;
// refusing to make the change the user just approved is worse, and would make
// the feature look broken for a reason nobody can see.
func (a *App) recordAIChange(hostID, path, action string, before []byte, created bool) {
	if a.rollback == nil {
		return
	}
	if _, err := a.rollback.Record(hostID, path, action, before, created); err != nil {
		a.emit("log:warning", i18n.T("되돌리기용 사본을 남기지 못했습니다 (%s): %v", path, err))
	}
}
