package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The built-in terminal (§4.6).
//
// Terminal output is binary — escape sequences, partial UTF-8 at chunk
// boundaries, whatever a program decides to print. It is base64-encoded across
// the Wails boundary rather than sent as a string, because JSON string encoding
// would mangle lone surrogates and invalid sequences into replacement
// characters and the display would be subtly wrong forever after.

// TerminalInfo identifies an open terminal to the frontend.
type TerminalInfo struct {
	ID     string `json:"id"`
	HostID string `json:"hostId"`
	Title  string `json:"title"`
	// Seq orders recovered tabs the way they were opened.
	Seq int `json:"seq"`
}

// TerminalOptions is what the frontend asks for.
type TerminalOptions struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	// Dir starts the shell there — "터미널에서 열기" from the file explorer.
	Dir string `json:"dir,omitempty"`
	// ContainerID opens a shell inside that container instead of on the host.
	ContainerID string `json:"containerId,omitempty"`
	// ShellID picks which shell to start, from HostShells. Empty means the
	// first one, which is what sshd would have given anyway.
	ShellID string `json:"shellId,omitempty"`
}

// openTerminal is one live session and what the UI needs to show it again.
//
// The info is kept here, not only in the frontend, because the terminal view
// unmounts whenever the user looks at another tab. Go outliving the component
// is what lets the tabs be recovered instead of leaked (§4.6).
type openTerminal struct {
	info TerminalInfo
	sess *sshcore.PTYSession
}

type terminalRegistry struct {
	app *App
	mu  sync.Mutex
	seq int
	all map[string]*openTerminal
}

func newTerminalRegistry(a *App) *terminalRegistry {
	return &terminalRegistry{app: a, all: make(map[string]*openTerminal)}
}

func (r *terminalRegistry) get(id string) (*sshcore.PTYSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.all[id]
	if !ok {
		return nil, false
	}
	return t.sess, true
}

func (r *terminalRegistry) drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.all, id)
}

// list returns the sessions still open on one host, oldest first.
func (r *terminalRegistry) list(hostID string) []TerminalInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TerminalInfo, 0, len(r.all))
	for _, t := range r.all {
		if t.info.HostID == hostID {
			out = append(out, t.info)
		}
	}
	// By the sequence in the ID, so tabs come back in the order they were made
	// rather than in map order, which changes on every call.
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// closeHost ends the terminals belonging to one host.
//
// Dropping the connection kills the sessions on the server either way, but the
// entries would otherwise linger and be offered back to the UI as tabs that
// cannot be typed into.
func (r *terminalRegistry) closeHost(hostID string) {
	r.mu.Lock()
	var doomed []*openTerminal
	for id, t := range r.all {
		if t.info.HostID == hostID {
			doomed = append(doomed, t)
			delete(r.all, id)
		}
	}
	r.mu.Unlock()
	closeTogether(func(yield func(*sshcore.PTYSession)) {
		for _, t := range doomed {
			yield(t.sess)
		}
	})
}

// closeAll ends every terminal, used on shutdown.
func (r *terminalRegistry) closeAll() {
	r.mu.Lock()
	sessions := make([]*sshcore.PTYSession, 0, len(r.all))
	for _, t := range r.all {
		sessions = append(sessions, t.sess)
	}
	r.all = make(map[string]*openTerminal)
	r.mu.Unlock()
	closeTogether(func(yield func(*sshcore.PTYSession)) {
		for _, s := range sessions {
			yield(s)
		}
	})
}

// closeTogether ends every session at once.
//
// Each close gives the remote shell a moment to leave (see PTYSession.Close),
// and four tabs closing one after another would make quitting the app — or
// disconnecting a host — take four of those.
func closeTogether(each func(yield func(*sshcore.PTYSession))) {
	var wg sync.WaitGroup
	each(func(s *sshcore.PTYSession) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Close()
		}()
	})
	wg.Wait()
}

// ListTerminals reports the sessions already open on a host (§4.6).
//
// The terminal view calls this on mount and adopts what it finds. Without it
// the view came back from a tab switch with an empty list, opened another
// terminal, and left the previous one holding a channel nothing could release —
// four round trips and the host had no slots left.
func (a *App) ListTerminals(hostID string) []TerminalInfo {
	return a.terminals.list(hostID)
}

