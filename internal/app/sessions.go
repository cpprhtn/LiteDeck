package app

// The SSH session view: who is on this server, and cutting them off.
//
// The whole feature turns on one guard. Ending a session is ordinary
// administration until the session is the one LiteDeck is holding, at which point
// it disconnects the person doing it — and on a machine reached only over SSH,
// possibly for good. So "which processes would take us with them" is computed
// from the server rather than assumed, and refused in Go rather than greyed out
// in the UI.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// selfCache remembers each connection's own sshd ancestors.
//
// Cached per host because the answer cannot change while the connection lives:
// it is the chain of processes sshd forked for this TCP connection. Re-probing on
// every refresh would be a round trip for a constant.
type selfCache struct {
	mu   sync.Mutex
	byID map[string]map[int]bool
}

func newSelfCache() *selfCache { return &selfCache{byID: map[string]map[int]bool{}} }

func (c *selfCache) get(id string) (map[int]bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byID[id]
	return v, ok
}

func (c *selfCache) put(id string, pids map[int]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = pids
}

func (c *selfCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
}

// genCache records the connection generation each host's caches were filled
// from, so freshen can tell a live cache from one left over by a reconnect.
type genCache struct {
	mu   sync.Mutex
	byID map[string]uint64
}

func newGenCache() *genCache { return &genCache{byID: map[string]uint64{}} }

// seen reports whether gen is the generation already recorded, recording it
// when it is not. False means the connection has been replaced.
func (c *genCache) seen(id string, gen uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID[id] == gen {
		return true
	}
	c.byID[id] = gen
	return false
}

// portCache remembers each connection's server-side SSH port, for the same
// reason selfCache exists: it cannot change while the connection lives.
type portCache struct {
	mu   sync.Mutex
	byID map[string]int
}

func newPortCache() *portCache { return &portCache{byID: map[string]int{}} }

func (c *portCache) get(id string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byID[id]
	return v, ok
}

func (c *portCache) put(id string, port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = port
}

func (c *portCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
}

// freshen drops everything cached about a host's connection once that
// connection has been replaced.
//
// The caches below are all keyed by host ID and all hold facts read from the
// server: which sshd processes are ours, which port it answers on, what the OS
// supports, where the CPU counters were last time. Every one of those is a
// property of a connection, not of a host, and reconnect replaces connections
// without telling anybody — a network blip or a server reboot is enough.
//
// Before this, the only thing that cleared them was the user pressing
// Disconnect. So a host that dropped and came back kept answering with the old
// connection's process IDs, and EndSSHSession's "this one is ours, refuse it"
// guard was checking a set that no longer contained the connection it was
// protecting. The guard the whole file exists for would have let you cut your
// own line, and after a reboot it would also have refused an innocent session
// whose PID had been recycled into the stale set.
func (a *App) freshen(hostID string) {
	gen := a.mgr.Generation(hostID)
	if gen == 0 {
		return // never connected; nothing was cached through a connection
	}
	if a.gens.seen(hostID, gen) {
		return
	}
	a.selves.forget(hostID)
	a.sshPorts.forget(hostID)
	a.detected.forget(hostID)
	a.cpu.forget(hostID)
}

// selfPIDs returns the sshd processes this connection is running underneath.
func (a *App) selfPIDs(ctx context.Context, hostID string) (map[int]bool, error) {
	a.freshen(hostID)
	if v, ok := a.selves.get(hostID); ok {
		return v, nil
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}
	// A compile-time constant script with nothing interpolated — the same
	// exception the metrics script takes to the argv-only rule.
	res, err := conn.Probe(ctx, "sh", "-c", adapter.SelfAncestorsScript)
	if err != nil {
		return nil, err
	}
	pids := adapter.ParseSelfAncestors(res.Stdout)
	if len(pids) == 0 {
		// Refusing to continue rather than guessing. Without this set every
		// session looks safe to end, including ours, and the first thing the user
		// clicks could be the connection they are clicking with.
		return nil, errors.New(
			i18n.S("이 서버에서 자기 세션을 식별하지 못했습니다 — 안전을 확인할 수 없어 세션 종료를 막습니다"))
	}
	a.selves.put(hostID, pids)
	return pids, nil
}

