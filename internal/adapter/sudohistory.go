package adapter

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Privileged commands, read back out of the journal (arch/07, 명령 이력 C-2).
//
// The point of this source is that it is not a guess. A shell history file says
// what was typed but not where, and the directory has to be reconstructed by
// replaying `cd` — which breaks the moment two sessions interleave. sudo writes
// the working directory down at the moment it runs the command, so PWD here is
// a record. The cost is coverage: only what was run through sudo appears, and
// anything typed inside a `sudo -i` root shell goes to root's history instead.
//
// What the journal gives is also less structured than it looks. PWD, USER,
// COMMAND and TTY are not fields — they are one sentence inside MESSAGE, and
// its wording follows the sudoers config. The structured metadata that would
// have been convenient (_COMM, _EXE, _SYSTEMD_UNIT, _AUDIT_SESSION) is filled
// by journald reading /proc/<pid>, and sudo has usually exited before it looks:
// in the capture behind testdata/golden/sudo, one entry in five had them. So
// nothing here depends on those fields.

// SudoHistoryArgs builds the argv for one read of the sudo journal.
//
// `-t sudo` matches SYSLOG_IDENTIFIER, which is what sudo sets. argv, never a
// shell string (§3.2b), and `since` comes from a closed set the same way the
// event timeline's does — journalctl's --since takes free English and there is
// no good way to validate it.
func SudoHistoryArgs(since string, limit int) []string {
	if limit <= 0 || limit > journalMaxLines {
		limit = journalMaxLines
	}
	args := []string{"-t", "sudo", "-o", "json", "--no-pager", "-q", "-n", strconv.Itoa(limit)}
	if since != "" {
		args = append(args, "--since", since)
	}
	return args
}

