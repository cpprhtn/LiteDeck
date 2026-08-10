package app

import (
	"context"
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/mcp"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

func appWithSettings(t *testing.T) *App {
	t.Helper()
	a := New()
	a.configDir = t.TempDir()
	a.settings = config.OpenSettings(a.configDir)
	store, err := config.Open(a.configDir)
	if err != nil {
		t.Fatalf("open host store: %v", err)
	}
	a.hosts = store
	// hosts_list reports connection state, so the manager has to exist. In the
	// app it always does: startMCP runs at the end of Startup, after this.
	a.mgr = sshcore.NewManager(sshcore.ManagerOptions{}, a.emitConnectionState)
	t.Cleanup(func() { _ = a.mgr.Close() })
	return a
}

func registered(t *testing.T, a *App) *mcp.Server {
	t.Helper()
	s := mcp.New()
	a.registerMCPTools(s)
	return s
}

// The whole reason read-only can ship without the approval model is that there
// is nothing to approve. This fails the moment somebody adds a tool that
// changes a server, which is exactly when the design note's §4 has to exist.
func TestNoToolCanChangeAServer(t *testing.T) {
	a := appWithSettings(t)
	forbidden := []string{"write", "restart", "stop", "start", "kill", "delete",
		"remove", "prune", "exec", "command", "push", "chmod", "rename", "mkdir",
		"upload", "signal", "elevate", "sudo"}

	for _, tool := range registered(t, a).Tools() {
		for _, verb := range forbidden {
			if strings.Contains(strings.ToLower(tool.Name), verb) {
				t.Errorf("%q reads like a write tool. Writes need the per-connection "+
					"approval model first; shipping one without it cannot be undone",
					tool.Name)
			}
		}
	}
}

// Registering a server in LiteDeck must not be what hands it to an AI.
func TestHostsAreRefusedUntilShared(t *testing.T) {
	a := appWithSettings(t)
	if err := a.hosts.Upsert(config.Host{
		ID: "h1", Name: "prod", Hostname: "10.0.0.5", Port: 22, User: "junwon",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	_, err := a.mcpHost(map[string]any{"hostId": "h1"})
	if err == nil {
		t.Fatal("a host nobody shared must be refused")
	}
	// The message is read by a model, so it has to close the door rather than
	// suggest a parameter that would open it.
	if !strings.Contains(err.Error(), "cannot be enabled from here") {
		t.Errorf("refusal should say the app owns this switch, got: %v", err)
	}

	a.SetMCPHost("h1", true)
	if _, err := a.mcpHost(map[string]any{"hostId": "h1"}); err != nil {
		t.Errorf("after sharing, the host should resolve: %v", err)
	}

	a.SetMCPHost("h1", false)
	if _, err := a.mcpHost(map[string]any{"hostId": "h1"}); err == nil {
		t.Error("un-sharing must take effect immediately")
	}
}

// An unnamed host must be an error, never a guess. Picking a default would mean
// reading a production machine because the model omitted an argument.
func TestHostMustBeNamed(t *testing.T) {
	a := appWithSettings(t)
	for _, args := range []map[string]any{{}, {"hostId": ""}, {"hostId": "   "}} {
		if _, err := a.mcpHost(args); err == nil {
			t.Errorf("args %v should have been refused", args)
		} else if !strings.Contains(err.Error(), "hosts_list") {
			t.Errorf("the error should point at hosts_list, got: %v", err)
		}
	}
	if _, err := a.mcpHost(map[string]any{"hostId": "nope"}); err == nil {
		t.Error("an unknown host ID should be refused")
	}
}

// hosts_list is the entry point and must work with nothing shared, or a model
// has no way to discover what it may ask for.
func TestHostsListWorksBeforeAnythingIsShared(t *testing.T) {
	a := appWithSettings(t)
	if err := a.hosts.Upsert(config.Host{ID: "h1", Name: "prod", Hostname: "h", Port: 22, User: "u",
		Auth: []config.AuthMethod{config.AuthAgent}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var tool mcp.Tool
	for _, candidate := range registered(t, a).Tools() {
		if candidate.Name == "hosts_list" {
			tool = candidate
		}
	}
	if tool.Handler == nil {
		t.Fatal("hosts_list is not registered")
	}

	out, err := tool.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("hosts_list: %v", err)
	}
	result, _ := out.(map[string]any)
	hosts, _ := result["hosts"].([]map[string]any)
	if len(hosts) != 1 {
		t.Fatalf("want one host, got %v", hosts)
	}
	if hosts[0]["sharedWithAI"] != false {
		t.Errorf("an unshared host must report sharedWithAI false: %v", hosts[0])
	}
	// The listing must not leak a credential path or anything else that is not
	// needed to name a server.
	for _, forbidden := range []string{"identityFile", "auth", "password"} {
		if _, present := hosts[0][forbidden]; present {
			t.Errorf("hosts_list exposes %q, which a model has no use for", forbidden)
		}
	}
}

// Every tool must require an explicit hostId, so none of them can be called
// against a server nobody shared.
func TestEveryServerToolRequiresAHost(t *testing.T) {
	a := appWithSettings(t)
	for _, tool := range registered(t, a).Tools() {
		if tool.Name == "hosts_list" {
			continue // the discovery entry point, deliberately argument-free
		}
		schema := tool.InputSchema
		required, _ := schema["required"].([]string)
		found := false
		for _, r := range required {
			if r == "hostId" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not require hostId; it could be called against "+
				"whatever the model guesses", tool.Name)
		}
	}
}

// The schema is prompt text. A tool the model cannot tell apart from another
// gets called wrongly, which costs a round trip on somebody's small server.
func TestToolsAreDescribedForAModel(t *testing.T) {
	a := appWithSettings(t)
	for _, tool := range registered(t, a).Tools() {
		if len(tool.Description) < 40 {
			t.Errorf("%s has a thin description (%q)", tool.Name, tool.Description)
		}
		if tool.Handler == nil {
			t.Errorf("%s has no handler", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s has no object schema", tool.Name)
		}
	}
}

// The token has to survive a restart or the line the user pasted into their
// client config stops working, which reads as "the feature is broken".
func TestTokenPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	first := New()
	first.configDir = dir
	first.settings = config.OpenSettings(dir)
	first.SetMCPEnabled(true)
	token := first.MCPState().Token
	if token == "" {
		t.Fatal("enabling should have issued a token")
	}
	first.stopMCP()

	second := New()
	second.configDir = dir
	second.settings = config.OpenSettings(dir)
	if got := second.MCPState().Token; got != token {
		t.Errorf("token changed across restart: %q then %q", token, got)
	}
}

func TestRotateIssuesANewToken(t *testing.T) {
	a := appWithSettings(t)
	a.SetMCPEnabled(true)
	defer a.stopMCP()

	before := a.MCPState().Token
	after := a.RotateMCPToken().Token
	if before == after || after == "" {
		t.Errorf("rotate did not change the token: %q then %q", before, after)
	}
}

// Off unless asked. An endpoint speaking for every connected server must not
// open because the app was installed.
func TestDisabledByDefaultAndStartsWhenEnabled(t *testing.T) {
	a := appWithSettings(t)

	state := a.MCPState()
	if state.Enabled || state.Running {
		t.Fatalf("MCP should be off by default: %+v", state)
	}

	state = a.SetMCPEnabled(true)
	defer a.stopMCP()
	if !state.Enabled || !state.Running {
		t.Fatalf("enabling should start the endpoint: %+v", state)
	}
	if !strings.HasPrefix(state.URL, "http://127.0.0.1:") {
		t.Errorf("URL %q must be loopback", state.URL)
	}
	// The snippet is what the user pastes; assembling it by hand is where they
	// get it wrong and conclude the feature does not work.
	if !strings.Contains(state.Snippet, "--transport http") ||
		!strings.Contains(state.Snippet, state.Token) ||
		!strings.Contains(state.Snippet, state.URL) {
		t.Errorf("snippet is not paste-ready: %q", state.Snippet)
	}

	if state = a.SetMCPEnabled(false); state.Running {
		t.Errorf("disabling should stop the endpoint: %+v", state)
	}
}

// What the AI asked for has to reach the same panel as what the user did.
func TestAICallsReachTheCommandLog(t *testing.T) {
	a := appWithSettings(t)
	a.logAICall("svc_list", map[string]any{"hostId": "h1", "state": "failed"})

	entries := a.CommandLog()
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Origin != "ai" {
		t.Errorf("origin = %q, want ai — the user must be able to tell", e.Origin)
	}
	if e.HostID != "h1" {
		t.Errorf("hostID = %q, want h1", e.HostID)
	}
	// Arguments are sorted so the same call always reads the same way.
	if e.Line != "svc_list(hostId=h1, state=failed)" {
		t.Errorf("line = %q", e.Line)
	}
}
