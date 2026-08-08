package app

import (
	"context"
	"fmt"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The process view (§4.4) and its destructive actions (§7.4).

// ListProcesses returns the process table, optionally ordered as a tree.
func (a *App) ListProcesses(hostID string, asTree bool) ([]adapter.ProcessInfo, error) {
	info, err := a.requireCapability(hostID, adapter.CapProcesses, "프로세스 목록")
	if err != nil {
		return nil, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	var procs []adapter.ProcessInfo
	if info.Platform == adapter.PlatformWindows {
		out, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll, adapter.WindowsProcessScript())
		if err != nil {
			return nil, err
		}
		// The clock is passed in rather than read inside the parser so the
		// elapsed column can be tested against a fixed capture.
		procs, err = adapter.ParseWindowsProcesses(out, time.Now().UnixMilli())
		if err != nil {
			return nil, err
		}
	} else {
		res, err := conn.Poll(ctx, "ps", adapter.PSArgs()...)
		if err != nil {
			return nil, err
		}
		if err := res.Err(); err != nil {
			return nil, err
		}
		procs, err = adapter.ParsePS(res.Stdout)
		if err != nil {
			return nil, err
		}
	}
	if asTree {
		return adapter.Tree(procs), nil
	}
	return procs, nil
}

// killSignals is an allowlist. Restricting the set means a frontend bug cannot
// turn into an arbitrary signal, and keeps the confirmation rules in §7.4
// enumerable rather than open-ended.
var killSignals = map[string]bool{
	"TERM": true, // the polite request
	"KILL": true, // unconditional; the UI double-confirms this one
	"HUP":  true, // reload, for daemons that use it
	"INT":  true,
}

// KillProcess signals a process (§3.4).
//
// The UI sends TERM first and only offers KILL if the process is still there
// afterwards, because TERM lets a program flush its state and KILL does not.
// Nothing here escalates on its own.
func (a *App) KillProcess(hostID string, pid int, signal string, elevate bool) ActionResult {
	if !killSignals[signal] {
		return failResult(fmt.Errorf("app: unsupported signal %q", signal))
	}
	// PID 1 is the init system. Signalling it takes the server down, and no
	// amount of confirmation dialog makes that a thing a file manager should
	// offer (§7.4).
	if pid == 1 {
		return failResult(fmt.Errorf("PID 1은 init 프로세스입니다 — 종료하면 서버가 정지합니다"))
	}
	if pid < 1 {
		return failResult(fmt.Errorf("app: invalid pid %d", pid))
	}
	info, err := a.requireCapability(hostID, adapter.CapProcesses, "프로세스 목록")
	if err != nil {
		return failResult(err)
	}

	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return failResult(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout+pollTimeout)
	defer cancel()

	if info.Platform == adapter.PlatformWindows {
		// There is no signal vocabulary here. Stop-Process asks a windowed
		// process to close and -Force terminates outright, so the UI's TERM
		// becomes the polite form and KILL the forced one. HUP and INT have no
		// counterpart at all and are refused rather than quietly treated as a
		// kill — a reload request that silently terminates the service instead
		// is exactly the kind of surprise this project keeps removing.
		switch signal {
		case "TERM", "KILL":
		default:
			return failResult(fmt.Errorf(
				"Windows에는 %s 시그널이 없습니다 — 종료(TERM)나 강제 종료(KILL)만 가능합니다", signal))
		}
		script, err := adapter.WindowsKillScript(pid, signal == "KILL")
		if err != nil {
			return failResult(err)
		}
		res, err := a.execPowerShell(ctx, conn, sshcore.CommandAction, script)
		return windowsActionResult(res, err)
	}

	// `--` keeps a negative PID from being read as an option — without it,
	// `kill -TERM -1` signals every process the user can reach.
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate,
		"kill", "-"+signal, "--", fmt.Sprint(pid))
	if err != nil {
		return failResult(err)
	}
	return a.classify(hostID, res, elevate)
}

// niceRange is the POSIX range. Lowering the value raises priority and needs
// root, which surfaces through the ordinary elevation path.
const (
	niceMin = -20
	niceMax = 19
)

// Renice changes a process's scheduling priority (§4.4).
func (a *App) Renice(hostID string, pid, nice int, elevate bool) ActionResult {
	if nice < niceMin || nice > niceMax {
		return failResult(fmt.Errorf("app: nice value %d outside %d..%d", nice, niceMin, niceMax))
	}
	if pid < 1 {
		return failResult(fmt.Errorf("app: invalid pid %d", pid))
	}
	info, err := a.requireCapability(hostID, adapter.CapProcesses, "프로세스 목록")
	if err != nil {
		return failResult(err)
	}

	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return failResult(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout+pollTimeout)
	defer cancel()

	if info.Platform == adapter.PlatformWindows {
		script, err := adapter.WindowsPriorityScript(pid, nice)
		if err != nil {
			return failResult(err)
		}
		res, err := a.execPowerShell(ctx, conn, sshcore.CommandAction, script)
		return windowsActionResult(res, err)
	}

	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate,
		"renice", "-n", fmt.Sprint(nice), "-p", fmt.Sprint(pid))
	if err != nil {
		return failResult(err)
	}
	return a.classify(hostID, res, elevate)
}

// ProcessExists reports whether a PID is still present. The UI calls it after a
// TERM to decide whether to offer KILL, rather than assuming either outcome.
func (a *App) ProcessExists(hostID string, pid int) (bool, error) {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	// Signal 0 performs the permission and existence checks without sending
	// anything. A probe: "no such process" is the answer, not a failure.
	res, err := conn.Probe(ctx, "kill", "-0", "--", fmt.Sprint(pid))
	if err != nil {
		return false, err
	}
	return res.OK(), nil
}
