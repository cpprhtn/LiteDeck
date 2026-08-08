package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"sync"
	"time"

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

type terminalRegistry struct {
	app *App
	mu  sync.Mutex
	seq int
	all map[string]*sshcore.PTYSession
}

func newTerminalRegistry(a *App) *terminalRegistry {
	return &terminalRegistry{app: a, all: make(map[string]*sshcore.PTYSession)}
}

func (r *terminalRegistry) get(id string) (*sshcore.PTYSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.all[id]
	return s, ok
}

func (r *terminalRegistry) drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.all, id)
}

// closeAll ends every terminal, used on shutdown.
func (r *terminalRegistry) closeAll() {
	r.mu.Lock()
	sessions := make([]*sshcore.PTYSession, 0, len(r.all))
	for _, s := range r.all {
		sessions = append(sessions, s)
	}
	r.all = make(map[string]*sshcore.PTYSession)
	r.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
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
	id := "term" + strconv.Itoa(a.terminals.seq)
	a.terminals.mu.Unlock()

	ptyOpts := sshcore.PTYOptions{
		Cols:       opts.Cols,
		Rows:       opts.Rows,
		InitialDir: opts.Dir,
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

	a.terminals.mu.Lock()
	a.terminals.all[id] = sess
	a.terminals.mu.Unlock()

	return TerminalInfo{ID: id, HostID: hostID, Title: title}, nil
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

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
