package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// What was typed in LiteDeck's own terminal (T-22, 명령 이력 B).
//
// The second source behind the command history. sudo's journal (C-2) is a
// record but only of privileged commands; this one covers everything run
// through this app's terminal, and it is written here rather than on the
// server — nothing is installed, nothing is appended to anybody's history file.
//
// # Why the working directory is not simply tracked
//
// The obvious design is to watch for `cd` and keep a running path. That works
// only while every line can be read, and lines stop being readable the moment
// the shell interprets a keystroke for itself: an arrow key recalling history,
// Tab completing a name, Ctrl-R searching. A recalled `cd` is one of the most
// ordinary things a person does at a prompt, and it arrives here as "something
// happened that I could not read".
//
// So the path carries its own confidence. A blind line does not move the path;
// it marks it uncertain, and everything recorded after that says so on screen
// until a readable `cd` re-anchors it. The alternative — carrying on as if the
// path were still known — produces a history that is confidently wrong, which
// is the one outcome worth more work to avoid.

// TypedCommand is one line entered in the app's terminal.
type TypedCommand struct {
	HostID string    `json:"hostId"`
	At     time.Time `json:"at"`
	// Command is the line as typed. Empty is not possible here: an unreadable
	// line is not recorded at all, it only spoils the path.
	Command string `json:"command"`
	// PWD is where it was run, as far as this can be known.
	PWD string `json:"pwd,omitempty"`
	// PWDCertain is false once an unreadable line has gone by without a `cd`
	// to re-anchor on. The screen shows those paths as estimates, the same way
	// the shell-history source will have to.
	PWDCertain bool `json:"pwdCertain"`
	// Effect reuses the sudo history's classification, so both sources sort
	// along the same axis: what did this do to the server.
	Effect adapter.SudoEffect `json:"effect"`
}

// typedLog keeps the app's own terminal history, per host.
//
// Bounded and local. It is a convenience, not an audit trail — the Command Log
// is the audit trail — so it is capped rather than rotated, and losing it costs
// nothing that cannot be typed again.
type typedLog struct {
	mu   sync.Mutex
	path string
	byID map[string][]TypedCommand
	// cwd tracks where each terminal session is standing, and whether that is
	// known. Keyed by terminal id, because two terminals on one host are in
	// two different directories.
	//
	// The tracker is the history replay's own walker. The terminal used to
	// carry a second, smaller cd parser; see adapter.CdTracker for what the two
	// disagreed about.
	cwd map[string]*adapter.CdTracker
	// home per host, so a `cd ~` here lands on the same spelling the shell
	// history's replay produces. Two spellings of one directory grow two
	// branches in the tree, which is the panel disagreeing with itself.
	home map[string]string
}

// typedLogMax bounds one host's history.
//
// Five hundred lines is more than anybody scrolls and small enough to write
// whole on every change. The file is rewritten rather than appended so a crash
// leaves the previous version rather than half a line.
const typedLogMax = 500

func newTypedLog(dir string) *typedLog {
	l := &typedLog{
		path: filepath.Join(dir, "typed.json"),
		byID: map[string][]TypedCommand{},
		cwd:  map[string]*adapter.CdTracker{},
		home: map[string]string{},
	}
	l.load()
	return l
}

func (l *typedLog) load() {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var stored map[string][]TypedCommand
	if json.Unmarshal(b, &stored) != nil {
		// A file that will not parse is discarded rather than repaired. It is a
		// convenience log; refusing to start over it would be the wrong trade.
		return
	}
	l.byID = stored
}

func (l *typedLog) save() {
	b, err := json.Marshal(l.byID)
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, l.path)
}