// OpenTerminal starts an interactive session and returns its ID.
//
// Output arrives as term:data:<id> events carrying base64; the session ends
// with term:exit:<id>.
func (a *App) OpenTerminal(hostID string, opts TerminalOptions) (TerminalInfo, error) {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return TerminalInfo{}, err
	}

	a.terminals.mu.Lock()
	a.terminals.seq++
	seq := a.terminals.seq
	id := "term" + strconv.Itoa(seq)
	a.terminals.mu.Unlock()

	ptyOpts := sshcore.PTYOptions{
		Cols:       opts.Cols,
		Rows:       opts.Rows,
		InitialDir: opts.Dir,
		Windows:    a.isWindows(hostID),
	}
	title := hostID
	// Windows hands out whatever sshd's DefaultShell says, and that is usually
	// cmd. Which shell the user asked for decides the whole command line,
	// including how "start in this directory" is spelled — the three shells do
	// not agree on that and there is no line that works in all of them.
	if opts.ContainerID == "" && a.isWindows(hostID) {
		if sh, exact := a.pickShell(hostID, opts.ShellID); sh.ID != "" {
			if err := a.warmWSL(hostID, sh); err != nil {
				return TerminalInfo{}, err
			}
			ptyOpts.Line = adapter.WindowsShellCommand(sh, opts.Dir)
			if exact && sh.ID != "cmd" {
				title = sh.Label
			}
		}
	}
	if opts.ContainerID != "" {
		runtime, err := a.containerRuntime(hostID)
		if err != nil {
			return TerminalInfo{}, err
		}
		// sh, not bash: most images do not have bash, and failing to open a
		// shell because of that would be a poor first impression.
		ptyOpts.Exec = []string{runtime, "exec", "-it", "--", opts.ContainerID, "/bin/sh"}
		ptyOpts.InitialDir = "" // meaningless inside the container's namespace
		title = "container " + shortID(opts.ContainerID)
	} else if opts.Dir != "" {
		title = opts.Dir
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := conn.OpenPTY(ctx, ptyOpts,
		func(chunk []byte) {
			a.emit("term:data:"+id, base64.StdEncoding.EncodeToString(chunk))
		},
		func(err error) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			a.terminals.drop(id)
			a.emit("term:exit:"+id, msg)
		},
	)
	if err != nil {
		return TerminalInfo{}, err
	}

	info := TerminalInfo{ID: id, HostID: hostID, Title: title, Seq: seq}
	a.terminals.mu.Lock()
	a.terminals.all[id] = &openTerminal{info: info, sess: sess}
	a.terminals.mu.Unlock()

	// The typed history tracks where each session is standing, and this is the
	// only moment it can be known for certain. A container shell is left
	// unanchored on purpose: opts.Dir is a path on the host, and inside the
	// container it means something else or nothing at all.
	if a.typed != nil && opts.Dir != "" && opts.ContainerID == "" {
		a.learnHome(hostID)
		a.typed.setCwd(hostID, info.ID, opts.Dir)
	}
	return info, nil
}

// WriteTerminal sends keystrokes. data is base64 for the same reason output is.
func (a *App) WriteTerminal(id, data string) error {
	sess, ok := a.terminals.get(id)
	if !ok {
		return fmt.Errorf("app: terminal %q is not open", id)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("app: terminal input: %w", err)
	}
	_, err = sess.Write(raw)
	return err
}

// ResizeTerminal tells the remote side the window changed.
func (a *App) ResizeTerminal(id string, cols, rows int) error {
	sess, ok := a.terminals.get(id)
	if !ok {
		return fmt.Errorf("app: terminal %q is not open", id)
	}
	return sess.Resize(cols, rows)
}

// CloseTerminal ends a session.
func (a *App) CloseTerminal(id string) error {
	if a.typed != nil {
		// A shell that is gone is not standing anywhere.
		a.typed.forgetTerm(id)
	}
	sess, ok := a.terminals.get(id)
	if !ok {
		return nil // already gone; closing twice is not an error worth raising
	}
	a.terminals.drop(id)
	return sess.Close()
}

// isWindows reports whether this host's shell is cmd.exe rather than a POSIX
// one, which changes how it is asked where it is standing.
//
// A host nobody has identified is treated as POSIX, which is what the SSH world
// mostly is. Guessing wrong here costs one failed question and an error the user
// can read, not a broken terminal — the shell is never handed anything at
// session start any more.
func (a *App) isWindows(hostID string) bool {
	info, ok := a.detected.get(hostID)
	return ok && info.Platform == adapter.PlatformWindows
}

