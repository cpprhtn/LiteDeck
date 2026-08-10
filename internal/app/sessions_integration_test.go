package app

import (
	"strings"
	"testing"
	"time"
)

// TestSSHSessionsLive runs against the fixture with a real login open.
//
// The parser has golden-file tests; what only a live server can show is whether
// the self-identification actually matches the connection LiteDeck is holding.
// That is the one property this feature cannot ship without, and it depends on
// the process tree of a real sshd rather than on any text this project controls.
func TestSSHSessionsLive(t *testing.T) {
	a, host := connectedApp(t), "fixture"

	sessions, err := a.ListSSHSessions(host)
	if err != nil {
		t.Fatalf("ListSSHSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("no sessions; LiteDeck's own connection should be visible at minimum")
	}

	// LiteDeck runs each command in its own session process, so at least one row
	// belongs to us. If none does, the ancestor probe is not matching and every
	// session in the list looks safe to end — including the one being used to end
	// it.
	var selves int
	for _, s := range sessions {
		if s.Self {
			selves++
		}
	}
	t.Logf("sessions=%d self=%d", len(sessions), selves)
	if selves == 0 {
		t.Fatalf("no session marked as ours out of %d; the self guard is not working",
			len(sessions))
	}

	// Ending one of our own must be refused, and the refusal has to come from Go
	// rather than from the view.
	for _, s := range sessions {
		if !s.Self {
			continue
		}
		res := a.EndSSHSession(host, s.PID)
		if res.OK {
			t.Fatalf("EndSSHSession accepted our own session (pid %d) — "+
				"running it would disconnect the client", s.PID)
		}
		t.Logf("refused own session: %s", res.Error)
		break
	}
}

// TestEndSSHSessionRefusesDaemon covers the other way to lock yourself out:
// signalling sshd itself rather than one login. It stops the service for
// everybody, and on a box with no console that is unrecoverable.
func TestEndSSHSessionRefusesDaemon(t *testing.T) {
	a, host := connectedApp(t), "fixture"

	conn, err := a.mgr.Conn(host)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	res, err := conn.Exec(testCtx(t), "ps", "-eo", "pid,ppid,user:32,etimes,args", "--no-headers")
	if err != nil {
		t.Fatalf("ps: %v", err)
	}

	var daemon, priv int
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if strings.Contains(line, "[listener]") && daemon == 0 {
			daemon = atoiSimple(fields[0])
		}
		if strings.Contains(line, "[priv]") && priv == 0 {
			priv = atoiSimple(fields[0])
		}
	}
	if daemon == 0 {
		t.Skip("no listener process in ps output")
	}

	if out := a.EndSSHSession(host, daemon); out.OK {
		t.Errorf("accepted the sshd daemon (pid %d) as a target", daemon)
	} else {
		t.Logf("refused daemon: %s", out.Error)
	}
	if priv != 0 {
		if out := a.EndSSHSession(host, priv); out.OK {
			t.Errorf("accepted a connection's privileged process (pid %d)", priv)
		}
	}
	// And PID 1, for the same reason it is refused everywhere else.
	if r := a.EndSSHSession(host, 1); r.OK {
		t.Error("accepted PID 1")
	}
}

// TestEndSSHSessionKillsAnotherConnection is the positive case: a login that is
// not ours really does go away.
//
// Without it every guard above could pass simply because nothing is ever
// killable, which is the shape a vacuous test takes here.
//
// The second login is a second LiteDeck connection to the same server. That is
// exactly the case the guard has to get right — same host, same account, same
// binary — and the one where confusing the two would be most damaging.
func TestEndSSHSessionKillsAnotherConnection(t *testing.T) {
	a, rec := liveApp(t)
	stop := autoAnswer(t, a, rec, "always", true)
	t.Cleanup(stop)
	if err := a.ConnectHost("fixture"); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}

	// A second entry pointing at the same server, so the manager opens a separate
	// TCP connection with its own sshd processes.
	hosts := a.ListHosts()
	if len(hosts) == 0 {
		t.Fatal("no hosts")
	}
	second := hosts[0].Host
	second.ID = "fixture2"
	second.Name = "second connection"
	if err := a.hosts.Upsert(second); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := a.ConnectHost("fixture2"); err != nil {
		t.Fatalf("ConnectHost(fixture2): %v", err)
	}
	// Give it a session process to be seen by: a command in flight.
	if _, err := a.ListProcesses("fixture2", false); err != nil {
		t.Fatalf("warm up second connection: %v", err)
	}

	var target int
	for i := 0; i < 40 && target == 0; i++ {
		sessions, err := a.ListSSHSessions("fixture")
		if err != nil {
			t.Fatalf("ListSSHSessions: %v", err)
		}
		for _, s := range sessions {
			if !s.Self {
				target = s.PID
				break
			}
		}
		if target == 0 {
			// Keep the other connection busy so it has a live session process.
			_, _ = a.ListProcesses("fixture2", false)
			sleepShort()
		}
	}
	if target == 0 {
		t.Fatal("the second connection never appeared as a non-self session — " +
			"either the listing misses it or the self check is over-matching")
	}

	if res := a.EndSSHSession("fixture", target); !res.OK {
		t.Fatalf("EndSSHSession(%d) failed: %s", target, res.Error)
	}

	// It has to actually be gone, not merely reported as ended.
	for i := 0; i < 40; i++ {
		sessions, err := a.ListSSHSessions("fixture")
		if err != nil {
			t.Fatalf("ListSSHSessions after: %v", err)
		}
		var still bool
		for _, s := range sessions {
			if s.PID == target {
				still = true
			}
		}
		if !still {
			t.Logf("ended pid %d, and our own connection is still usable", target)
			// The whole point: ending someone else must not have taken us with
			// them.
			if _, err := a.ListSSHSessions("fixture"); err != nil {
				t.Fatalf("our own connection died: %v", err)
			}
			return
		}
		sleepShort()
	}
	t.Errorf("pid %d is still listed after being ended", target)
}

func atoiSimple(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func sleepShort() { time.Sleep(300 * time.Millisecond) }
