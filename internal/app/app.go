// Package app is the Wails binding layer: the only Go code the frontend can
// reach (§5). It translates GUI intent into sshcore calls and pushes state
// changes back out as events (§3.2e).
package app

import (
	"context"
	"runtime"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/rollback"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
	wr "github.com/wailsapp/wails/v2/pkg/runtime"
)

// processStart is stamped as early as possible so cold start can be measured
// against it — a first-class metric now that the app is task-oriented and gets
// opened many times a day (§1.6, M0 risk ⑤).
var processStart = time.Now()

// App holds everything that outlives a single binding call.
type App struct {
	ctx context.Context

	mgr       *sshcore.Manager
	hosts     *config.Store
	secrets   secret.Store
	prompts   *promptBridge
	log       *commandLog
	detected  *detectCache
	selves    *selfCache
	sshPorts  *portCache
	ifaces    *ifaceCache
	gens      *genCache
	transfers *transferQueue
	terminals *terminalRegistry
	cpu       *cpuHistory
	gpus      *gpuWatcher
	logs      *logRegistry
	mcp       mcpState
	approvals *approvalBridge
	rollback  *rollback.Store

	// emit sends an event to the frontend. It is a field rather than a direct
	// call to the Wails runtime so the prompt bridge — the one piece of logic
	// here with real concurrency in it — can be tested without a window.
	emit func(event string, payload any)

	// configDir is resolved once at startup rather than looked up per call, so
	// integration tests can point the whole app at a temporary directory.
	configDir  string
	startupErr string
	settings   *config.SettingsStore

	sim *procSim  // synthetic data for the render benchmark; not shipped
	rep *reporter // frontend timings, written where a headless run can read them
}

// New builds the app. Nothing that can fail belongs here — Startup owns that.
func New() *App {
	a := &App{sim: newProcSim(), rep: newReporter()}
	a.prompts = newPromptBridge(a)
	a.log = newCommandLog(a)
	a.detected = newDetectCache()
	a.selves = newSelfCache()
	a.sshPorts = newPortCache()
	a.ifaces = newIfaceCache()
	a.gens = newGenCache()
	a.transfers = newTransferQueue(a)
	a.terminals = newTerminalRegistry(a)
	a.cpu = newCPUHistory()
	a.gpus = newGPUWatcher()
	a.logs = newLogRegistry(a)
	a.approvals = newApprovalBridge(a)
	a.secrets = secret.Ephemeral{}
	// Until Startup runs there is no window; dropping events is correct, and
	// keeps every caller free of nil checks.
	a.emit = func(string, any) {}
	return a
}

// Startup runs once the webview exists and events can be delivered.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.emit = func(event string, payload any) { wr.EventsEmit(ctx, event, payload) }
	a.mgr = sshcore.NewManager(sshcore.ManagerOptions{}, a.emitConnectionState)
	a.secrets = secret.Open()

	// Paths only — the frontend decides what to do with them, because only it
	// knows which host and directory the user is looking at.
	wr.OnFileDrop(ctx, func(_, _ int, paths []string) {
		if len(paths) > 0 {
			a.emit("files:dropped", paths)
		}
	})

	dir, err := config.Dir()
	if err != nil {
		a.startupErr = err.Error()
		a.hosts = &config.Store{}
		return
	}
	a.configDir = dir
	// Preferences load before the host list: a corrupt hosts.json must not cost
	// the user their language, and neither file should break the other.
	a.settings = config.OpenSettings(dir)
	// What an AI overwrites is kept here, because the approval dialog stops
	// being a safety net the moment somebody turns it off and goes to bed.
	a.rollback = rollback.Open(dir)
	// The stored choice if there is one, the environment otherwise. The frontend
	// refines this a moment later with what the webview reports, which is the
	// better answer where the two disagree — but a message raised before that
	// round trip should still be in the right language.
	if lang := a.settings.Get().Language; lang != "" {
		i18n.SetLanguage(i18n.Parse(lang))
	} else if lang := systemLanguage(); lang != "" {
		i18n.SetLanguage(i18n.Parse(lang))
	}

	store, err := config.Open(dir)
	if err != nil {
		// A corrupt hosts.json must not stop the app from opening — the user
		// needs a window to fix it from. Start empty and say so.
		a.startupErr = err.Error()
		a.hosts = &config.Store{}
		return
	}
	a.hosts = store

	// Last, and only if the user asked for it: the endpoint speaks for every
	// host above, so it must not come up before they are loaded.
	a.startMCP()
}

