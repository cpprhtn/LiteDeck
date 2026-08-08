package app

import (
	"context"
	"fmt"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The network view (v1.x): interfaces and listening sockets.

// NetworkView is what the tab renders.
type NetworkView struct {
	Interfaces []adapter.Interface `json:"interfaces"`
	Listeners  []adapter.Listener  `json:"listeners"`
	// Warnings records what could not be collected — `ip` and `ss` are absent
	// on minimal images, and a half-empty tab with no explanation reads as a
	// bug rather than a missing package.
	Warnings []string `json:"warnings"`
}

// HostNetwork collects interfaces and listening sockets.
//
// The two commands are independent, so one missing tool does not empty the
// whole tab — each failure becomes a warning and the other half still renders.
func (a *App) HostNetwork(hostID string) (NetworkView, error) {
	// Gated on the capability: the network view was the one that had no check at
	// all, so opening the tab on Windows ran `ip -j addr` and `ss -tulnp` against
	// cmd.exe on a timer and filled the log with console-codepage errors.
	info, err := a.requireCapability(hostID, adapter.CapNetwork, "네트워크 정보")
	if err != nil {
		return NetworkView{}, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return NetworkView{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	out := NetworkView{
		Interfaces: []adapter.Interface{},
		Listeners:  []adapter.Listener{},
		Warnings:   []string{},
	}

	if info.Platform == adapter.PlatformWindows {
		// One round trip for all five tables. The Linux path issues two commands
		// because ip and ss are separate binaries; here everything is one
		// PowerShell session, and the tab refreshes on a timer.
		raw, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll, adapter.WindowsNetworkScript())
		if err != nil {
			return NetworkView{}, err
		}
		ifaces, listeners, warnings, err := adapter.ParseWindowsNetwork(raw)
		if err != nil {
			return NetworkView{}, err
		}
		out.Interfaces = ifaces
		out.Listeners = listeners
		out.Warnings = append(out.Warnings, warnings...)
		return out, nil
	}

	if res, err := conn.Poll(ctx, "ip", adapter.IPAddrArgs()...); err != nil {
		out.Warnings = append(out.Warnings, "ip addr: "+err.Error())
	} else if !res.OK() {
		out.Warnings = append(out.Warnings,
			"`ip` 명령을 실행하지 못했습니다 — iproute2가 설치되어 있지 않을 수 있습니다")
	} else if ifaces, perr := adapter.ParseInterfaces(res.Stdout); perr != nil {
		out.Warnings = append(out.Warnings, perr.Error())
	} else {
		out.Interfaces = ifaces
	}

	if res, err := conn.Poll(ctx, "ss", adapter.SSArgs()...); err != nil {
		out.Warnings = append(out.Warnings, "ss: "+err.Error())
	} else if !res.OK() && len(res.Stdout) == 0 {
		out.Warnings = append(out.Warnings,
			"`ss` 명령을 실행하지 못했습니다 — iproute2가 설치되어 있지 않을 수 있습니다")
	} else if ls, perr := adapter.ParseListeners(res.Stdout); perr != nil {
		out.Warnings = append(out.Warnings, perr.Error())
	} else {
		out.Listeners = ls
		// Without privileges ss omits other users' process names rather than
		// failing. Saying so beats leaving a column mysteriously blank.
		named := 0
		for _, l := range ls {
			if l.Process != "" {
				named++
			}
		}
		if len(ls) > 0 && named == 0 {
			out.Warnings = append(out.Warnings,
				"프로세스 이름을 볼 수 없습니다 — 다른 사용자의 소켓은 관리자 권한이 필요합니다")
		}
	}

	if len(out.Interfaces) == 0 && len(out.Listeners) == 0 && len(out.Warnings) > 0 {
		return out, fmt.Errorf("app: %s", out.Warnings[0])
	}
	return out, nil
}