// RevealFromTerminal handles a `code` or `vi` the app caught before it was sent
// (§4.6a).
//
// arg is exactly what followed the command, unresolved: the client does not
// know where that shell is standing, and for a relative path this is where the
// terminal gets asked. An absolute path never needs asking, so the common
// `code /etc/nginx` costs nothing and works even while something is running.
func (a *App) RevealFromTerminal(termID, arg string) RevealRequest {
	a.terminals.mu.Lock()
	t, ok := a.terminals.all[termID]
	a.terminals.mu.Unlock()
	if !ok {
		return RevealRequest{Error: i18n.S("이 터미널은 더 이상 열려 있지 않습니다")}
	}

	target := strings.TrimSpace(arg)
	if target == "" {
		target = "."
	}
	if !isAbsoluteRemote(target, a.isWindows(t.info.HostID)) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cwd, err := t.sess.CurrentDir(ctx)
		if err != nil {
			return RevealRequest{HostID: t.info.HostID, Path: arg, Error: err.Error()}
		}
		target = joinRemote(cwd, target)
	}
	// An absolute path skipped the join above and may still be spelled the way
	// the shell writes it rather than the way SFTP reads it.
	return a.reveal(t.info.HostID, toRemotePath(target))
}

// isAbsoluteRemote answers for the server's world, not this machine's.
// `C:\Users\KTJ` is absolute on a Windows host and a relative filename here.
func isAbsoluteRemote(p string, windows bool) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if !windows {
		return false
	}
	if strings.HasPrefix(p, `\\`) {
		return true // UNC
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// toRemotePath rewrites what a shell reports into the form SFTP uses.
//
// The two disagree on Windows. cmd.exe says `C:\Users\KTJ\Desktop`; the SFTP
// server the same machine runs presents that directory as `/C:/Users/KTJ/
// Desktop`. Same place, two spellings, and only the second one is a path the
// rest of the app can open — everything downstream requires a leading slash.
//
// Idempotent: a path already in SFTP form has no drive letter in second
// position and comes back unchanged.
func toRemotePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' {
		return "/" + p
	}
	return p
}