// ListSSHSessions returns the logins on this server.
func (a *App) ListSSHSessions(hostID string) ([]adapter.SSHSession, error) {
	info, err := a.requireCapability(hostID, adapter.CapSessions, i18n.S("SSH 세션 목록"))
	if err != nil {
		return nil, err
	}
	_ = info

	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	self, err := a.selfPIDs(ctx, hostID)
	if err != nil {
		return nil, err
	}

	res, err := conn.Poll(ctx, "ps", adapter.SessionPSArgs()...)
	if err != nil {
		return nil, err
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	sessions, err := adapter.ParseSSHSessions(res.Stdout, self)
	if err != nil {
		return nil, err
	}

	// Everything below is enrichment. Each source is optional and each failure is
	// a blank column, because all three of them come back empty on a perfectly
	// healthy container while ps lists every session.
	//
	// Most precise first, and later sources only fill what is still blank.
	if r, err := conn.Probe(ctx, "ss", adapter.SSArgsForPort(a.sshPort(ctx, hostID))...); err == nil && r.OK() {
		peers := adapter.ParseSSHPeers(r.Stdout)
		for i := range sessions {
			if p, ok := peers[sessions[i].PID]; ok {
				sessions[i].From = p
			} else if p, ok := peers[sessions[i].PPID]; ok {
				sessions[i].From = p
			}
		}
	}
	if r, err := conn.Probe(ctx, "w", "-h"); err == nil && r.OK() {
		from, idle, what := adapter.ParseWIdle(r.Stdout)
		for i := range sessions {
			if sessions[i].TTY == "" {
				continue
			}
			if sessions[i].From == "" {
				sessions[i].From = from[sessions[i].TTY]
			}
			sessions[i].Idle = idle[sessions[i].TTY]
			sessions[i].What = what[sessions[i].TTY]
		}
	}
	// Only worth asking when something is still missing. `who` answers a strict
	// subset of what `w` does, so a run where `w` spoke has nothing to gain here.
	if missingOrigins(sessions) {
		if r, err := conn.Probe(ctx, "who", adapter.WhoArgs()...); err == nil && r.OK() {
			from := adapter.ParseWho(r.Stdout)
			for i := range sessions {
				if sessions[i].TTY != "" && sessions[i].From == "" {
					sessions[i].From = from[sessions[i].TTY]
				}
			}
		}
	}
	return sessions, nil
}

// missingOrigins reports whether any interactive login still has a blank origin.
func missingOrigins(ss []adapter.SSHSession) bool {
	for _, s := range ss {
		if s.TTY != "" && s.From == "" {
			return true
		}
	}
	return false
}

// sshPort is the port this connection's sshd is listening on, asked of the
// server and cached for the connection's life.
//
// Deliberately not the configured port. That is the port LiteDeck dialled, which
// is a different number the moment anything forwards — a published container
// port, a router, a jump host. Zero on failure, which SSArgsForPort reads as the
// default; a filter on the wrong port silently matches nothing.
func (a *App) sshPort(ctx context.Context, hostID string) int {
	if p, ok := a.sshPorts.get(hostID); ok {
		return p
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return 0
	}
	r, err := conn.Probe(ctx, "sh", "-c", adapter.ServerPortScript)
	if err != nil || !r.OK() {
		return 0
	}
	p := adapter.ParseServerPort(r.Stdout)
	if p > 0 {
		a.sshPorts.put(hostID, p)
	}
	return p
}

// EndSSHSession disconnects one login.
//
// Four things are refused, and all four are refused here rather than in the view:
//
//   - the connection LiteDeck is using, computed from the server's own process
//     tree, because ending it disconnects the person pressing the button
//   - the listening daemon and the privileged halves, because stopping sshd cuts
//     off everybody and locks the machine if there is no console
//   - PID 1, for the reason it is refused everywhere else
//   - a PID that is not an SSH session at all, so a stale list cannot be used to
//     signal an arbitrary process
func (a *App) EndSSHSession(hostID string, pid int) ActionResult {
	if pid <= 1 {
		return failResult(fmt.Errorf("app: invalid pid %d", pid))
	}
	if _, err := a.requireCapability(hostID, adapter.CapSessions, i18n.S("SSH 세션 목록")); err != nil {
		return failResult(err)
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return failResult(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout+pollTimeout)
	defer cancel()

	self, err := a.selfPIDs(ctx, hostID)
	if err != nil {
		return failResult(err)
	}

	// Re-read the process table rather than trusting the list the UI is showing.
	// PIDs are reused, and a row that was a login when the view last refreshed may
	// be something else by the time it is clicked.
	res, err := conn.Poll(ctx, "ps", adapter.SessionPSArgs()...)
	if err != nil {
		return failResult(err)
	}
	if err := res.Err(); err != nil {
		return failResult(err)
	}

	if adapter.SessionListenerPIDs(res.Stdout)[pid] {
		return failResult(errors.New(
			i18n.T("PID %d는 sshd 데몬 또는 연결의 관리 프로세스입니다 — 종료하면 이 서버의 모든 SSH 접속이 끊깁니다", pid)))
	}

	sessions, err := adapter.ParseSSHSessions(res.Stdout, self)
	if err != nil {
		return failResult(err)
	}
	var target *adapter.SSHSession
	for i := range sessions {
		if sessions[i].PID == pid {
			target = &sessions[i]
			break
		}
	}
	if target == nil {
		return failResult(errors.New(
			i18n.T("PID %d는 더 이상 SSH 세션이 아닙니다 — 목록을 새로고침하세요", pid)))
	}
	if target.Self || self[pid] {
		return failResult(errors.New(
			i18n.S("지금 LiteDeck이 쓰고 있는 접속입니다 — 종료하면 이 서버와의 연결이 끊깁니다")))
	}

	out, err := conn.Exec(ctx, "kill", adapter.KillSessionArgs(pid)...)
	if err != nil {
		return failResult(err)
	}
	return a.classify(hostID, out, false)
}
