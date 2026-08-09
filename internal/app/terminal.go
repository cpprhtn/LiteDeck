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
	for _, t := range doomed {
		_ = t.sess.Close()
	}
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
	for _, s := range sessions {
		_ = s.Close()
	}
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
		return RevealRequest{Error: "이 터미널은 더 이상 열려 있지 않습니다"}
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
	out.Error = fmt.Sprintf("%s 를 찾을 수 없습니다", cleaned)
	return out
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
