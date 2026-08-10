package sshcore

import (
	"bytes"
	"regexp"
)

// Asking a live terminal where it is (§4.6a).
//
// `code .` and `vi foo.cpp` are handled by the app, not by the server: the
// keystrokes are caught before they are sent, so no command runs remotely and
// nothing has to exist there. That is what makes the feature work on a Windows
// box with neither VS Code nor vi on it, and on any shell at all.
//
// It leaves exactly one thing the client cannot know. `.` and `src/main.go` are
// relative to wherever that shell is standing, and only the shell knows. So it
// is asked — once, at the moment somebody types such a path, and never at
// session start. Nothing is defined, exported or installed; a shell that is
// never asked is never touched.
//
// An earlier design defined functions in the session instead. It broke a
// Windows terminal outright (cmd.exe cannot read POSIX) and an Alpine one
// (`export -f` aborts dash), and it could not work on zsh at all. Asking a
// question is portable in a way that installing a vocabulary is not.

// cwdProbe is the question, per shell family. The reply is swallowed before it
// reaches the screen, so the user sees their prompt and nothing else.
type cwdProbe struct {
	command  string
	response *regexp.Regexp
}

var (
	// POSIX. $PWD needs no subshell and no external binary, so this works on
	// bash, zsh, ash and dash alike.
	posixCWD = cwdProbe{
		command:  `printf 'LDCWD:%s:END\n' "$PWD"`,
		response: regexp.MustCompile(`LDCWD:(.+?):END`),
	}
	// cmd.exe. `%cd%` is the shell's own idea of where it is.
	cmdCWD = cwdProbe{
		command:  `echo LDCWD:%cd%:END`,
		response: regexp.MustCompile(`LDCWD:(.+?):END`),
	}
)

// parseCWD pulls the answer out of everything the shell said.
//
// The echo of the question contains the marker too — `%s` on POSIX, `%cd%` on
// cmd — so the *last* match is the answer and the first is the question coming
// back. Matching the last one is simpler than trying to make the question
// unrecognisable, and it does not depend on how the shell echoes.
func parseCWD(out []byte, p cwdProbe) string {
	all := p.response.FindAllSubmatch(out, -1)
	for i := len(all) - 1; i >= 0; i-- {
		got := bytes.TrimSpace(all[i][1])
		// The echoed question still holds its own placeholder.
		if bytes.ContainsAny(got, "%") {
			continue
		}
		if len(got) > 0 {
			return string(got)
		}
	}
	return ""
}
