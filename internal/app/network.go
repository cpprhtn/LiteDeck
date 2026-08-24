package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The network view (v1.x): interfaces and listening sockets.

// ifaceTTL is how long an interface listing is reused.
//
// The two halves of this view change at completely different rates, and reading
// both at the faster one is what made the tab cost two Exec channels per tick
// instead of one. Listeners are why it polls at all — a port opens, a service
// restarts, and five seconds later the table says so. Interfaces do not behave
// that way: an address appears when a VPN comes up or a container network is
// created, which is minutes apart, and in between the same answer is re-read
// eleven more times for nothing.
//
// Thirty seconds rather than the life of the connection, because interfaces are
// not immutable the way a config file is — that is the difference between this
// and the sshd section below, which reads once and stops. The refresh button
// bypasses it outright: somebody who just brought up a VPN and pressed it is
// telling us the answer changed.
//
// There are three Exec channels for a whole connection (§3.2a) and the summary
// bar is polling on one of them. Halving a view's share is not a micro-optimisation
// at that budget.
const ifaceTTL = 30 * time.Second

// ifaceCache holds one host's interface listing for ifaceTTL.
type ifaceCache struct {
	mu   sync.Mutex
	byID map[string]ifaceEntry
}

type ifaceEntry struct {
	// gen is the connection this was read through. A reconnect can be a
	// rebooted machine — or a different one behind the same name — and its
	// addresses are not the ones cached here (see Manager.Generation).
	gen   uint64
	at    time.Time
	items []adapter.Interface
}

func newIfaceCache() *ifaceCache { return &ifaceCache{byID: map[string]ifaceEntry{}} }

func (c *ifaceCache) get(id string, gen uint64) ([]adapter.Interface, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok || e.gen != gen || time.Since(e.at) > ifaceTTL {
		return nil, false
	}
	// A copy of the slice: the caller hands this straight to the frontend and
	// to the MCP tool, and one of them appending to it would corrupt what every
	// later reader sees. The elements themselves are read-only by contract.
	return append([]adapter.Interface(nil), e.items...), true
}

func (c *ifaceCache) put(id string, gen uint64, items []adapter.Interface) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = ifaceEntry{gen: gen, at: time.Now(), items: items}
}

func (c *ifaceCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
}

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
	info, err := a.requireCapability(hostID, adapter.CapNetwork, i18n.S("네트워크 정보"))
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

	// Only a success is cached. A server without iproute2 goes on being asked,
	// which is the behaviour it already had — and a failure held for half a
	// minute would be a tab that stays broken after the package is installed.
	gen := a.mgr.Generation(hostID)
	if ifaces, ok := a.ifaces.get(hostID, gen); ok {
		out.Interfaces = ifaces
	} else if res, err := conn.Poll(ctx, "ip", adapter.IPAddrArgs()...); err != nil {
		out.Warnings = append(out.Warnings, "ip addr: "+err.Error())
	} else if !res.OK() {
		out.Warnings = append(out.Warnings,
			i18n.S("`ip` 명령을 실행하지 못했습니다 — iproute2가 설치되어 있지 않을 수 있습니다"))
	} else if ifaces, perr := adapter.ParseInterfaces(res.Stdout); perr != nil {
		out.Warnings = append(out.Warnings, perr.Error())
	} else {
		out.Interfaces = ifaces
		a.ifaces.put(hostID, gen, ifaces)
	}

	if res, err := conn.Poll(ctx, "ss", adapter.SSArgs()...); err != nil {
		out.Warnings = append(out.Warnings, "ss: "+err.Error())
	} else if !res.OK() && len(res.Stdout) == 0 {
		out.Warnings = append(out.Warnings,
			i18n.S("`ss` 명령을 실행하지 못했습니다 — iproute2가 설치되어 있지 않을 수 있습니다"))
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
				i18n.S("프로세스 이름을 볼 수 없습니다 — 다른 사용자의 소켓은 관리자 권한이 필요합니다"))
		}
	}

	if len(out.Interfaces) == 0 && len(out.Listeners) == 0 && len(out.Warnings) > 0 {
		return out, fmt.Errorf("app: %s", out.Warnings[0])
	}
	return out, nil
}

// RefreshHostNetwork is HostNetwork with the interface cache dropped first.
//
// What the refresh button calls. Serving a cached interface list to somebody
// who just brought up a VPN and pressed refresh would be the app quietly
// deciding it knows better than they do — on the one control whose entire
// meaning is "I know something changed".
func (a *App) RefreshHostNetwork(hostID string) (NetworkView, error) {
	a.ifaces.forget(hostID)
	return a.HostNetwork(hostID)
}