// enter records a line, or the fact that one could not be read.
//
// Returns the command it stored, if any, so the caller can tell whether the
// line became history or only cost the path its confidence.
func (l *typedLog) enter(hostID, termID, line string, blind bool) *TypedCommand {
	l.mu.Lock()
	defer l.mu.Unlock()
	home := l.home[hostID]

	here := l.cwd[termID]
	if here == nil {
		// No anchor yet — a container shell, or a session opened before the
		// home was known. It follows moves from nothing, which an absolute
		// `cd` repairs.
		here = adapter.NewCdTracker("", home)
		l.cwd[termID] = here
	}
	if blind {
		// Something was run that could not be read. It may well have been a
		// `cd`, so the path is no longer trustworthy — but it is not discarded
		// either: a stale path marked uncertain is more useful than none.
		here.Blind()
		return nil
	}
	here.Step(line)

	pwd, certain := here.Here()
	cmd := TypedCommand{
		HostID:     hostID,
		At:         time.Now().UTC(),
		Command:    line,
		PWD:        pwd,
		PWDCertain: certain,
		Effect:     adapter.ClassifyCommand(line),
	}
	rows := append(l.byID[hostID], cmd)
	if len(rows) > typedLogMax {
		rows = rows[len(rows)-typedLogMax:]
	}
	l.byID[hostID] = rows
	l.save()
	return &cmd
}

// setCwd anchors a terminal's directory from something authoritative — the
// shell itself, asked once when the session opens.
func (l *typedLog) setCwd(hostID, termID, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cwd[termID] = adapter.NewCdTracker(path, l.home[hostID])
}

// setHome records a host's home directory, asked once and reused.
func (l *typedLog) setHome(hostID, home string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.home[hostID] = home
}

func (l *typedLog) knowsHome(hostID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.home[hostID]
	return ok
}

func (l *typedLog) forgetTerm(termID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.cwd, termID)
}

func (l *typedLog) list(hostID string) []TypedCommand {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows := l.byID[hostID]
	out := make([]TypedCommand, len(rows))
	copy(out, rows)
	// Newest first, the way both other sources arrive.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// TypedEntered records a line the app's terminal saw the user enter.
//
// Called from the frontend, which is the only side that sees keystrokes. A line
// it could not reconstruct arrives with blind set and no text: that is not a
// gap to be filled in later, it is the honest answer, and it costs the tracked
// directory its confidence rather than being guessed at.
func (a *App) TypedEntered(hostID, termID, line string, blind bool) {
	if a.typed == nil {
		return
	}
	a.learnHome(hostID)
	if !blind && strings.TrimSpace(line) == "" {
		// Enter on an empty prompt. Not a command, and not a reason to doubt
		// where the shell is standing.
		return
	}
	a.typed.enter(hostID, termID, strings.TrimSpace(line), blind)
}

// learnHome resolves a host's home once and remembers it.
//
// Needed before a tracker is made, not after: bare `cd` and `~` are the two
// operands that cannot be followed without it, and a tracker built with an
// empty home stays that way for the life of the session.
func (a *App) learnHome(hostID string) {
	if a.typed == nil || a.typed.knowsHome(hostID) {
		return
	}
	// Getwd on an open SFTP session is local — the value was resolved at the
	// handshake — but opening that session may not be, which is why this is
	// asked once and kept.
	home, err := a.HomeDir(hostID)
	if err != nil {
		home = ""
	}
	a.typed.setHome(hostID, home)
}

// TypedHistory is what was typed in this app's terminal on a host, newest first.
func (a *App) TypedHistory(hostID string) []TypedCommand {
	if a.typed == nil {
		return []TypedCommand{}
	}
	return a.typed.list(hostID)
}

// TerminalCwd is where a terminal session is standing, as far as this can tell.
//
// The history panel asks so it can open at the directory the user is actually
// in — "what did I run here" is the second half of the question this feature
// exists for, and answering it needs to know where "here" is.
//
// Empty when nothing has anchored the session yet. Certain is false once an
// unreadable line has gone by; the panel still uses the path, it just does not
// claim it.
func (a *App) TerminalCwd(termID string) (string, bool) {
	if a.typed == nil {
		return "", false
	}
	a.typed.mu.Lock()
	defer a.typed.mu.Unlock()
	c := a.typed.cwd[termID]
	if c == nil {
		return "", false
	}
	return c.Here()
}
