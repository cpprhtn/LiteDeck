package app

import (
	"path"
	"regexp"
	"strings"
)

// What a person is asked about whatever the approval mode.
//
// # Why a floor under the modes
//
// The modes answer one question: does an allowed change interrupt anyone. The
// relaxed one exists so an agent can work unattended, and that is right for
// restarting nginx or editing its config — the change is bounded by the server
// the user had in mind, and the Changes tab can put a file back.
//
// Three kinds of call are not bounded that way, and "let it work overnight"
// was never consent to them:
//
//   - Secrets leaving the server. A private key read into a model's context is
//     in the client's transcript and the provider's logs. No undo reaches
//     those.
//   - Footholds. A line in authorized_keys, a cron entry, a unit file or a
//     shell startup file outlives the session and lets someone in, or runs,
//     when nobody is watching. Undo restores the file only after somebody
//     notices; what ran in between stays done. docs/mcp.md already said
//     fs_write could reach these — this is where that stops being silent.
//   - Commands whose effect cannot be read off the line: text piped into a
//     shell, a recursive delete of / or a home directory, mkfs, a shutdown, and
//     sudo — run_command has no terminal for a password, but on a server that
//     allows this user sudo without one, it simply runs as root.
//
// # What this is not
//
// A floor, not a classifier. Nothing here decides that a call is safe, so
// nothing here can make anything run without a dialog that would otherwise
// have had one. A false positive costs one dialog in the relaxed mode; a false
// negative leaves things exactly as they were before this file existed. The
// patterns are tuned for the first property — they would rather ask about
// `crontab -l` than miss `crontab -r`.
//
// And it is still a person deciding: none of these are refused outright. The
// user may well want the agent to add a deploy key; they just have to see it.

// Guard kinds. They travel to the dialog as codes so the frontend can say them
// in the reader's language, and to the model in English (guardReason.forModel).
const (
	guardSecret      = "secret"
	guardFoothold    = "foothold"
	guardOpaque      = "opaque"
	guardRoot        = "root"
	guardDestructive = "destructive"
)

// guardReason is why a call is asked about in every mode. The zero value means
// it is not.
type guardReason struct {
	Kind string
	// Detail is the matched path or the part of the command that tripped it,
	// so the dialog can point at it rather than at the whole line.
	Detail string
}

func (g guardReason) any() bool { return g.Kind != "" }

// forModel is the sentence appended to a declined or unanswered call, so the
// agent learns that relaxing the mode would not have helped and stops asking
// the user to do it.
func (g guardReason) forModel() string {
	var why string
	switch g.Kind {
	case guardSecret:
		why = "it reads or replaces a file that probably holds a credential (" + g.Detail + "), " +
			"and what reaches your context cannot be taken back"
	case guardFoothold:
		why = "it touches " + g.Detail + ", which decides who can log in or what runs later " +
			"without anyone watching"
	case guardOpaque:
		why = "what it runs is not on the line the user was shown (" + g.Detail + ")"
	case guardRoot:
		why = "it asks for root (" + g.Detail + ")"
	case guardDestructive:
		why = "it cannot be undone and reaches far beyond one service (" + g.Detail + ")"
	default:
		return ""
	}
	return "LiteDeck asks about this in every approval mode, including the one that asks " +
		"about nothing, because " + why + ". Relaxing the mode would not change that"
}

// guardFor is the floor for one write request.
func guardFor(req writeRequest) guardReason {
	switch req.tool {
	case "run_command":
		return commandGuard(req.command)
	case "fs_write", "fs_edit", "fs_delete":
		if g := footholdPath(req.path); g.any() {
			return g
		}
		return secretPath(req.path)
	}
	return guardReason{}
}

/* ---------------------------------------------------------------- paths */

// normPath lowercases and cleans a remote path for matching. Lowercase because
// a Windows host matches C:/Users/Me/.SSH as readily as the POSIX spelling, and
// a POSIX path differing only in case from a secret is suspicious enough to ask.
func normPath(p string) string {
	p = strings.TrimSpace(toRemotePath(p))
	if p == "" {
		return ""
	}
	return strings.ToLower(path.Clean(p))
}

