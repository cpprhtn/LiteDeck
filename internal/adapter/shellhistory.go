package adapter

import (
	"path"
	"strconv"
	"strings"
	"time"
)

// The shell's own history file (T-23, 명령 이력 C-1).
//
// This is the source people actually reach for. Somebody who has been away
// scrolls their history looking for the lines that changed something, and the
// reason they have to scroll is that the file says what was typed and not where.
// Replaying `cd` gives it back — as an estimate, which is the whole difficulty.
//
// # What the file really looks like, measured
//
// A real Ubuntu 24.04 box, bash 5.2, 2000 lines:
//
//   - **No timestamps.** HISTTIMEFORMAT is not set by default, so there are no
//     `#<epoch>` lines at all. The history has an order and no times. Anything
//     that prints "3 months ago" over this made it up.
//   - **A multi-line command is stored as several lines.** `sh -c '` opens a
//     quote and the following lines close it. Read one line at a time, one
//     command becomes five, four of which are fragments.
//   - **Pasted output ends up in it.** Log lines somebody pasted at a prompt sit
//     there looking exactly like commands, because as far as the file is
//     concerned that is what they were.
//
// # How good the directory actually is
//
// Better than it first looks. Counted over the measured file: 274 relative
// moves, 26 absolute, 21 bare `cd`, and **zero** of the forms that cannot be
// followed — no `cd $VAR`, no `cd -`, no quoting, no pushd. The replay resolved
// every one of them. So the path is reported per line, and only a `cd` the
// replay actually failed on marks the lines after it.
//
// The one thing no parser can see from here is session interleaving: bash
// appends a session's lines when the shell *exits*, so a file is ordered by
// when sessions ended, and two overlapping sessions leave a seam with no marker
// in it. That is a caveat about the source, said once, and not a reason to
// label every line a guess — doing that threw away everything the replay did
// know.

// ShellCommand is one line of history, with where it probably ran.
type ShellCommand struct {
	Command string `json:"command"`
	// At is nil where the file carries no times, which is the common case for
	// bash.
	//
	// A pointer rather than a zero time.Time, and that is not style. `omitempty`
	// does nothing for a struct, so a zero time.Time goes out as
	// "0001-01-01T00:00:00Z" — a date, which the screen dutifully rendered as
	// "24662 months ago". Absent has to be absent on the wire, not merely
	// meaningless.
	At *time.Time `json:"at,omitempty"`
	// PWD is the directory `cd` replay arrived at.
	PWD string `json:"pwd,omitempty"`
	// PWDCertain reports that the replay never lost track between the last
	// anchor and this line. False after a `cd` it could not resolve, until an
	// absolute one puts it back on solid ground.
	//
	// It is not a promise about the file, only about the replay — see the note
	// on session interleaving above, which no parser can see from here. The
	// screen says that once, about the source, rather than dimming every row:
	// marking all 321 of the measured file's `cd`s as guesses when the replay
	// resolved every one of them threw away what it did know.
	PWDCertain bool `json:"pwdCertain"`
	// Effect sorts it the same way the other two sources are sorted.
	Effect SudoEffect `json:"effect"`
}

// maxContinuation bounds how far an unterminated quote may swallow.
//
// A line ending in an odd number of quotes is usually a multi-line command, and
// occasionally it is an apostrophe in a word somebody typed. Without a bound the
// second case eats the rest of the file.
const maxContinuation = 20

// ParseBashHistory reads ~/.bash_history.
//
// `#<epoch>` lines are timestamps for the command that follows, present only
// when HISTTIMEFORMAT was set. Their absence is the normal case and is not an
// error: the entries simply carry no time.
func ParseBashHistory(text string) []ShellCommand {
	out := []ShellCommand{}
	lines := strings.Split(text, "\n")
	var pending *time.Time

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if t, ok := bashStamp(line); ok {
			pending = &t
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Join a command that was written across several lines.
		joined := line
		for n := 0; n < maxContinuation && !quotesBalanced(joined) && i+1 < len(lines); n++ {
			i++
			joined += "\n" + lines[i]
		}
		out = append(out, ShellCommand{
			Command: strings.TrimSpace(joined),
			At:      pending,
			Effect:  ClassifyCommand(joined),
		})
		pending = nil
	}
	return out
}

// ParseZshHistory reads ~/.zsh_history.
//
// With EXTENDED_HISTORY — which oh-my-zsh turns on — each line is
// `: <epoch>:<elapsed>;<command>`. Without it the file is plain lines, so both
// shapes are accepted rather than one being assumed.
func ParseZshHistory(text string) []ShellCommand {
	out := []ShellCommand{}
	lines := strings.Split(text, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		var at *time.Time
		cmd := line
		if rest, ok := strings.CutPrefix(line, ": "); ok {
			if stamp, body, found := strings.Cut(rest, ";"); found {
				if secs, _, ok := strings.Cut(stamp, ":"); ok {
					if n, err := strconv.ParseInt(secs, 10, 64); err == nil && n > 0 {
						t := time.Unix(n, 0).UTC()
						at = &t
						cmd = body
					}
				}
			}
		}
		// zsh escapes a newline inside a command with a trailing backslash.
		for n := 0; n < maxContinuation && strings.HasSuffix(cmd, "\\") && i+1 < len(lines); n++ {
			i++
			cmd = strings.TrimSuffix(cmd, "\\") + "\n" + lines[i]
		}
		out = append(out, ShellCommand{
			Command: strings.TrimSpace(cmd),
			At:      at,
			Effect:  ClassifyCommand(cmd),
		})
	}
	return out
}