// Shutdown closes every connection before the window goes away.
func (a *App) Shutdown(context.Context) {
	if a.terminals != nil {
		a.terminals.closeAll()
	}
	if a.logs != nil {
		a.logs.closeAll()
	}
	if a.mgr != nil {
		_ = a.mgr.Close()
	}
	if a.rep != nil {
		a.rep.close()
	}
	a.stopMCP()
}

// Bootstrap is the frontend's first call: everything needed to draw the shell,
// in one round trip.
type Bootstrap struct {
	// Version is sent to the frontend so the running build can identify itself
	// on screen. A bug report that says "the latest one" is not actionable, and
	// nothing else in the window says which binary this is.
	Version     string       `json:"version"`
	Platform    PlatformInfo `json:"platform"`
	Hosts       []HostView   `json:"hosts"`
	ColdStartMs float64      `json:"coldStartMs"`
	KeychainOK  bool         `json:"keychainOk"`
	ConfigDir   string       `json:"configDir"`
	HostsPath   string       `json:"hostsPath"`
	// Language is the explicit choice, or "" for "follow the OS" — the frontend
	// resolves that against what the webview reports.
	Language string `json:"language"`
	// SystemLanguage is what the environment says, for the frontend to fall back
	// on when the webview has nothing useful to report.
	SystemLanguage string `json:"systemLanguage"`
	StartupError   string `json:"startupError,omitempty"`
}

// Bootstrap reports the initial application state.
func (a *App) Bootstrap() Bootstrap {
	b := Bootstrap{
		Version:        Version,
		Platform:       a.Platform(),
		Hosts:          a.ListHosts(),
		ColdStartMs:    a.ColdStartMs(),
		KeychainOK:     a.secrets.Available(),
		ConfigDir:      a.configDir,
		StartupError:   a.startupErr,
		SystemLanguage: systemLanguage(),
	}
	if a.settings != nil {
		b.Language = a.settings.Get().Language
	}
	if a.hosts != nil {
		b.HostsPath = a.hosts.Path()
	}
	return b
}

// ConnectionState is the payload of a conn:state:<hostID> event.
type ConnectionState struct {
	HostID string `json:"hostId"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
}

func (a *App) emitConnectionState(hostID string, s sshcore.State, err error) {
	payload := ConnectionState{HostID: hostID, State: s.String()}
	if err != nil {
		payload.Error = err.Error()
	}
	// Both a per-host channel for the tab that cares and a broadcast for the
	// sidebar, which tracks every host at once.
	a.emit("conn:state:"+hostID, payload)
	a.emit("conn:state", payload)
}

// PlatformInfo tells the frontend which keyboard conventions to apply.
//
// It exists because §8 requires platform-correct shortcuts — Enter renames on
// macOS but opens on Windows, and getting that backwards breaks the "these are
// my files" illusion (§1.1) more thoroughly than any wrong icon could. The
// frontend must read this rather than sniff the user agent.
type PlatformInfo struct {
	OS       string `json:"os"`       // darwin, windows, linux
	Arch     string `json:"arch"`     //
	IsMac    bool   `json:"isMac"`    // convenience for the mod-key split
	ModKey   string `json:"modKey"`   // "Meta" on macOS, "Control" elsewhere
	ModLabel string `json:"modLabel"` // "⌘" or "Ctrl", for rendering hints
}

// Platform reports the host OS and its modifier conventions.
func (a *App) Platform() PlatformInfo {
	info := PlatformInfo{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		IsMac:    runtime.GOOS == "darwin",
		ModKey:   "Control",
		ModLabel: "Ctrl",
	}
	if info.IsMac {
		info.ModKey = "Meta"
		info.ModLabel = "⌘"
	}
	return info
}

// ReadClipboard returns the system clipboard as text.
//
// The frontend cannot read it for itself. WebKit refuses
// navigator.clipboard.readText() outright — "NotAllowedError: The request is
// not allowed by the user agent" — with no permission prompt to grant and no
// user gesture that satisfies it. Measured in a real build, not assumed:
// writeText is allowed in the same webview, so only the read side needs this.
//
// The terminal's paste is what needs it (§4.6). Everything else in the window
// gets paste from the OS through the Edit menu, which delivers a native paste
// event that the focused field handles itself.
//
// Bound to the frontend only. MCP tools are an explicit list in mcp_tools.go
// and this is not on it — an AI client cannot read the user's clipboard.
func (a *App) ReadClipboard() (string, error) {
	return wr.ClipboardGetText(a.ctx)
}

// ColdStartMs reports milliseconds from process start to this call. The
// frontend calls it once on first paint, which makes the number the thing the
// user actually waits for rather than a synthetic timer.
func (a *App) ColdStartMs() float64 {
	return float64(time.Since(processStart).Microseconds()) / 1000
}
