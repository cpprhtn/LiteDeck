package app

import (
	"context"
	"strings"

	"github.com/pkg/sftp"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// The shell's own history file (T-23, 명령 이력 C-1).
//
// The third and last source, and the one people actually reach for. sudo's
// journal covers what was privileged; this app's terminal covers what was typed
// through it; this covers everything else — which on most servers is most of it.
//
// # Why this one has a switch of its own
//
// A shell history is the densest credential file on a server. `mysql -pHUNTER2`,
// `export AWS_SECRET_ACCESS_KEY=…`, `curl -H "Authorization: Bearer …"`. Right
// now nothing leaks because nothing reads it; a feature that reads it every time
// somebody opens a tab changes that, and in server mode one stolen login would
// be worth every password ever typed on the box.
//
// So it is off until switched on, per host. The gate is here rather than in the
// UI on purpose: /rpc reaches every binding directly, so a hidden tab is a door
// left open with the handle taken off.

// ShellHistoryView is what the history pane gets for this source.
type ShellHistoryView struct {
	// Allowed is false until the user turns this on for the host. Then nothing
	// was read, and the pane offers the switch rather than showing an empty list
	// that looks like a server nobody has ever worked on.
	Allowed  bool                   `json:"allowed"`
	Commands []adapter.ShellCommand `json:"commands"`
	// File is which history was read, so the screen can name its source.
	File string `json:"file,omitempty"`
	// Root reports that root's history was included, which only happens when
	// the user asked for it and sudo agreed.
	Root bool `json:"root,omitempty"`
	// Timed is false where the file carries no times at all — the bash default.
	// The pane must then not draw ages it does not have.
	Timed bool `json:"timed"`
	// Secrets counts commands that looked like they carried a credential.
	Secrets int `json:"secrets"`
	// Checked reports that the paths were settled against the server rather
	// than by arithmetic alone. False on a host where nothing could be asked.
	Checked bool `json:"checked"`
}

// dirCheck answers "is this directory really there", once per path.
//
// The replay needs it to tell a real subdirectory from a session seam, and it
// asks about the same handful of directories over and over — a 2,000-line
// history walks maybe a hundred distinct places. Memoised, that is a hundred
// SFTP stats for the whole read; unmemoised it would be one per `cd`.
type dirCheck struct {
	client *sftp.Client
	seen   map[string]bool
	asked  int
}

func newDirCheck(client *sftp.Client) *dirCheck {
	return &dirCheck{client: client, seen: map[string]bool{}}
}

func (d *dirCheck) exists(p string) bool {
	if p == "" {
		return false
	}
	if got, ok := d.seen[p]; ok {
		return got
	}
	d.asked++
	fi, err := d.client.Stat(p)
	ok := err == nil && fi.IsDir()
	d.seen[p] = ok
	return ok
}

// shellHistoryMax bounds how much of the file is turned into rows.
//
// The measured file was 2,000 lines, which is bash's default HISTFILESIZE and
// therefore the common ceiling. Reading all of it is fine; the cap is on what
// crosses to the UI.
const shellHistoryMax = 2000

// rootHome is where /root/.bash_history's shell was standing when it started.
//
// Not read from the server: the file being at /root/.bash_history is what says
// whose it is, and a login shell for root starts in root's home on every
// distribution that puts the file there.
const rootHome = "/root"

// HostShellHistory reads the shell history for a host.
//
// elevate additionally reads root's history through sudo. That is a separate
// ask, not a fallback: a root history holds what somebody did after `sudo -i`,
// which is exactly the part the sudo journal cannot see — and exactly the part
// nobody should read by accident.
func (a *App) HostShellHistory(hostID string, elevate bool) (ShellHistoryView, error) {
	if _, err := a.requireCapability(hostID, adapter.CapSessions, i18n.S("셸 이력")); err != nil {
		return ShellHistoryView{}, err
	}
	view := ShellHistoryView{Commands: []adapter.ShellCommand{}}
	if !a.shellHistoryAllowed(hostID) {
		// Refused here, not hidden in the UI. This binding is reachable over
		// /rpc without going through any screen.
		return view, nil
	}
	view.Allowed = true

	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return ShellHistoryView{}, err
	}
	home, err := a.HomeDir(hostID)
	if err != nil {
		// Not fatal. Without it the replay simply has nothing to stand on until
		// an absolute `cd`, which is worse but honest.
		home = ""
	}

	var cmds []adapter.ShellCommand
	// bash first because it is the default shell nearly everywhere, and its
	// file is the one that was measured.
	if text, err := readSmallFile(client, home+"/.bash_history"); err == nil && text != "" {
		view.File = "~/.bash_history"
		cmds = adapter.ParseBashHistory(text)
	} else if text, err := readSmallFile(client, home+"/.zsh_history"); err == nil && text != "" {
		view.File = "~/.zsh_history"
		cmds = adapter.ParseZshHistory(text)
	}

	// Replayed here, before root's file is appended. Each file is one shell's
	// own run of moves: walking them as a single stream started root's first
	// relative `cd` from wherever the user's last command left off, and sent
	// root's bare `cd` to the user's home.
	view.Timed = anyTimed(cmds)
	dirs := newDirCheck(client)
	cmds = adapter.ReplayCdChecked(cmds, home, dirs.exists)

	if elevate {
		if root, ok := a.rootHistory(hostID); ok {
			view.Root = true
			// Appended, not merged by time: root's file has no times either, and
			// interleaving two untimed files by guesswork would invent an order
			// that neither of them claims.
			cmds = append(cmds, adapter.ReplayCdChecked(root, rootHome, dirs.exists)...)
		}
	}
	view.Checked = dirs.asked > 0
	if len(cmds) > shellHistoryMax {
		cmds = cmds[len(cmds)-shellHistoryMax:]
	}
	// Masked before it leaves Go, the same way the sudo history is. The raw text
	// does not cross to a browser.
	for i := range cmds {
		masked, found := adapter.MaskSecrets(cmds[i].Command)
		cmds[i].Command = masked
		if found {
			view.Secrets++
		}
	}
	// Newest last in the file; the pane wants newest first.
	for i, j := 0, len(cmds)-1; i < j; i, j = i+1, j-1 {
		cmds[i], cmds[j] = cmds[j], cmds[i]
	}
	view.Commands = cmds
	return view, nil
}