// joinRemote resolves a relative path against the shell's directory, leaving
// the cleanup to CleanRemotePath so `..` is handled in exactly one place.
func joinRemote(cwd, rel string) string {
	cwd = toRemotePath(cwd)
	if rel == "." || rel == "" {
		return cwd
	}
	rel = strings.ReplaceAll(rel, `\`, "/")
	return strings.TrimSuffix(cwd, "/") + "/" + rel
}

// RevealRequest is a path the terminal asked the GUI to open (§4.6a).
type RevealRequest struct {
	HostID string `json:"hostId"`
	Path   string `json:"path"`
	IsDir  bool   `json:"isDir"`
	// New means the file is not there yet but could be — `vi test.cpp` in a
	// directory that exists. Refusing that would break the oldest idiom the
	// command has; the editor opens empty and the first save creates it.
	New bool `json:"new"`
	// Set when the path could not be inspected; the UI says so rather than
	// navigating somewhere arbitrary.
	Error string `json:"error,omitempty"`
}

// reveal decides what the file view should do with a path the shell sent.
//
// The shell already resolved it to an absolute path, but only the server knows
// whether it is a directory to navigate to or a file to open — and `code`
// against a path that has since been deleted must say so rather than send the
// tree somewhere arbitrary.
func (a *App) reveal(hostID, p string) RevealRequest {
	out := RevealRequest{HostID: hostID, Path: p}
	cleaned, err := CleanRemotePath(p)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Path = cleaned

	st, err := a.StatPath(hostID, cleaned)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if st.Exists {
		out.IsDir = st.IsDir
		return out
	}

	// Not there yet. If the directory it would live in exists, this is somebody
	// creating a file, not a typo.
	parent, err := a.StatPath(hostID, path.Dir(cleaned))
	if err == nil && parent.Exists && parent.IsDir {
		out.New = true
		return out
	}
	out.Error = i18n.T("%s 를 찾을 수 없습니다", cleaned)
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Which shells this host can open (§4.6).
//
// One entry on a POSIX host — the account's login shell, which the user did not
// choose here and should not be asked about. Several on Windows, where sshd
// hands out cmd.exe by default and the person at the keyboard probably wanted
// PowerShell.
//
// Cached per connection: the answer is a property of the machine, and a menu
// that costs a round trip every time it opens is a menu that feels broken.
type shellCache struct {
	mu   sync.Mutex
	byID map[string]shellEntry
}

type shellEntry struct {
	gen    uint64
	shells []adapter.Shell
}

func newShellCache() *shellCache { return &shellCache{byID: map[string]shellEntry{}} }

func (c *shellCache) get(id string, gen uint64) ([]adapter.Shell, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok || e.gen != gen {
		return nil, false
	}
	return e.shells, true
}

func (c *shellCache) put(id string, gen uint64, shells []adapter.Shell) {
	c.mu.Lock()
	c.byID[id] = shellEntry{gen: gen, shells: shells}
	c.mu.Unlock()
}

func (c *shellCache) forget(id string) {
	c.mu.Lock()
	delete(c.byID, id)
	c.mu.Unlock()
}

// HostShells lists what a new terminal on this host could start.
func (a *App) HostShells(hostID string) ([]adapter.Shell, error) {
	if !a.isWindows(hostID) {
		// Nothing to choose. The empty argv is "whatever sshd gives", which on
		// a POSIX host is the login shell the account already has.
		return []adapter.Shell{{ID: "login", Label: i18n.T("로그인 셸")}}, nil
	}
	gen := a.mgr.Generation(hostID)
	if got, ok := a.shells.get(hostID, gen); ok {
		return got, nil
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	raw, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll, adapter.WindowsShellsScript())
	if err != nil {
		// A probe that failed is not a reason to refuse a terminal. cmd is
		// always there; the menu just has one entry until the next connection.
		return adapter.ParseWindowsShells(""), nil
	}
	shells := adapter.ParseWindowsShells(string(raw))
	a.shells.put(hostID, gen, shells)
	return shells, nil
}

// pickShell finds the requested shell, falling back to the first one.
func (a *App) pickShell(hostID, id string) (adapter.Shell, bool) {
	list, err := a.HostShells(hostID)
	if err != nil || len(list) == 0 {
		return adapter.Shell{}, false
	}
	for _, s := range list {
		if s.ID == id {
			return s, true
		}
	}
	return list[0], false
}

// wslWarmTimeout bounds the check below.
//
// Measured on Windows 10 19045 with the virtual machine down: 3.6 seconds to
// answer, and 0.1 once it is up. Eight is more than twice the slow case, and
// the wait is enforced on the server rather than here — see warmWSL.
const wslWarmSeconds = 10

// warmWSL makes sure the distribution can answer before a terminal is attached
// to it.
//
// # Why there is a check at all
//
// A cold WSL2 machine spends about three and a half seconds starting its
// virtual machine, and `wsl.exe` asked for an interactive session on a
// distribution that cannot start does not fail — it hangs, holding LxssManager
// open. The service goes to StopPending and WSL is then broken for every
// program on that machine until it reboots. A terminal that will not open is a
// small problem; a terminal that breaks a system service is not.
//
// # Why it goes through PowerShell and kills its own child
//
// The obvious version — run `wsl.exe -d X -- true` over the Exec channel and
// let the context expire — was worse than nothing. Windows sshd does not kill
// the child when the channel closes, so every timed-out probe left a wsl.exe
// running forever. Caught in the act: two of them, aged one and one and a half
// minutes, both `wsl.exe -d Ubuntu-24.04 -- true`, on a machine whose WSL had
// stopped answering. The check meant to protect WSL was piling onto it.
//
// So the timeout is enforced where the process is. PowerShell starts it, waits,
// and kills it if it does not finish — the probe cannot outlive its own answer.
func (a *App) warmWSL(hostID string, sh adapter.Shell) error {
	distro := strings.TrimPrefix(sh.ID, "wsl:")
	if distro == sh.ID {
		return nil // not a WSL shell
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return err
	}
	// The only bound there is. Measured cold: 3.6 seconds; warm: 0.1. Ten is
	// nearly three times the slow case, and when WSL is wedged no amount of
	// waiting produces an answer.
	ctx, cancel := context.WithTimeout(context.Background(), wslWarmSeconds*time.Second)
	defer cancel()

	out, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll,
		adapter.WSLProbeScript(distro, wslWarmSeconds))
	state := adapter.WSLHung
	if err == nil {
		state = adapter.ParseWSLProbe(string(out))
	}
	switch state {
	case adapter.WSLReady:
		return nil
	case adapter.WSLHung:
		return i18n.Errorf(
			"%s 가 응답하지 않습니다. 서버에서 `wsl --shutdown` 을 실행하거나, 그래도 안 되면 서버를 재시작해야 합니다.",
			distro)
	default:
		return i18n.Errorf("%s 를 시작하지 못했습니다 — 서버에서 `wsl -d %s` 가 되는지 확인해 주세요.", distro, distro)
	}
}
