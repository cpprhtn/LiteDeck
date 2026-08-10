package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The service view (§4.3), and the per-host detection it depends on.

// pollTimeout bounds one view refresh. Generous enough for a loaded server,
// short enough that a wedged host does not freeze the tab.
const pollTimeout = 20 * time.Second

type detectCache struct {
	mu   sync.Mutex
	byID map[string]adapter.ServerInfo
}

func newDetectCache() *detectCache {
	return &detectCache{byID: make(map[string]adapter.ServerInfo)}
}

func (c *detectCache) get(id string) (adapter.ServerInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byID[id]
	return v, ok
}

func (c *detectCache) put(id string, info adapter.ServerInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[id] = info
}

func (c *detectCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, id)
}

// ServerInfoView is Detect's result plus the capability map the UI switches on.
type ServerInfoView struct {
	adapter.ServerInfo
	Capabilities map[adapter.Capability]bool `json:"capabilities"`
	// Supported is sent rather than left for the frontend to work out from the
	// platform string. It was worked out there — `platform !== 'linux'` — and when
	// the Windows adapter landed, Go started supporting it while the UI went on
	// showing "this server is not supported" over a working adapter. One source of
	// truth for a question asked on both sides.
	Supported bool `json:"supported"`
}

func newServerInfoView(info adapter.ServerInfo) ServerInfoView {
	return ServerInfoView{
		ServerInfo:   info,
		Capabilities: info.Capabilities(),
		Supported:    info.Platform.Supported(),
	}
}

// DetectHost probes a connected server, caching the result for the life of the
// connection. Detection is not free — several round trips — and nothing it
// looks at changes while the user is logged in.
func (a *App) DetectHost(hostID string) (ServerInfoView, error) {
	if info, ok := a.detected.get(hostID); ok {
		return newServerInfoView(info), nil
	}

	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return ServerInfoView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	info, err := adapter.Detect(ctx, conn)
	if err != nil {
		return ServerInfoView{}, err
	}
	a.detected.put(hostID, info)
	return newServerInfoView(info), nil
}

// requireAdapter refuses work on a host no adapter can drive.
//
// The guard is here rather than in the UI for the usual reason: a greyed-out tab
// is a suggestion, and a binding called directly walks straight past it. The error
// names the platform that was actually found, because "unsupported" alone sends
// people looking for a bug that is not there.
//
// Files and terminals are deliberately never gated — SFTP and a PTY come from SSH
// itself, so they work even on a host nothing else can drive.
func (a *App) requireAdapter(hostID string) (ServerInfoView, error) {
	info, err := a.DetectHost(hostID)
	if err != nil {
		return info, err
	}
	if !info.Platform.Supported() {
		name := info.PrettyName
		if name == "" {
			name = info.Kernel
		}
		if name == "" {
			name = string(info.Platform)
		}
		return info, fmt.Errorf(
			i18n.S("LiteDeck은 아직 이 서버를 지원하지 않습니다 (%s). ")+
				i18n.S("파일 탐색과 터미널은 그대로 쓸 수 있습니다"), name)
	}
	return info, nil
}

// requireCapability refuses a view the server cannot serve.
//
// Keyed on the capability map rather than on a platform test, so there is one
// place that knows what each adapter implements. The frontend asks the same map:
// checking hasSystemd directly is what told a Windows host it had no init system
// while the service list underneath it worked perfectly.
func (a *App) requireCapability(hostID string, c adapter.Capability, what string) (ServerInfoView, error) {
	info, err := a.requireAdapter(hostID)
	if err != nil {
		return info, err
	}
	if !info.Capabilities[c] {
		name := info.PrettyName
		if name == "" {
			name = string(info.Platform)
		}
		return info, i18n.Errorf("%s에서 %s을(를) 읽을 수 없습니다", name, what)
	}
	return info, nil
}