// rootHistory reads /root/.bash_history through sudo.
//
// SFTP has no sudo, so this is the one part that needs a command. It is a plain
// read and nothing else — `cat`, not a shell — so the elevated path stays as
// small as it can be.
func (a *App) rootHistory(hostID string) ([]adapter.ShellCommand, bool) {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	res, err := a.execMaybeElevated(ctx, conn, hostID, true, "cat", "/root/.bash_history")
	if err != nil || !res.OK() {
		// A refused sudo is a normal answer here. The user's own history was
		// read either way, and saying nothing beats an error over a file they
		// may simply not be allowed to see.
		return nil, false
	}
	text := string(res.Stdout)
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	return adapter.ParseBashHistory(text), true
}

// shellHistoryAllowed reports whether the user turned this on for the host.
func (a *App) shellHistoryAllowed(hostID string) bool {
	if a.settings == nil {
		return false
	}
	return a.settings.Get().ShellHistory[hostID]
}

// SetShellHistoryAllowed turns the shell history on or off for one host.
func (a *App) SetShellHistoryAllowed(hostID string, allowed bool) error {
	if a.settings == nil {
		return nil
	}
	return a.settings.SetShellHistory(hostID, allowed)
}

// anyTimed reports whether the file carried timestamps at all.
//
// The nil check is the point. ShellCommand.At is a pointer because an unknown
// time has to be absent on the wire, and calling a time.Time method through a
// nil pointer panics — which is what `!c.At.IsZero()` did here, on every
// history that has no timestamps, which is most of them.
func anyTimed(cmds []adapter.ShellCommand) bool {
	for _, c := range cmds {
		if c.At != nil {
			return true
		}
	}
	return false
}
