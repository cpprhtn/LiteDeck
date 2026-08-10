package adapter

// Who is logged in over SSH, and cutting them off.
//
// The obvious sources for this are `w`, `who` and `loginctl`, and all three can
// come back empty on a machine that plainly has sessions on it: they read utmp
// or logind, and a container writes neither. Measured on the systemd fixture with
// three live SSH sessions, `w -h`, `who -u` and `loginctl list-sessions` returned
// nothing at all while ps listed every one of them.
//
// So the list is built from ps, which is always right, and the friendlier fields
// are folded in from the others when they happen to be available. The same shape
// as the Windows network view: take the reliable source, enrich from the nice one.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SSHSession is one login.
type SSHSession struct {
	PID  int    `json:"pid"`
	PPID int    `json:"ppid"`
	User string `json:"user"`
	// TTY is pts/0 for an interactive login and empty for a command or an
	// SFTP transfer, which sshd labels "notty".
	TTY string `json:"tty,omitempty"`
	// From is the client address, available only when the caller can see other
	// users' sockets — ss needs privileges for that.
	From string `json:"from,omitempty"`
	// Seconds since the session process started. sshd's own uptime, not the
	// user's idle time.
	Elapsed int64 `json:"elapsed"`
	// Idle and What come from w and stay empty where utmp is not written.
	Idle string `json:"idle,omitempty"`
	What string `json:"what,omitempty"`
	// Self marks the connection LiteDeck is using. The UI greys the button; the
	// binding refuses regardless.
	Self bool `json:"self"`
}

// Interactive reports a login with a terminal, as opposed to a single command or
// a file transfer.
func (s SSHSession) Interactive() bool { return s.TTY != "" }

// SessionPSArgs lists processes for the session view.
//
// The same projection the process view uses, minus the columns that do not
// matter here. args must stay last: it is the one field that can contain
// anything.
func SessionPSArgs() []string {
	return []string{"-eo", "pid,ppid,user:32,etimes,args", "--no-headers"}
}

// SelfAncestorsScript reports every sshd process between this command and init.
//
// This is what makes "do not cut off your own connection" a fact rather than a
// hope. Killing a process that LiteDeck is running underneath disconnects
// LiteDeck, and the set of such processes is exactly the ancestor chain — no port
// matching, no privileges, no guessing.
//
// Measured on the fixture it returns three: the session process for this command,
// the privileged process for the whole connection, and the listening daemon.
// Killing the second ends the connection; killing the third ends sshd for
// everybody.
const SelfAncestorsScript = `p=$$; out=""; while [ "$p" -gt 1 ]; do ` +
	`if [ "$(cat /proc/$p/comm 2>/dev/null)" = "sshd" ]; then out="$out $p"; fi; ` +
	`p=$(awk '{print $4}' /proc/$p/stat 2>/dev/null); done; echo "$out"`

// ParseSelfAncestors reads the ancestor script's output.
func ParseSelfAncestors(data []byte) map[int]bool {
	out := map[int]bool{}
	for _, f := range strings.Fields(string(data)) {
		if n, err := strconv.Atoi(f); err == nil && n > 0 {
			out[n] = true
		}
	}
	return out
}

// sshdSession matches "sshd: alice@pts/0" and "sshd: alice@notty".
//
// The user name is taken up to the last @ because a login name cannot contain
// one but the field after it — the terminal — is fixed vocabulary.
var sshdSession = regexp.MustCompile(`^sshd:\s+(\S+)@(\S+)$`)

// sshdPriv matches "sshd: alice [priv]", the privileged half of a connection.
// It is not a session and is never listed, but it is the process that owns the
// connection, so it matters for the self check.
var sshdPriv = regexp.MustCompile(`^sshd:\s+(\S+)\s+\[priv\]$`)

// ParseSSHSessions builds the session list from ps output.
//
// selfPIDs comes from SelfAncestorsScript. A session whose process or whose
// parent is in that set belongs to LiteDeck's own connection: the parent test is
// what catches the transient per-command session processes, which are children of
// the connection's privileged process.
func ParseSSHSessions(data []byte, selfPIDs map[int]bool) ([]SSHSession, error) {
	out := []SSHSession{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Four fixed fields then the command line, which may contain spaces.
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		elapsed, _ := strconv.ParseInt(fields[3], 10, 64)

		// Rebuild args from the original line so internal spacing survives.
		args := strings.TrimSpace(strings.Join(fields[4:], " "))
		m := sshdSession.FindStringSubmatch(args)
		if m == nil {
			continue
		}
		user, tty := m[1], m[2]
		if tty == "notty" {
			tty = ""
		}

		out = append(out, SSHSession{
			PID:     pid,
			PPID:    ppid,
			User:    user,
			TTY:     tty,
			Elapsed: elapsed,
			Self:    selfPIDs[pid] || selfPIDs[ppid],
		})
	}

	// Newest first: the session someone just opened is the one being looked for.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Elapsed < out[j].Elapsed })
	return out, nil
}

// SessionListenerPIDs returns the sshd processes that are not sessions — the
// listening daemon and the privileged halves.
//
// They never appear in the list, and they are refused as kill targets: ending the
// listener stops sshd for everyone on the machine, which is the same class of
// mistake as signalling PID 1.
func SessionListenerPIDs(data []byte) map[int]bool {
	out := map[int]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimRight(line, "\r"))
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		args := strings.TrimSpace(strings.Join(fields[4:], " "))
		if !strings.HasPrefix(args, "sshd:") {
			continue
		}
		if sshdPriv.MatchString(args) || strings.Contains(args, "[listener]") {
			out[pid] = true
		}
	}
	return out
}

// SSArgs22 lists established connections to the SSH port.
//
// -p attaches the owning process, which needs privileges for anyone else's
// sockets; without it the addresses still come back and the column is simply
// blank. That is the difference between a missing detail and a broken view.
func SSArgs22() []string {
	return []string{"-tnpH", "state", "established", "( sport = :22 )"}
}

var ssUsers = regexp.MustCompile(`pid=(\d+)`)

// ParseSSHPeers maps an sshd PID to the client address it is serving.
func ParseSSHPeers(data []byte) map[int]string {
	out := map[int]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		peer := fields[3]
		for _, m := range ssUsers.FindAllStringSubmatch(line, -1) {
			if pid, err := strconv.Atoi(m[1]); err == nil {
				// Both the session and the privileged process hold the socket;
				// recording both means the lookup succeeds whichever the list
				// happens to hold.
				out[pid] = peer
			}
		}
	}
	return out
}

// wLine pulls the idle time and current command out of `w -h`.
//
// Columns are USER TTY FROM LOGIN@ IDLE JCPU PCPU WHAT, and FROM can be a dash,
// a hostname or an address, so the parse is positional from the left and stops
// caring after IDLE.
func ParseWIdle(data []byte) (idle map[string]string, what map[string]string) {
	idle, what = map[string]string{}, map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		tty := fields[1]
		idle[tty] = fields[4]
		what[tty] = strings.Join(fields[7:], " ")
	}
	return idle, what
}

// KillSessionArgs returns the argv that ends one session.
//
// TERM rather than KILL: sshd closes the connection cleanly, writes the logout
// record, and lets the user's shell run its exit handlers. A session that ignores
// TERM is rare enough to be worth a second, explicit press.
func KillSessionArgs(pid int) []string {
	return []string{"-TERM", "--", fmt.Sprint(pid)}
}
