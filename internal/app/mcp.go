package app

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/mcp"
)

// The MCP integration's lifecycle and its bindings to the GUI.
//
// Reads need only the per-host share (§4.1). Writes additionally pass the
// approval gate in mcp_approval.go, whose mode is per host, expires on its own,
// and is settable from this app and nowhere else.

// Rate limits. One diagnosis fans out across four to six tools, so the burst
// covers a full turn; the refill is what a small server absorbs without the
// person watching the Command Log feeling it.
const (
	mcpCallsPerSecond = 1.5
	mcpBurst          = 8
)

// defaultMCPPort is tried first so the endpoint has a stable address.
//
// Letting the OS choose looks tidier and is wrong: the port lands in the line
// the user pasted into their MCP client, and a new one on every launch means
// that line is dead by tomorrow. Whatever is actually bound is written back to
// settings, so it stays put even when this preference was unavailable.
const defaultMCPPort = 8779

type mcpState struct {
	mu       sync.Mutex
	listener *mcp.Listener
	url      string
}

// MCPStatus is what the settings screen draws.
type MCPStatus struct {
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
	Token   string `json:"token,omitempty"`
	// Hosts the AI may read, by host ID.
	Hosts map[string]bool `json:"hosts"`
	// Write is the approval mode per host: "ask", "strict" or "bypass", with
	// the unix second it reverts. A host with no entry is "ask".
	Write map[string]WritePolicyView `json:"write"`
	// Delete lists hosts where file deletion is offered at all.
	Delete map[string]bool `json:"delete"`
	// Snippet is the line to paste into an MCP client.
	Snippet string `json:"snippet,omitempty"`
	Error   string `json:"error,omitempty"`
}

// WritePolicyView is one host's approval mode, as the settings screen sees it.
type WritePolicyView struct {
	Mode  string `json:"mode"`
	Until int64  `json:"until,omitempty"`
}

// startMCP brings the endpoint up if the user has enabled it.
//
// A failure here is reported, never fatal. The app's job is to manage servers;
// losing the AI endpoint should not cost somebody their window.
func (a *App) startMCP() {
	if a.settings == nil {
		return
	}
	s := a.settings.Get().MCP
	if !s.Enabled {
		return
	}

	a.mcp.mu.Lock()
	defer a.mcp.mu.Unlock()
	if a.mcp.listener != nil {
		return
	}

	token := s.Token
	if token == "" {
		var err error
		if token, err = mcp.NewToken(); err != nil {
			a.emit("log:warning", i18n.T("MCP 토큰을 만들지 못했습니다: %v", err))
			return
		}
		s.Token = token
		if err := a.settings.SetMCP(s); err != nil {
			a.emit("log:warning", i18n.T("MCP 설정을 저장하지 못했습니다: %v", err))
		}
	}

	srv := mcp.New()
	a.registerMCPTools(srv)
	a.registerMCPWriteTools(srv)

	opts := mcp.Options{
		Token:   token,
		Port:    s.Port,
		Limiter: mcp.NewLimiter(mcpCallsPerSecond, mcpBurst),
		OnCall:  a.logAICall,
	}
	if opts.Port == 0 {
		opts.Port = defaultMCPPort
	}

	listener, url, err := srv.Serve(opts)
	if err != nil && opts.Port != 0 {
		// Something else has the port. Take any free one rather than leaving the
		// integration off, and remember it so this is a one-time change.
		opts.Port = 0
		listener, url, err = srv.Serve(opts)
	}
	if err != nil {
		a.emit("log:warning", i18n.T("MCP 서버를 열지 못했습니다: %v", err))
		return
	}
	a.mcp.listener, a.mcp.url = listener, url

	// Persist whatever was bound. This is what keeps the client config the user
	// pasted once from going stale on the next launch.
	if bound := listenerPort(listener.Addr()); bound != 0 && bound != s.Port {
		s.Port = bound
		if err := a.settings.SetMCP(s); err != nil {
			a.emit("log:warning", i18n.T("MCP 설정을 저장하지 못했습니다: %v", err))
		}
	}
}

// listenerPort pulls the port out of a "127.0.0.1:8779" address.
func listenerPort(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return n
}

// stopMCP closes the endpoint.
func (a *App) stopMCP() {
	a.mcp.mu.Lock()
	listener := a.mcp.listener
	a.mcp.listener, a.mcp.url = nil, ""
	a.mcp.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
}

