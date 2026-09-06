package adapter

import (
	"strconv"
	"strings"
)

// "What happened since you last looked" (T-29, arch/07's revisit goal).
//
// The app is opened when something is already wrong. This is the one thing that
// gives somebody a reason to open it when nothing is — not by inventing news,
// but by answering a question they cannot answer any other way: the server was
// running while nobody was watching, and what did it do.
//
// Everything here is already in the journal. The cost is one pass over it.

// DigestScript counts, in one read, the four things worth knowing on return.
//
// The window is `$1`, passed as an argument rather than pasted into the script.
// The metrics script gets to be a constant because nothing goes into it; this
// one needs a value, and building it by concatenation is exactly how the
// argv-only rule (§3.2b) gets broken by a well-meaning change later. `sh -c
// <script> sh <since>` keeps the script a constant and the value an argument.
//
// Two things in the boot pattern are measured, not guessed.
//
// It is anchored on systemd[1] because a boot writes "Startup finished" more
// than once: every user instance says it too. One real reboot produced three of
// those lines on the server this was checked against — systemd[1248],
// systemd[1] and systemd[1876] — so an unanchored match reports one restart as
// three.
//
// And it does not look for journalctl's "-- Boot ... --" separator, which is
// the obvious way to count boots and does not appear at all under `-q -o
// short-iso`. Matching on something that is never printed is a counter that is
// always zero, which looks exactly like a server that never restarts.
const DigestScript = `since="$1"
journalctl --since "$since" --no-pager -q -o short-iso 2>/dev/null | awk '
/systemd\[1\]: Startup finished/          { boot++ }
/ sshd\[[0-9]*\]: Failed password/       { authfail++ }
/ sudo\[[0-9]*\]: .*COMMAND=/            { sudo++ }
/systemd\[1\]: .*(Failed with result|Failed to start)/ { unitfail++ }
END {
  printf "boots %d\n", boot + 0
  printf "authFailures %d\n", authfail + 0
  printf "sudoCommands %d\n", sudo + 0
  printf "unitFailures %d\n", unitfail + 0
}'
:`

// Digest is what changed while nobody was watching.
type Digest struct {
	// Boots is how many times the machine came up. One is a restart; more than
	// one is something worth looking at.
	Boots int `json:"boots"`
	// UnitFailures counts services that failed, not services that are failing —
	// a unit that broke and was restarted still happened.
	UnitFailures int `json:"unitFailures"`
	// AuthFailures is failed SSH passwords. On a box facing the internet this
	// is a large number and the size is the point, not the individual rows.
	AuthFailures int `json:"authFailures"`
	// SudoCommands is what somebody ran as root. Including, usually, this
	// person — the digest says what happened, not who is to blame.
	SudoCommands int `json:"sudoCommands"`
}

// Quiet reports whether there is nothing to say.
//
// A digest that shows four zeroes every time is a banner people stop reading,
// and then it is worse than nothing: it is a banner people stop reading that
// occasionally has something in it.
func (d Digest) Quiet() bool {
	return d.Boots == 0 && d.UnitFailures == 0 && d.AuthFailures == 0 && d.SudoCommands == 0
}

// ParseDigest reads the counts the script printed.
func ParseDigest(out string) Digest {
	var d Digest
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		switch f[0] {
		case "boots":
			d.Boots = n
		case "unitFailures":
			d.UnitFailures = n
		case "authFailures":
			d.AuthFailures = n
		case "sudoCommands":
			d.SudoCommands = n
		}
	}
	return d
}