// ReplayCd walks the history forward, keeping track of where each command ran.
//
// `home` is where it starts, and it matters more than it looks: a real history
// begins with relative moves — the first line of the measured file was
// `cd monitoring/` — because a login shell starts in the user's home and nobody
// types the absolute path they are already standing in. Starting from nothing
// left almost every command without a directory.
//
// Pass "" when home is unknown; the path then stays empty until an absolute
// `cd` gives the replay something to stand on.
func ReplayCd(cmds []ShellCommand, home string) []ShellCommand {
	return ReplayCdChecked(cmds, home, nil)
}

// ReplayCdChecked is ReplayCd with the server available to settle one question
// the file cannot answer: where a session began.
//
// # Why this needs asking at all
//
// A history file is several shells concatenated. bash appends a session's lines
// when that shell *exits*, so the file is ordered by when sessions ended, and
// every one of them started at home. Nothing in the file marks the seam.
//
// Walked as one continuous shell, the second session's `cd project` lands
// inside the first session's last directory. Somebody who opens a terminal,
// runs `cd project`, and closes it four times leaves a file that replays to
// project/project/project/project — a path that exists on no server, drawn as
// four levels of nesting that never happened.
//
// exists reports whether a directory is really there, and may be nil, in which
// case the walk is the old unchecked one. When a relative move lands somewhere
// that is not there, the same move is tried from home: a shell that starts at
// home and runs `cd project` is the ordinary explanation, and it is checked
// rather than assumed. If neither is there the path stays put and stops
// claiming to be right — the file said something this cannot follow.
func ReplayCdChecked(cmds []ShellCommand, home string, exists func(string) bool) []ShellCommand {
	w := cdWalker{here: home, certain: home != "", home: home, exists: exists}
	out := make([]ShellCommand, len(cmds))
	for i, c := range cmds {
		w.step(c.Command)
		c.PWD = w.here
		c.PWDCertain = w.certain && w.here != ""
		out[i] = c
	}
	return out
}

// CdTracker follows the working directory for a shell that is still running.
//
// The same walker the history replay uses, exposed for the app's own terminal.
// That terminal used to carry a second, smaller cd parser, and the two
// disagreed in both directions: it spelled `~` differently, so one directory
// grew two branches in the tree; and it silently ignored `cd -`, `cd $VAR` and
// `pushd`, leaving a stale path still marked certain. One implementation cannot
// disagree with itself.
type CdTracker struct{ w cdWalker }

// NewCdTracker starts a tracker at here, with home for bare `cd` and `~`.
func NewCdTracker(here, home string) *CdTracker {
	return &CdTracker{w: cdWalker{here: here, home: home, certain: here != ""}}
}

// Step feeds one entered line.
func (t *CdTracker) Step(line string) { t.w.step(line) }

// Blind records that a line went by unread. It may well have been a `cd`, so
// the path stops claiming to be right — but it is kept, because a stale path
// marked uncertain is more useful than none.
func (t *CdTracker) Blind() { t.w.certain = false }

// Here is the tracked directory and whether it can be trusted.
func (t *CdTracker) Here() (string, bool) {
	return t.w.here, t.w.certain && t.w.here != ""
}

// cdWalker follows the working directory through a run of commands.
type cdWalker struct {
	here    string
	prev    string   // for `cd -`
	stack   []string // for pushd/popd
	home    string
	certain bool
	// exists asks the server whether a directory is really there. nil means
	// nobody asked, and the walk trusts its own arithmetic.
	exists func(string) bool
}

func (w *cdWalker) step(line string) {
	// Only the first segment can move this shell. `cd x && make` does; the `cd`
	// inside `(cd x && make)` does not, and neither does one after a `|`.
	head := line
	for _, sep := range []string{"&&", "||", ";", "|"} {
		if i := strings.Index(head, sep); i >= 0 {
			head = head[:i]
		}
	}
	head = strings.TrimSpace(head)
	verb, rest, _ := strings.Cut(head, " ")
	rest = strings.TrimSpace(rest)
	switch verb {
	case "cd":
		w.cd(rest)
	case "pushd":
		if rest == "" {
			// Swaps the top two entries of the stack. The shell moved and this
			// cannot say where, so the path stops claiming to be right — it
			// used to stay put and stay certain, which is the one combination
			// that is a lie.
			w.certain = false
			return
		}
		if to, ok := w.resolve(rest); ok {
			w.stack = append(w.stack, w.here)
			w.moveTo(to)
			return
		}
		w.certain = false
	case "popd":
		if n := len(w.stack); n > 0 {
			w.moveTo(w.stack[n-1])
			w.stack = w.stack[:n-1]
			return
		}
		w.certain = false
	}
}