// ListServices returns the merged service table (§4.3).
//
// Two commands, because neither alone is enough: list-units knows the runtime
// state, list-unit-files knows whether a unit is enabled. They are issued
// concurrently — that is what the multiplexed connection is for (§3.2a).
func (a *App) ListServices(hostID string) ([]linuxsystemd.ServiceUnit, error) {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return nil, err
	}
	info, err := a.requireCapability(hostID, adapter.CapServices, i18n.S("서비스 목록"))
	if err != nil {
		return nil, err
	}

	if info.Platform == adapter.PlatformWindows {
		ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
		defer cancel()
		// One command rather than the two systemd needs: Win32_Service carries the
		// runtime state and the start mode together, where systemd splits them
		// across list-units and list-unit-files.
		out, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll, adapter.WindowsServiceListScript())
		if err != nil {
			return nil, err
		}
		return adapter.ParseWindowsServices(out)
	}

	unitArgs := []string{"list-units", "--type=service", "--all"}
	fileArgs := []string{"list-unit-files", "--type=service"}
	if info.SystemdJSON {
		unitArgs = append(unitArgs, "--output=json")
		fileArgs = append(fileArgs, "--output=json")
	} else {
		unitArgs = append(unitArgs, "--plain", "--no-legend")
		fileArgs = append(fileArgs, "--plain", "--no-legend")
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	var (
		wg                 sync.WaitGroup
		unitsRes, filesRes *sshcore.Result
		unitsErr, filesErr error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		unitsRes, unitsErr = conn.Poll(ctx, "systemctl", unitArgs...)
	}()
	go func() {
		defer wg.Done()
		filesRes, filesErr = conn.Poll(ctx, "systemctl", fileArgs...)
	}()
	wg.Wait()

	if unitsErr != nil {
		return nil, unitsErr
	}
	if err := unitsRes.Err(); err != nil {
		return nil, err
	}
	if filesErr != nil {
		return nil, filesErr
	}
	if err := filesRes.Err(); err != nil {
		return nil, err
	}

	var loaded []linuxsystemd.ServiceUnit
	var files map[string]linuxsystemd.ServiceUnit
	if info.SystemdJSON {
		if loaded, err = linuxsystemd.ParseListUnits(unitsRes.Stdout); err != nil {
			return nil, err
		}
		if files, err = linuxsystemd.ParseUnitFiles(filesRes.Stdout); err != nil {
			return nil, err
		}
	} else {
		if loaded, err = linuxsystemd.ParseListUnitsTable(unitsRes.Stdout); err != nil {
			return nil, err
		}
		if files, err = linuxsystemd.ParseUnitFilesTable(filesRes.Stdout); err != nil {
			return nil, err
		}
	}
	return linuxsystemd.MergeServices(loaded, files), nil
}

// serviceActions is an allowlist. The unit name is quoted by shellquote either
// way, but restricting the verb means a bug in the frontend cannot turn into an
// arbitrary systemctl subcommand.
var serviceActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "reload": true,
	"enable": true, "disable": true,
}

// ServiceAction runs one systemctl verb against one unit (§4.3).
//
// elevate is false on the first attempt: commands run as the logged-in user,
// and only if the server refuses does the UI offer to retry as administrator
// (§7.2). It returns a result rather than an error because "you need root" is a
// question to ask, not a failure to report.
func (a *App) ServiceAction(hostID, unit, action string, elevate bool) ActionResult {
	if !serviceActions[action] {
		return failResult(fmt.Errorf("app: unsupported service action %q", action))
	}
	info, err := a.requireCapability(hostID, adapter.CapServices, i18n.S("서비스 목록"))
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
		var script string
		switch action {
		case "enable", "disable":
			script, err = adapter.WindowsServiceStartTypeScript(unit, action == "enable")
		default:
			script, err = adapter.WindowsServiceActionScript(action, unit)
		}
		if err != nil {
			return failResult(err)
		}
		res, err := a.execPowerShell(ctx, conn, sshcore.CommandAction, script)
		return windowsActionResult(res, err)
	}

	// `--` stops systemctl reading a unit name as an option (§3.4).
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate, "systemctl", action, "--", unit)
	if err != nil {
		return failResult(err)
	}
	return a.classify(hostID, res, elevate)
}

func isPermissionDenied(res *sshcore.Result) bool {
	s := strings.ToLower(string(res.Stderr))
	for _, marker := range []string{
		"access denied", "interactive authentication required",
		"permission denied", "must be root",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