// hasComponent reports whether dir appears as a whole path component.
func hasComponent(p, dir string) bool {
	return strings.Contains(p+"/", "/"+dir+"/")
}

var privateKeyName = regexp.MustCompile(`^(id_[a-z0-9_]+|ssh_host_[a-z0-9_]+_key|identity)$`)

// envExamples are .env variants that are committed on purpose and hold none.
var envExamples = map[string]bool{
	".env.example": true, ".env.sample": true, ".env.template": true,
	".env.dist": true, ".env.defaults": true,
}

// credentialFiles are names that hold a credential wherever they live.
var credentialFiles = map[string]bool{
	".netrc": true, "_netrc": true, ".pgpass": true, ".my.cnf": true,
	".git-credentials": true, ".npmrc": true, ".pypirc": true,
}

// secretPath is the floor for reading a file into the model's context, and for
// replacing one.
func secretPath(raw string) guardReason {
	p := normPath(raw)
	if p == "" {
		return guardReason{}
	}
	base := path.Base(p)
	hit := guardReason{Kind: guardSecret, Detail: raw}

	switch {
	case strings.HasSuffix(base, ".pub"):
		// The one file in ~/.ssh meant to be handed out.
		return guardReason{}
	case privateKeyName.MatchString(base) && (hasComponent(p, ".ssh") || hasComponent(p, "ssh")):
		return hit
	case base == ".env" || (strings.HasPrefix(base, ".env.") && !envExamples[base]):
		return hit
	case credentialFiles[base]:
		return hit
	case p == "/etc/shadow" || p == "/etc/gshadow":
		return hit
	case strings.HasSuffix(p, "/.aws/credentials"), strings.HasSuffix(p, "/.docker/config.json"),
		strings.HasSuffix(p, "/.kube/config"), strings.HasSuffix(p, "/.config/gh/hosts.yml"):
		return hit
	}

	// Key stores by extension. A certificate directory is left out: those
	// are public by design, and asking about every CA bundle would teach
	// people to click through the dialog this is meant to make them read.
	if hasComponent(p, "certs") || hasComponent(p, "ca-certificates") {
		return guardReason{}
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".ppk", ".kdbx"} {
		if strings.HasSuffix(base, ext) {
			return hit
		}
	}
	return guardReason{}
}

// footholdPrefixes are directories where a new or changed file runs, or
// grants access, on its own later.
var footholdPrefixes = []string{
	"/etc/sudoers.d/",
	"/etc/cron.d/", "/etc/cron.hourly/", "/etc/cron.daily/", "/etc/cron.weekly/", "/etc/cron.monthly/",
	"/var/spool/cron/",
	"/etc/systemd/", "/lib/systemd/system/", "/usr/lib/systemd/system/",
	"/etc/init.d/", "/etc/pam.d/", "/etc/profile.d/",
	"/etc/ssh/sshd_config.d/",
}

var footholdFiles = map[string]bool{
	"/etc/sudoers": true, "/etc/crontab": true, "/etc/anacrontab": true,
	"/etc/passwd": true, "/etc/shadow": true, "/etc/group": true, "/etc/gshadow": true,
	"/etc/ssh/sshd_config": true, "/etc/ld.so.preload": true, "/etc/rc.local": true,
	"/etc/profile": true, "/etc/bash.bashrc": true, "/etc/environment": true,
	"/etc/zsh/zshrc": true, "/etc/zshrc": true,
}

// shellStartup are per-user files the next login runs.
var shellStartup = map[string]bool{
	".bashrc": true, ".bash_profile": true, ".bash_login": true, ".profile": true,
	".zshrc": true, ".zprofile": true, ".zshenv": true, ".zlogin": true,
}

// footholdPath is the floor for writing or deleting a file.
func footholdPath(raw string) guardReason {
	p := normPath(raw)
	if p == "" {
		return guardReason{}
	}
	hit := guardReason{Kind: guardFoothold, Detail: raw}
	base := path.Base(p)

	if base == "authorized_keys" || base == "authorized_keys2" {
		return hit
	}
	if footholdFiles[p] || shellStartup[base] {
		return hit
	}
	for _, prefix := range footholdPrefixes {
		if strings.HasPrefix(p, prefix) {
			return hit
		}
	}
	if strings.Contains(p, "/.config/systemd/user/") || strings.Contains(p, "/.config/autostart/") {
		return hit
	}
	return guardReason{}
}

