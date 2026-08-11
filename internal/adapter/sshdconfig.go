package adapter

import (
	"bufio"
	"bytes"
	"path"
	"strconv"
	"strings"
)

// Reading the server's sshd configuration (§4.4).
//
// Everything here works on text the app fetched over SFTP. `sshd -T` would give
// the *effective* configuration, defaults included, but it needs root — and a
// view that demands a password before it can show you anything is a view nobody
// opens. Reading the files needs nothing: they are world-readable on every
// distribution that ships them, and SFTP is already open.
//
// The cost of that trade is stated rather than hidden: this knows what the files
// *declare*, and nothing about the defaults that apply where they are silent.
// SSHDReport.Declared is the word doing that work.

// SSHDDirective is one keyword a config file sets, and where it says it.
type SSHDDirective struct {
	Keyword string `json:"keyword"`
	Value   string `json:"value"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	// Conditional marks a directive that came after a Match block, so it
	// applies only to the users, addresses or hosts that block names. Treating
	// one of these as a global setting is the classic way to misread an sshd
	// config — "PermitRootLogin yes" under `Match Address 10.0.0.0/8` is not
	// the same statement as the one at the top of the file.
	Conditional bool `json:"conditional,omitempty"`
}

// SSHDInclude is one Include line, and where in the file's directives it sits.
//
// The position matters and is easy to lose. sshd expands an Include *at the
// line it appears on*, and takes the first value it sees for any keyword — so a
// drop-in included at the top of sshd_config beats the main file's own setting
// further down, which is precisely why Ubuntu puts the Include on line 12.
// Reading the parent file whole and appending the drop-ins afterwards inverts
// that and reports the losing value as the server's setting.
type SSHDInclude struct {
	// Patterns are absolute, relative ones already resolved.
	Patterns []string
	// After is how many of the file's directives precede this line.
	After int
}

// ParseSSHDConfig reads one file. Include targets come back unexpanded because
// resolving them is a remote glob, which belongs to the caller that has the
// connection.
func ParseSSHDConfig(file string, data []byte) (out []SSHDDirective, includes []SSHDInclude) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	inMatch := false
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// sshd accepts `Keyword value` and `Keyword=value` alike, but only the
		// first separator is one — an `=` later in the line belongs to the
		// value, as in `AuthorizedKeysCommand /usr/bin/f --key=%k`.
		keyword, value := line, ""
		if i := strings.IndexAny(line, " \t="); i >= 0 {
			keyword = line[:i]
			value = strings.TrimSpace(strings.TrimLeft(line[i:], " \t="))
		}
		if keyword == "" {
			continue
		}

		switch strings.ToLower(keyword) {
		case "match":
			// Everything from here to the next Match is conditional. There is
			// no closing token: a Match block runs to the end of the file or
			// to the next Match.
			inMatch = true
			out = append(out, SSHDDirective{
				Keyword: "Match", Value: value, File: file, Line: n, Conditional: true,
			})
			continue
		case "include":
			// Relative patterns resolve against the directory sshd_config
			// itself lives in, not the working directory of anything.
			inc := SSHDInclude{After: len(out)}
			for _, pat := range strings.Fields(value) {
				if !strings.HasPrefix(pat, "/") {
					pat = path.Join(path.Dir(file), pat)
				}
				inc.Patterns = append(inc.Patterns, pat)
			}
			if len(inc.Patterns) > 0 {
				includes = append(includes, inc)
			}
			continue
		}

		out = append(out, SSHDDirective{
			Keyword: keyword, Value: value, File: file, Line: n, Conditional: inMatch,
		})
	}
	return out, includes
}

// SSHDReport is what the whole set of files declares.
type SSHDReport struct {
	// Files actually read, in the order sshd would read them.
	Files []string `json:"files"`
	// Declared holds the winning directive per keyword. sshd takes the *first*
	// value it sees, not the last — the opposite of most config formats, and
	// the reason Ubuntu puts its Include at the top of the file.
	Declared []SSHDDirective `json:"declared"`
	// Matches lists the Match blocks, so a reader knows the answer above is not
	// the whole story for every client.
	Matches []SSHDDirective `json:"matches,omitempty"`
	// Notes are the things worth saying out loud, most serious first.
	Notes []SSHDNote `json:"notes,omitempty"`
	// Unreadable records files sshd would read that this could not, so a quiet
	// gap never looks like a clean bill of health.
	Unreadable []string `json:"unreadable,omitempty"`
}

// SSHDNote is one observation. The wording lives in the UI; this carries the
// code and the evidence so the sentence can name a file and a line.
type SSHDNote struct {
	// Code identifies the observation, e.g. "permit-root-login".
	Code  string `json:"code"`
	Level string `json:"level"` // warn | info
	Value string `json:"value"`
	File  string `json:"file"`
	Line  int    `json:"line"`
}

const (
	SSHDWarn = "warn"
	SSHDInfo = "info"
)

// BuildSSHDReport folds the directives from every file, in sshd's own reading
// order, into one answer.
func BuildSSHDReport(files []string, all []SSHDDirective, unreadable []string) SSHDReport {
	rep := SSHDReport{Files: files, Unreadable: unreadable}

	seen := make(map[string]bool, len(all))
	for _, d := range all {
		if d.Conditional {
			if strings.EqualFold(d.Keyword, "Match") {
				rep.Matches = append(rep.Matches, d)
			}
			// A conditional value never becomes the global answer, and it must
			// not consume the keyword either — the global default still applies
			// to everyone the Match does not name.
			continue
		}
		key := strings.ToLower(d.Keyword)
		if seen[key] {
			continue // first one wins
		}
		seen[key] = true
		rep.Declared = append(rep.Declared, d)
	}

	rep.Notes = sshdNotes(rep.Declared)
	return rep
}

// sshdNotes says only what the files say. Nothing is inferred from a keyword's
// absence: the built-in default differs between distributions — Debian's
// PermitRootLogin is prohibit-password where upstream's is yes — and guessing
// which one a server compiled in would be worse than staying quiet.
func sshdNotes(declared []SSHDDirective) []SSHDNote {
	var warns, infos []SSHDNote
	add := func(dst *[]SSHDNote, code, level string, d SSHDDirective) {
		*dst = append(*dst, SSHDNote{
			Code: code, Level: level, Value: d.Value, File: d.File, Line: d.Line,
		})
	}

	for _, d := range declared {
		v := strings.ToLower(d.Value)
		switch strings.ToLower(d.Keyword) {
		case "permitrootlogin":
			if v == "yes" {
				add(&warns, "permit-root-login", SSHDWarn, d)
			}
		case "permitemptypasswords":
			if v == "yes" {
				add(&warns, "permit-empty-passwords", SSHDWarn, d)
			}
		case "passwordauthentication":
			if v == "yes" {
				add(&infos, "password-authentication", SSHDInfo, d)
			}
		case "maxsessions":
			// LiteDeck's own budget. It opens one SFTP channel, up to five for
			// terminals and log tails, and three for commands — nine against a
			// default of ten. Below that, tabs start failing to open and the
			// cause is on the server, not in the app.
			if n, err := strconv.Atoi(d.Value); err == nil && n < 10 {
				add(&warns, "max-sessions-low", SSHDWarn, d)
			}
		case "port":
			add(&infos, "port", SSHDInfo, d)
		case "x11forwarding":
			if v == "yes" {
				add(&infos, "x11-forwarding", SSHDInfo, d)
			}
		case "allowusers", "allowgroups", "denyusers", "denygroups":
			add(&infos, "access-list", SSHDInfo, d)
		}
	}
	return append(warns, infos...)
}