// logAICall records what the AI asked for, in the same panel as everything the
// user does (§4.6).
//
// The tool call is logged rather than the SSH commands underneath it, and that
// is the better record: "the AI asked for failed units on prod" is the intent,
// and the commands it produces show up on their own lines a moment later
// exactly as they would for a click. Tagging the SSH lines instead would mean
// threading a caller identity through every binding, which buys a label and
// costs a signature change in twenty files.
func (a *App) logAICall(tool string, args map[string]any) {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(tool)
	b.WriteString("(")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%v", k, args[k])
	}
	b.WriteString(")")

	hostID, _ := args["hostId"].(string)
	a.log.AICall(hostID, b.String())
}

/* ------------------------------------------------------------- bindings */

// MCPState reports the integration's current state.
func (a *App) MCPState() MCPStatus {
	out := MCPStatus{Hosts: map[string]bool{}}
	if a.settings == nil {
		return out
	}
	s := a.settings.Get().MCP
	out.Enabled = s.Enabled
	out.Token = s.Token
	for k, v := range s.Hosts {
		out.Hosts[k] = v
	}
	out.Delete = map[string]bool{}
	for k, v := range s.Delete {
		out.Delete[k] = v
	}
	out.Write = map[string]WritePolicyView{}
	for id := range s.Hosts {
		// Reported through policyFor so an expired window reads as "ask" here
		// too, rather than the UI showing a mode that no longer applies.
		p := a.policyFor(id)
		out.Write[id] = WritePolicyView{Mode: p.Mode, Until: p.Until}
	}

	a.mcp.mu.Lock()
	out.Running = a.mcp.listener != nil
	out.URL = a.mcp.url
	a.mcp.mu.Unlock()

	if out.Running {
		// The exact line to paste. A user who has to assemble this from three
		// fields gets it wrong once and concludes the feature is broken.
		out.Snippet = fmt.Sprintf(
			"claude mcp add --transport http litedeck %s --header \"Authorization: Bearer %s\"",
			out.URL, out.Token)
	}
	return out
}

// SetMCPEnabled turns the endpoint on or off.
func (a *App) SetMCPEnabled(enabled bool) MCPStatus {
	if a.settings == nil {
		return a.MCPState()
	}
	s := a.settings.Get().MCP
	s.Enabled = enabled
	if enabled && s.Token == "" {
		if t, err := mcp.NewToken(); err == nil {
			s.Token = t
		}
	}
	if err := a.settings.SetMCP(s); err != nil {
		out := a.MCPState()
		out.Error = err.Error()
		return out
	}

	a.stopMCP()
	if enabled {
		a.startMCP()
	}
	return a.MCPState()
}

// SetMCPHost shares one server with AI clients, or stops sharing it.
//
// Per host on purpose. "Staging yes, production no" is how people actually
// work, and a single global switch would make the cautious answer no.
func (a *App) SetMCPHost(hostID string, allowed bool) MCPStatus {
	if a.settings == nil {
		return a.MCPState()
	}
	s := a.settings.Get().MCP
	if s.Hosts == nil {
		s.Hosts = map[string]bool{}
	}
	if allowed {
		s.Hosts[hostID] = true
	} else {
		delete(s.Hosts, hostID)
	}
	if err := a.settings.SetMCP(s); err != nil {
		out := a.MCPState()
		out.Error = err.Error()
		return out
	}
	return a.MCPState()
}

// SetMCPHostDelete decides whether file deletion is offered on one host.
func (a *App) SetMCPHostDelete(hostID string, allowed bool) MCPStatus {
	if a.settings == nil {
		return a.MCPState()
	}
	s := a.settings.Get().MCP
	if s.Delete == nil {
		s.Delete = map[string]bool{}
	}
	if allowed {
		s.Delete[hostID] = true
	} else {
		delete(s.Delete, hostID)
	}
	if err := a.settings.SetMCP(s); err != nil {
		out := a.MCPState()
		out.Error = err.Error()
		return out
	}
	return a.MCPState()
}

// RotateMCPToken issues a new token and invalidates the old one.
//
// Manual, never automatic. Rotating on the app's schedule would break the
// client config the user pasted in, at a moment they did not choose.
func (a *App) RotateMCPToken() MCPStatus {
	if a.settings == nil {
		return a.MCPState()
	}
	token, err := mcp.NewToken()
	if err != nil {
		out := a.MCPState()
		out.Error = err.Error()
		return out
	}
	s := a.settings.Get().MCP
	s.Token = token
	if err := a.settings.SetMCP(s); err != nil {
		out := a.MCPState()
		out.Error = err.Error()
		return out
	}

	if s.Enabled {
		a.stopMCP()
		a.startMCP()
	}
	return a.MCPState()
}

// mcpSettings is a small helper for tests that need the stored shape.
func (a *App) mcpSettings() config.MCPSettings {
	if a.settings == nil {
		return config.MCPSettings{}
	}
	return a.settings.Get().MCP
}