/* ------------------------------------------------------------- commands */

// The command checks read the line the way a person skimming the dialog
// would, not the way sh parses it. Quoting, line continuations and variables
// can hide any of these from a regular expression — which is fine for a
// floor: a hidden one is no worse off than before, and the dialog in the
// default mode still shows the line.
var (
	// Text piped into a shell. Whatever produced that text is what runs.
	pipeToShell = regexp.MustCompile(`\|\s*(?:sudo\s+(?:-\S+\s+)*)?(?:env\s+)?(?:\S*/)?(?:sh|bash|zsh|dash|ksh|fish)\b`)
	// An interpreter reading its program from stdin: `| python3`, `| perl -`.
	pipeToInterp = regexp.MustCompile(`\|\s*(?:sudo\s+)?(?:\S*/)?(?:python[0-9.]*|perl|ruby|node|php)\s*(?:-\s*)?(?:$|[;&|)])`)
	// Code fetched or computed and run in the same breath.
	runSubstituted = regexp.MustCompile(`(?:^|[\s;&|(])(?:source|\.|sh|bash|zsh|dash)\s+<\(|(?:^|[\s;&|(])eval\s|(?:sh|bash|zsh|dash)\s+-c\s+["']?(?:\$\(|` + "`" + `)`)

	// A command word that asks for root.
	asRoot = regexp.MustCompile(`(?:^|[;&|(\n]|&&|\|\|)\s*(?:(?:nohup|time|nice|exec|command|xargs|env)\s+(?:\S+=\S*\s+)*)*(sudo|su|doas|pkexec|run0)(?:\s|$)`)

	diskWipe = regexp.MustCompile(`(?:^|[\s;&|(])(mkfs(?:\.\w+)?|wipefs|blkdiscard)\b|\bdd\b[^;&|\n]*\bof=/dev/|>\s*/dev/(?:sd|nvme|vd|xvd|hd|mmcblk|disk)|\bshred\b[^;&|\n]*/dev/`)
	powerOff = regexp.MustCompile(`(?:^|[;&|(\n]|&&|\|\|)\s*(?:sudo\s+)?(shutdown|reboot|poweroff|halt)\b|\bsystemctl\s+(?:reboot|poweroff|halt|kexec|emergency|rescue)\b|\binit\s+[06]\b`)
	forkBomb = regexp.MustCompile(`:\s*\(\s*\)\s*\{`)

	// Account changes. visudo and crontab are here rather than under footholds
	// because they edit those files without naming them.
	accountCmd = regexp.MustCompile(`(?:^|[;&|(\n]|&&|\|\|)\s*(?:sudo\s+)?(useradd|usermod|userdel|adduser|deluser|passwd|chpasswd|visudo|crontab)\b`)

	segmentSplit = regexp.MustCompile(`&&|\|\||[;&|\n]`)
)

// footholdMentions are substrings that, anywhere in a command, mean it reads
// or writes a file footholdPath or secretPath would ask about.
var footholdMentions = []string{
	"authorized_keys", "/etc/sudoers", "/etc/crontab", "/etc/cron.", "/var/spool/cron",
	"/etc/systemd/system", "/.config/systemd/user", "/etc/ld.so.preload", "/etc/pam.d",
	"/etc/rc.local", "systemctl enable", "systemctl --user enable",
}

var secretMentions = []string{
	".ssh/id_", "ssh_host_", "/etc/shadow", ".aws/credentials", ".git-credentials",
	".netrc", ".pgpass", ".kube/config", ".docker/config.json",
}