// SudoRun is one line of the sudo journal that named a command.
type SudoRun struct {
	At time.Time `json:"at"`
	// User is who invoked sudo, and RunAs who it ran as — usually root.
	User  string `json:"user"`
	RunAs string `json:"runAs,omitempty"`
	// PWD is where it ran. A record, not a reconstruction, which is the whole
	// reason this source exists.
	PWD     string `json:"pwd"`
	Command string `json:"command"`
	TTY     string `json:"tty,omitempty"`
	// Refused marks a line sudo wrote *instead of* running something — a
	// password it wanted, a command the sudoers file did not allow. Kept rather
	// than dropped: "I tried and could not" is part of what happened here, and
	// on a host that always asks it is most of the file.
	Refused bool   `json:"refused,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Effect is what this command did to the server, which is the axis the
	// history is read along — see SudoEffect.
	Effect SudoEffect `json:"effect"`
	// BootID groups runs into boots, so "before the reboot" can be drawn.
	BootID string `json:"bootId,omitempty"`
}

// SudoEffect sorts a command by what it changed, not by what it is.
//
// The question this history answers is "what did I do to this box last time",
// and roughly eight lines in ten are looking rather than doing. Splitting on
// the effect lets the looking fold away and leaves the answer on one screen.
type SudoEffect string

const (
	// SudoChange altered something: a service, a package, a file, an image.
	SudoChange SudoEffect = "change"
	// SudoEdit opened an editor. Separate from change because the interesting
	// part is the file, and because it usually means a change followed.
	SudoEdit SudoEffect = "edit"
	// SudoRead only looked.
	SudoRead SudoEffect = "read"
)

// ParseSudoHistory reads `journalctl -t sudo -o json` output, newest first.
//
// Lines that are not command records — PAM session notices, anything whose
// sentence has no COMMAND in it — are skipped rather than guessed at.
func ParseSudoHistory(out []byte) []SudoRun {
	runs := []SudoRun{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]journalValue
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		r, ok := sudoRunFrom(raw)
		if !ok {
			continue
		}
		runs = append(runs, r)
	}
	// Newest first: "what did I do last time" is answered from the end.
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].At.After(runs[j].At) })
	return runs
}

func sudoRunFrom(raw map[string]journalValue) (SudoRun, bool) {
	msg := raw["MESSAGE"].String()
	if msg == "" {
		return SudoRun{}, false
	}
	at, ok := journalTime(raw["__REALTIME_TIMESTAMP"].String())
	if !ok {
		return SudoRun{}, false
	}
	run, ok := parseSudoMessage(msg)
	if !ok {
		return SudoRun{}, false
	}
	run.At = at
	run.BootID = raw["_BOOT_ID"].String()
	return run, true
}

// sudoFields are the keys sudo writes into its sentence. Anything else before
// the first of them is the reason a command did not run.
var sudoFields = map[string]bool{
	"TTY": true, "PWD": true, "USER": true, "COMMAND": true,
	"GROUP": true, "ENV": true, "TSID": true, "APPARMOR_PROFILE": true,
}

// parseSudoMessage pulls one sudo sentence apart.
//
//	deploy : TTY=pts/0 ; PWD=/etc/nginx ; USER=root ; COMMAND=/usr/bin/nginx -t
//	deploy : a password is required ; PWD=/home/deploy ; USER=root ; COMMAND=/usr/bin/true
//
// COMMAND is taken as everything after `COMMAND=` rather than as one more
// " ; "-separated field, because the command can contain that separator itself
// — `sh -c 'cd x ; ./go'` is an ordinary thing to run through sudo, and
// splitting it would silently truncate exactly the interesting rows.
func parseSudoMessage(msg string) (SudoRun, bool) {
	user, rest, found := strings.Cut(strings.TrimSpace(msg), " : ")
	if !found {
		return SudoRun{}, false
	}
	run := SudoRun{User: strings.TrimSpace(user)}

	head := rest
	if before, after, ok := strings.Cut(rest, "COMMAND="); ok {
		run.Command = strings.TrimSpace(after)
		head = before
	}
	if run.Command == "" {
		// No command named: a PAM notice, an authentication line, something
		// this history has nothing to say about.
		return SudoRun{}, false
	}

	for _, seg := range strings.Split(head, " ; ") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		key, value, ok := strings.Cut(seg, "=")
		if !ok || !sudoFields[strings.TrimSpace(key)] {
			// Not a field, so it is sudo saying why it refused. The wording
			// follows the sudoers config, so it is carried through as written
			// rather than matched against a list that would go stale.
			run.Refused = true
			run.Reason = seg
			continue
		}
		switch strings.TrimSpace(key) {
		case "TTY":
			run.TTY = value
		case "PWD":
			run.PWD = value
		case "USER":
			run.RunAs = value
		}
	}
	run.Effect = ClassifyCommand(run.Command)
	return run, true
}

// readOnly are commands that answer a question and change nothing.
var readOnly = map[string]bool{
	"ls": true, "cat": true, "less": true, "more": true, "head": true,
	"tail": true, "grep": true, "find": true, "stat": true, "file": true,
	"df": true, "du": true, "free": true, "ps": true, "top": true, "htop": true,
	"uptime": true, "id": true, "whoami": true, "uname": true, "hostname": true,
	"dmesg": true, "journalctl": true, "lsof": true, "ss": true, "netstat": true,
	"ip": true, "ping": true, "true": true, "echo": true, "which": true,
}

var editors = map[string]bool{
	"vi": true, "vim": true, "nvim": true, "nano": true, "emacs": true,
	"ed": true, "pico": true, "sensible-editor": true, "visudo": true,
}

// readSubcommands are the verbs that make an otherwise-changing program
// read-only. `systemctl status` and `systemctl restart` are not the same event
// and the base name cannot tell them apart.
var readSubcommands = map[string]map[string]bool{
	"systemctl": {"status": true, "show": true, "cat": true, "list-units": true,
		"list-timers": true, "is-active": true, "is-enabled": true, "is-failed": true},
	"service":   {"status": true},
	"docker":    {"ps": true, "logs": true, "images": true, "inspect": true, "stats": true, "top": true},
	"podman":    {"ps": true, "logs": true, "images": true, "inspect": true, "stats": true, "top": true},
	"git":       {"status": true, "log": true, "diff": true, "show": true, "branch": true},
	"apt":       {"list": true, "show": true, "search": true, "policy": true},
	"apt-get":   {"check": true},
	"ufw":       {"status": true},
	"firewalld": {"state": true},
}

// ClassifyCommand decides what a command did to the server.
//
// Exported because both history sources sort along this axis — sudo's journal
// and the app's own terminal log — and two copies of this table would drift
// into disagreeing about what `systemctl status` is.
//
// The default is change, and deliberately: an unrecognised command shown among
// the changes is noise, and an unrecognised command folded away with the reads
// is a change nobody sees. The first costs a line, the second costs the answer.
func ClassifyCommand(cmd string) SudoEffect {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return SudoChange
	}
	base := path.Base(fields[0])
	if editors[base] {
		return SudoEdit
	}
	if subs, ok := readSubcommands[base]; ok {
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				continue
			}
			if subs[f] {
				return SudoRead
			}
			break
		}
		return SudoChange
	}
	if readOnly[base] {
		return SudoRead
	}
	return SudoChange
}

// secretish matches the shapes a credential takes on a command line.
//
// A shell history is the highest-density credential file on most servers, and
// the sudo journal is a slice of the same thing. Nothing here is a guarantee —
// the point is that a screen full of history should not put a password in front
// of somebody who was looking for a systemctl call.
var secretish = []*regexp.Regexp{
	// -pPASSWORD, the MySQL shape, where the value is glued to the flag. The
	// leading boundary is not decoration: without it this matched the `--p` of
	// `--password` and masked the word "assword" while leaving the actual
	// secret, one space away, in the clear.
	regexp.MustCompile(`(?i)(?:^|\s)(-p)([^\s\-=][^\s]*)`),
	// --password=x, --token x, --api-key=x and friends.
	regexp.MustCompile(`(?i)(--?(?:password|passwd|pass|token|secret|api[-_]?key|access[-_]?key)[= ])([^\s]+)`),
	// FOO_TOKEN=x as an assignment or an export.
	regexp.MustCompile(`(?i)([A-Z0-9_]*(?:PASSWORD|PASSWD|TOKEN|SECRET|KEY)[A-Z0-9_]*=)([^\s]+)`),
	// Authorization: Bearer x
	regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+)([^\s"']+)`),
}

// MaskSecrets replaces anything that looks like a credential, and reports
// whether it found one.
//
// The flag is as useful as the masking: "this server's history has 12 lines
// with a password in them" is a finding somebody will want to act on, which is
// the same shape as the sshd config review.
func MaskSecrets(cmd string) (string, bool) {
	found := false
	out := cmd
	for _, re := range secretish {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			g := re.FindStringSubmatch(m)
			if len(g) == 0 {
				return m
			}
			// The value is the last group in every pattern here, and it is at
			// the end of the match. Rebuilding from the match itself rather
			// than from the groups keeps whatever the pattern needed in front
			// of the flag — a leading space, say — instead of eating it.
			secret := g[len(g)-1]
			if secret == "" {
				return m
			}
			found = true
			return strings.TrimSuffix(m, secret) + "••••••"
		})
	}
	return out, found
}
