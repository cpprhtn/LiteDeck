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
// # Why the directory is always an estimate here
//
// Even with perfect `cd` parsing the answer is a guess, and not because of the
// parsing. bash appends a session's lines when the shell *exits*, so a file is
// ordered by when sessions ended, not by when commands ran. Two overlapping
// sessions interleave, and replaying `cd` across the seam produces a path that
// never existed. There is no marker in the file for where one session's block
// begins. So every path from this source is labelled an estimate — not as a
// hedge, but because that is what it is.

// ShellCommand is one line of history, with where it probably ran.
type ShellCommand struct {
	Command string `json:"command"`
	// At is zero where the file carries no times, which is the common case for
	// bash. A zero here means "unknown", never "the epoch".
	At time.Time `json:"at,omitempty"`
	// PWD is the directory `cd` replay arrived at. Always an estimate — see
	// above — and empty until the replay has something absolute to stand on.
	PWD string `json:"pwd,omitempty"`
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
	var pending time.Time

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if t, ok := bashStamp(line); ok {
			pending = t
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
		pending = time.Time{}
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
		var at time.Time
		cmd := line
		if rest, ok := strings.CutPrefix(line, ": "); ok {
			if stamp, body, found := strings.Cut(rest, ";"); found {
				if secs, _, ok := strings.Cut(stamp, ":"); ok {
					if n, err := strconv.ParseInt(secs, 10, 64); err == nil && n > 0 {
						at = time.Unix(n, 0).UTC()
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

// ReplayCd walks the history forward, keeping track of where each command
// probably ran.
//
// `home` is where the replay starts, and it matters more than it looks: a real
// history begins with relative moves — the first line of the measured file was
// `cd monitoring/` — because a login shell starts in the user's home and
// nobody types the absolute path they are already standing in. Starting from
// nothing left almost every command without a directory, which is the feature
// not working rather than the feature being careful.
//
// Pass "" when the home directory is not known; then the path stays empty until
// an absolute `cd` gives the replay something to stand on.
//
// Nothing is invented either way. A `cd` this cannot resolve — `cd $DIR`,
// `cd -`, anything quoted — leaves the path where it was rather than moving it
// somewhere made up.
func ReplayCd(cmds []ShellCommand, home string) []ShellCommand {
	here := home
	out := make([]ShellCommand, len(cmds))
	for i, c := range cmds {
		if dir, ok := ParseCd(c.Command); ok {
			here = ResolveCd(here, dir)
		}
		c.PWD = here
		out[i] = c
	}
	return out
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

// ParseCd reads a `cd` off a command line.
//
// Only the plain forms. `cd $DEPLOY_DIR`, `cd -`, `pushd`, `(cd x && …)` and
// `make -C` are all real and none can be resolved from the text alone, so they
// are treated as "not a cd this can follow" — which leaves the path where it
// was rather than moving it somewhere invented.
func ParseCd(line string) (string, bool) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) == 0 || f[0] != "cd" {
		return "", false
	}
	if len(f) == 1 {
		return "~", true // bare `cd` is home
	}
	if len(f) > 2 {
		return "", false
	}
	arg := f[1]
	if strings.ContainsAny(arg, "$`*?\"'") || arg == "-" {
		return "", false
	}
	return arg, true
}

// ResolveCd joins a target onto the current path.
func ResolveCd(base, target string) string {
	switch {
	case target == "~" || strings.HasPrefix(target, "~/"):
		// Home is not known from here, and guessing /home/<user> is wrong for
		// root and on macOS. The tilde is kept as written.
		return target
	case strings.HasPrefix(target, "/"):
		return path.Clean(target)
	case base == "":
		return ""
	default:
		return path.Clean(path.Join(base, target))
	}
}