func (w *cdWalker) cd(arg string) {
	// `--` ends the options, and `cd -- -x` is how you reach a directory whose
	// name begins with a dash. Left in, it read as a directory called "--".
	if rest, ok := strings.CutPrefix(arg, "--"); ok {
		arg = strings.TrimSpace(rest)
	}
	if arg == "" {
		w.moveTo(w.home) // bare `cd` is home
		return
	}
	if arg == "-" {
		if w.prev == "" {
			w.certain = false
			return
		}
		w.moveTo(w.prev)
		return
	}
	if relative(arg) {
		// The only kind a session seam can spoil, and the only kind worth
		// asking the server about.
		w.tryRelative(arg)
		return
	}
	if to, ok := w.resolve(arg); ok {
		w.moveTo(to)
		return
	}
	// `cd $DEPLOY_DIR` and friends. The shell went somewhere; this cannot say
	// where, so the path stays put and stops claiming to be right.
	w.certain = false
}

// relative reports whether an operand is resolved against the current
// directory, which is the only kind a session restart can spoil. An absolute
// path, `~`, `-` and the stack forms all say where they are going.
func relative(arg string) bool {
	return arg != "" && arg != "-" && !strings.HasPrefix(arg, "/") && !strings.HasPrefix(arg, "~")
}

// resolve turns a `cd` operand into a path, or reports that it cannot.
func (w *cdWalker) resolve(arg string) (string, bool) {
	// A quoted literal is still a literal: `cd "my dir"` is followable, and
	// only expansion inside it is not.
	quoted := false
	if len(arg) >= 2 {
		if (arg[0] == '\'' && arg[len(arg)-1] == '\'') ||
			(arg[0] == '"' && arg[len(arg)-1] == '"') {
			inner := arg[1 : len(arg)-1]
			if !strings.ContainsAny(inner, "$`") {
				arg, quoted = inner, true
			}
		}
	}
	if strings.ContainsAny(arg, "$`*?") {
		return "", false
	}
	// A space in an *unquoted* operand means `cd` was given two of them, which
	// the shell refuses. Inside quotes it is just part of the name — checking
	// after unquoting would undo the unquoting.
	if !quoted && strings.ContainsAny(arg, " \t\"'") {
		return "", false
	}
	switch {
	case arg == "~":
		return w.home, w.home != ""
	case strings.HasPrefix(arg, "~") && !strings.HasPrefix(arg, "~/"):
		// `cd ~root`. Another account's home, which only the server knows.
		// Joining it onto the current path invented `/home/deploy/~root`.
		return "", false
	case strings.HasPrefix(arg, "~/"):
		if w.home == "" {
			return "", false
		}
		return path.Join(w.home, arg[2:]), true
	case strings.HasPrefix(arg, "/"):
		return path.Clean(arg), true
	case w.here == "":
		// Nothing to stand on — the home was not known and no absolute move has
		// happened yet. The relative tail is still worth keeping: `…/src` says
		// more than nothing, and the screen labels it as a fragment.
		return path.Clean(arg), false
	default:
		return path.Clean(path.Join(w.here, arg)), true
	}
}

func (w *cdWalker) moveTo(to string) {
	if to == "" {
		return
	}
	w.prev, w.here = w.here, to
	// An absolute answer puts the replay back on solid ground.
	w.certain = true
}

// tryRelative moves by an operand resolved against the current directory,
// falling back to the same move from home when the server says the first answer
// is not a real directory. See ReplayCdChecked for why home is the fallback.
func (w *cdWalker) tryRelative(arg string) {
	to, ok := w.resolve(arg)
	if !ok {
		if to != "" {
			// An unanchored fragment. Kept, never trusted, and never checked
			// against the server — there is no path to check.
			w.here = to
		}
		w.certain = false
		return
	}
	if w.exists == nil || w.exists(to) {
		w.moveTo(to)
		return
	}
	if w.home != "" {
		if restart := path.Clean(path.Join(w.home, arg)); w.exists(restart) {
			// A new shell, started at home. The stitch is the file's, not the
			// user's, and unpicking it here is what keeps the tree from growing
			// levels nobody ever stood in.
			w.prev, w.here = w.here, restart
			w.certain = true
			return
		}
	}
	// Neither is there. Something moved that this cannot follow — a `cd` into a
	// directory since deleted, or a seam this rule does not cover.
	w.certain = false
}

// bashStamp reads a `#<epoch>` line.
func bashStamp(line string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#")
	if !ok {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	// Comments are legal in a history file too. Only a bare number this large
	// is a timestamp; anything else is somebody's `# note to self`.
	if err != nil || n < 1_000_000_000 {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}

// quotesBalanced reports whether a line closes every quote it opens.
//
// Deliberately not a shell parser. It is the one test that separates "the
// command continues on the next line" from "the command ended here", and
// getting it approximately right on the common case beats getting it exactly
// right on a grammar this file does not promise to follow.
func quotesBalanced(s string) bool {
	var single, double bool
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // whatever follows is literal
		case '\'':
			if !double {
				single = !single
			}
		case '"':
			if !single {
				double = !double
			}
		}
	}
	return !single && !double
}
