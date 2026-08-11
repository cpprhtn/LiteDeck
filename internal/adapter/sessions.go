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
//
// The enrichment reads three sources rather than one, because they do not fail
// together and the failures are not the ones you would guess:
//
//   - `ss -p` is the most precise and the least available. It needs privileges to
//     see anyone else's socket, so on the ordinary case — LiteDeck logged in as a
//     normal user — it answers for nothing but our own connection.
//   - `w -h` carries the origin, the idle time and the running command in one
//     line, and is the one that most often prints nothing at all.
//   - `who -u` carries the origin and little else, and kept working on a fixture
//     where `w` had already given up.
//
// Whichever answers first wins, most precise first. A blank column is the
// correct output when none of them can speak; a wrong one is not.

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

// ServerPortScript asks the server which port this connection arrived on.
//
// sshd sets SSH_CONNECTION in every session to "<client ip> <client port>
// <server ip> <server port>", so the answer is authoritative, needs no
// privileges and costs one round trip that is cached for the connection's life.
//
// Asking is not pedantry. The port LiteDeck dialled is the wrong answer whenever
// anything sits in between — a published container port, a forwarded router
// port, a jump host. The project's own demo fixture is reached on 2299 while its
// sshd listens on 22, so the first version of this code, which used the
// configured port, was wrong on the very machine it was written against.
const ServerPortScript = `printf '%s\n' "$SSH_CONNECTION"`

// ParseServerPort reads the listening port out of SSH_CONNECTION.
//
// Zero when the variable is missing or malformed, which callers read as "use the
// default". A wrong port here is a silently empty column, so a refusal to guess
// beats a plausible guess.
func ParseServerPort(data []byte) int {
	fields := strings.Fields(string(data))
	if len(fields) < 4 {
		return 0
	}
	port, err := strconv.Atoi(fields[3])
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// SSArgsForPort lists established connections to the port sshd is listening on.
//
// -p attaches the owning process, which needs privileges for anyone else's
// sockets; without it the addresses still come back and the column is simply
// blank. That is the difference between a missing detail and a broken view.
//
// The port is a parameter and not the constant 22 it used to be. Hard-coding it
// meant the filter matched nothing at all on any server that moved sshd — a
// common hardening step, and one this project's own README recommends thinking
// of as "the SSH port" rather than "port 22". Every session's origin went blank
// and nothing said why.
func SSArgsForPort(port int) []string {
	if port <= 0 {
		port = 22
	}
	return []string{"-tnpH", "state", "established", fmt.Sprintf("( sport = :%d )", port)}
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

// ParseWIdle pulls the origin, idle time and current command out of `w -h`.
//
// Columns are USER TTY FROM LOGIN@ IDLE JCPU PCPU WHAT, keyed by TTY.
//
// FROM is read here as well, and that is the point of this pass. The origin
// column was previously filled only from `ss -p`, which cannot see another
// user's socket without privileges — so on every server where LiteDeck logs in
// as an ordinary user, which is the normal case, the column was always empty
// while `w` had the answer sitting in field 3 the whole time.
//
// A dash means the login is local and has no origin to report; it is dropped
// rather than shown, because "-" reads as missing data.
func ParseWIdle(data []byte) (from, idle, what map[string]string) {
	from, idle, what = map[string]string{}, map[string]string{}, map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		tty := fields[1]
		if f := fields[2]; f != "-" && f != ":0" {
			from[tty] = f
		}
		idle[tty] = fields[4]
		what[tty] = strings.Join(fields[7:], " ")
	}
	return from, idle, what
}

// WhoArgs asks who is logged in, with the origin.
func WhoArgs() []string { return []string{"-u"} }

// whoFrom matches the origin `who -u` puts in trailing parentheses.
//
// The columns before it are not fixed: LOGIN@ is two fields on some builds and
// three on others, IDLE is "." or "old" or HH:MM, and the PID may or may not be
// there. Anchoring on the parenthesised tail is the only part of the line whose
// position does not move.
var whoFrom = regexp.MustCompile(`\(([^()]+)\)\s*$`)

// ParseWho reads `who -u`, keyed by TTY.
//
// A second source for the same column as ParseWIdle, because they do not fail
// together: measured on the systemd fixture with four live logins, `w -h`
// printed nothing at all while `who -u` listed every one of them with its
// origin. Both read utmp; only one of them insists on more than that.
func ParseWho(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		m := whoFrom.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if host := strings.TrimSpace(m[1]); host != "" && host != ":0" {
			out[fields[1]] = host
		}
	}
	return out
}

// KillSessionArgs returns the argv that ends one session.
//
// TERM rather than KILL: sshd closes the connection cleanly, writes the logout
// record, and lets the user's shell run its exit handlers. A session that ignores
// TERM is rare enough to be worth a second, explicit press.
func KillSessionArgs(pid int) []string {
	return []string{"-TERM", "--", fmt.Sprint(pid)}
}