// commandGuard is the floor for run_command.
func commandGuard(command string) guardReason {
	line := strings.TrimSpace(command)
	if line == "" {
		return guardReason{}
	}
	lower := strings.ToLower(line)

	if m := pipeToShell.FindString(lower); m != "" {
		return guardReason{Kind: guardOpaque, Detail: strings.TrimSpace(m)}
	}
	if m := pipeToInterp.FindString(lower); m != "" {
		return guardReason{Kind: guardOpaque, Detail: strings.TrimSpace(m)}
	}
	if m := runSubstituted.FindString(lower); m != "" {
		return guardReason{Kind: guardOpaque, Detail: strings.TrimSpace(m)}
	}

	if g := recursiveOnProtected(line); g.any() {
		return g
	}
	for _, re := range []*regexp.Regexp{diskWipe, powerOff, forkBomb} {
		if m := re.FindString(lower); m != "" {
			return guardReason{Kind: guardDestructive, Detail: strings.TrimSpace(m)}
		}
	}

	if m := asRoot.FindStringSubmatch(lower); m != nil {
		return guardReason{Kind: guardRoot, Detail: m[1]}
	}

	for _, s := range secretMentions {
		if strings.Contains(lower, s) {
			return guardReason{Kind: guardSecret, Detail: s}
		}
	}
	if m := accountCmd.FindStringSubmatch(lower); m != nil {
		return guardReason{Kind: guardFoothold, Detail: m[1]}
	}
	for _, s := range footholdMentions {
		if strings.Contains(lower, s) {
			return guardReason{Kind: guardFoothold, Detail: s}
		}
	}
	return guardReason{}
}

// recursiveTools are the commands whose recursive form on a protected path is
// the same disaster whatever it does to each file.
var recursiveTools = map[string]bool{"rm": true, "chmod": true, "chown": true, "chgrp": true}

// wholeTree are targets that mean "everything here" without naming a path.
// run_command starts in the login user's home, so `*` and `.` are the home
// directory.
var wholeTree = map[string]bool{
	"/": true, "/*": true, "~": true, "~/": true, "~/*": true, "*": true, ".": true,
	"./": true, "./*": true, "..": true, "$home": true, "${home}": true,
	"$home/": true, "${home}/": true, "$home/*": true, "${home}/*": true,
}

// recursiveOnProtected finds `rm -r`, `chmod -R` and the like aimed at a path
// IsProtectedPath covers — the same paths the GUI makes somebody type out.
func recursiveOnProtected(line string) guardReason {
	for _, seg := range segmentSplit.Split(line, -1) {
		fields := strings.Fields(seg)
		// Step over wrappers so `sudo rm -rf /` is still an rm.
		for len(fields) > 0 && (fields[0] == "sudo" || fields[0] == "doas" ||
			fields[0] == "nohup" || fields[0] == "exec" || fields[0] == "command" ||
			fields[0] == "xargs" || strings.HasPrefix(fields[0], "-")) {
			fields = fields[1:]
		}
		if len(fields) == 0 || !recursiveTools[path.Base(fields[0])] {
			continue
		}
		recursive, endOfFlags := false, false
		var targets []string
		for _, f := range fields[1:] {
			switch {
			case endOfFlags || !strings.HasPrefix(f, "-"):
				targets = append(targets, strings.Trim(f, `"'`))
			case f == "--":
				endOfFlags = true
			case f == "--recursive":
				recursive = true
			case !strings.HasPrefix(f, "--") && strings.ContainsAny(f, "rR"):
				recursive = true
			}
		}
		if !recursive {
			continue
		}
		for _, t := range targets {
			// ~/.ssh holds authorized_keys: removing it recursively locks out
			// whoever logs in with a key, possibly the user at this keyboard.
			if wholeTree[strings.ToLower(t)] || path.Base(strings.TrimSuffix(t, "/")) == ".ssh" {
				return guardReason{Kind: guardDestructive, Detail: strings.TrimSpace(seg)}
			}
			if !strings.HasPrefix(t, "/") {
				continue
			}
			// "/var/log/*" empties /var/log; judge it as /var/log.
			cleaned, err := CleanRemotePath(strings.TrimSuffix(t, "/*"))
			if err == nil && IsProtectedPath(cleaned) {
				return guardReason{Kind: guardDestructive, Detail: strings.TrimSpace(seg)}
			}
		}
	}
	return guardReason{}
}
