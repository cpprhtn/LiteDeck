package app

import (
	"io"
	"sort"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/pkg/sftp"
)

// The sshd configuration review (§4.4).
//
// Read over SFTP, not run as a command. `sshd -T` prints the effective
// configuration with every default filled in, which is strictly more
// informative — and needs root, which would put a password prompt in front of a
// read-only view. The files are world-readable everywhere they ship, so this
// costs one open and one read per file and never touches a shell.
//
// What it gives up is stated in the UI rather than papered over: this reports
// what the files declare, and says nothing about the compiled-in defaults that
// apply wherever they are silent.

// sshdConfigPath is where every distribution that ships OpenSSH puts it.
const sshdConfigPath = "/etc/ssh/sshd_config"

// maxSSHDConfigBytes caps one file. A config is a few kilobytes; anything
// approaching this is not a config, and reading it would be the app's fault.
const maxSSHDConfigBytes = 1 << 20

// maxSSHDIncludeDepth stops an Include loop. sshd itself refuses to nest
// deeper than a handful, and a file that includes its own directory would
// otherwise read forever.
const maxSSHDIncludeDepth = 8

// SSHDConfig reads the server's sshd configuration and reports what it declares.
func (a *App) SSHDConfig(hostID string) (adapter.SSHDReport, error) {
	// Not gated on a capability: this needs SFTP and nothing else, so it works
	// on a host no adapter can drive — which is exactly the host whose sshd
	// settings you most want to look at.
	info, err := a.requireAdapter(hostID)
	if err != nil {
		return adapter.SSHDReport{}, err
	}
	if info.Platform == adapter.PlatformWindows {
		// Windows OpenSSH keeps sshd_config under ProgramData and the path is
		// not fixed. Rather than guess, say so.
		return adapter.SSHDReport{}, i18n.Errorf("Windows 서버의 sshd 설정은 아직 읽지 않습니다")
	}

	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return adapter.SSHDReport{}, err
	}

	var (
		files      []string
		directives []adapter.SSHDDirective
		unreadable []string
		read       = map[string]bool{}
	)

	// Depth-first in the order sshd reads: a file's Include is expanded where
	// the Include line sits, because the first value seen wins and Ubuntu's
	// Include is deliberately at the top.
	var visit func(path string, depth int)
	visit = func(path string, depth int) {
		if read[path] || depth > maxSSHDIncludeDepth {
			return
		}
		read[path] = true

		data, err := readSFTPFile(client, path, maxSSHDConfigBytes)
		if err != nil {
			unreadable = append(unreadable, path)
			return
		}
		files = append(files, path)

		// Expanded where the Include line sits, not after the whole file: the
		// first value sshd sees wins, so a drop-in included on line 12 beats
		// the same keyword set on line 60 of the parent.
		parsed, includes := adapter.ParseSSHDConfig(path, data)
		at := 0
		for _, inc := range includes {
			directives = append(directives, parsed[at:inc.After]...)
			at = inc.After
			for _, pattern := range inc.Patterns {
				for _, match := range globSorted(client, pattern) {
					visit(match, depth+1)
				}
			}
		}
		directives = append(directives, parsed[at:]...)
	}
	visit(sshdConfigPath, 0)

	if len(files) == 0 {
		return adapter.SSHDReport{}, i18n.Errorf("%s 를 읽지 못했습니다", sshdConfigPath)
	}
	return adapter.BuildSSHDReport(files, directives, unreadable), nil
}

// globSorted expands an Include pattern. sshd reads matches in lexical order,
// which is the whole point of naming drop-ins 10-foo.conf and 50-bar.conf.
func globSorted(client *sftp.Client, pattern string) []string {
	matches, err := client.Glob(pattern)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

func readSFTPFile(client *sftp.Client, path string, limit int64) ([]byte, error) {
	f, err := client.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}
