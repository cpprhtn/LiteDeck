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

// selfPIDs returns the sshd processes this connection is running underneath.
func (a *App) selfPIDs(ctx context.Context, hostID string) (map[int]bool, error) {
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
	if r, err := conn.Probe(ctx, "ss", adapter.SSArgs22()...); err == nil && r.OK() {
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
		idle, what := adapter.ParseWIdle(r.Stdout)
		for i := range sessions {
			if sessions[i].TTY == "" {
				continue
			}
			sessions[i].Idle = idle[sessions[i].TTY]
			sessions[i].What = what[sessions[i].TTY]
		}
	}
	return sessions, nil
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
