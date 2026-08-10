package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// Registers exactly what the running app registers. An earlier version of this
// helper called only registerMCPTools, so every test that walked the tool set
// was blind to the write tools — the tests passed by not looking, which is the
// same failure they exist to prevent.
func registered(t *testing.T, a *App) *mcp.Server {
	t.Helper()
	s := mcp.New()
	a.registerMCPTools(s)
	a.registerMCPWriteTools(s)
	return s
}

// The tool set, pinned.
//
// This exists because the first cut of the MCP work shipped without svc_logs
// and container_logs — the two tools the design note's own diagnosis scenario
// depends on. Every other test passed, because they all checked properties of
// the tools that *were* registered. A test that only looks at what is there
// cannot see what is missing, which is the same shape of hole the i18n coverage
// tests had. Changing this list should be a deliberate edit, in both directions.
func TestToolSetIsExactlyWhatWasDesigned(t *testing.T) {
	want := map[string]bool{
		// Discovery.
		"hosts_list": true,
		// Diagnosis. health_snapshot answers most questions in one round trip;
		// the rest are the drill-down.
		"health_snapshot": true,
		"sys_stats":       true,
		"svc_list":        true,
		"svc_logs":        true,
		"proc_list":       true,
		"container_list":  true,
		"container_logs":  true,
		"net_ports":       true,
		"sessions_list":   true,
		// Files.
		"fs_list": true,
		"fs_read": true,
		// Changes. Every one of these passes approveWrite; see
		// TestWriteToolsCannotRunWithoutApproval.
		"svc_control":       true,
		"container_control": true,
		"proc_signal":       true,
		"fs_write":          true,
	}

	got := map[string]bool{}
	for _, tool := range registered(t, appWithSettings(t)).Tools() {
		got[tool.Name] = true
	}

	for name := range want {
		if !got[name] {
			t.Errorf("%s is missing. It is in the phase-1 set for a reason; "+
				"removing it needs the same deliberation as adding one", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("%s is registered but not in the reviewed set. Add it here "+
				"once its schema and its blast radius have been looked at", name)
		}
	}
}

// Without logs, a diagnosis stops at "postgresql is failed" and never reaches
// why. This pins the pairing rather than the individual tools.
func TestEveryListingHasAMatchingLogTool(t *testing.T) {
	got := map[string]bool{}
	for _, tool := range registered(t, appWithSettings(t)).Tools() {
		got[tool.Name] = true
	}
	for listing, logs := range map[string]string{
		"svc_list":       "svc_logs",
		"container_list": "container_logs",
	} {
		if got[listing] && !got[logs] {
			t.Errorf("%s exists without %s: a model can see that something failed "+
				"but never why, which is the question worth asking", listing, logs)
		}
	}
}

// Two things stay out on purpose, and both are easy to add by accident.
//
// An arbitrary-command tool makes the per-tool allowlist decorative: switching
// svc_control off means nothing if the same thing can be typed. Deletion is
// worse — a restart is undone by restarting, a delete is not.
func TestNoEscapeHatchAndNoDeletion(t *testing.T) {
	a := appWithSettings(t)
	forbidden := map[string]string{
		"run_command": "an arbitrary-command tool needs its own toggle (§4.5)",
		"exec":        "an arbitrary-command tool needs its own toggle (§4.5)",
		"shell":       "an arbitrary-command tool needs its own toggle (§4.5)",
		"delete":      "deletion is not recoverable; it stays out while this is new",
		"remove":      "deletion is not recoverable; it stays out while this is new",
		"prune":       "deletion is not recoverable; it stays out while this is new",
		"rm":          "deletion is not recoverable; it stays out while this is new",
	}
	for _, tool := range registered(t, a).Tools() {
		for verb, why := range forbidden {
			if strings.Contains(strings.ToLower(tool.Name), verb) {
				t.Errorf("%q: %s", tool.Name, why)
			}
		}
	}
}

// The property the whole design rests on: nothing changes a server until a
// person has said so. Run with the default mode and nobody answering, every
// write tool must fail rather than proceed.
func TestWriteToolsCannotRunWithoutApproval(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	// Short enough that the test is quick, long enough to be a real wait.
	restore := WriteApprovalTimeoutForTest(150 * time.Millisecond)
	defer restore()

	writes := map[string]map[string]any{
		"svc_control":       {"hostId": "h1", "unit": "nginx.service", "action": "restart"},
		"container_control": {"hostId": "h1", "id": "abc123", "action": "stop"},
		"proc_signal":       {"hostId": "h1", "pid": float64(4242), "signal": "TERM"},
		"fs_write":          {"hostId": "h1", "path": "/etc/nginx/nginx.conf", "content": "new"},
	}

	byName := map[string]mcp.Tool{}
	for _, tool := range registered(t, a).Tools() {
		byName[tool.Name] = tool
	}

	for name, args := range writes {
		tool, ok := byName[name]
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if _, err := tool.Handler(context.Background(), args); err == nil {
			t.Errorf("%s ran with nobody approving it", name)
		} else if !strings.Contains(err.Error(), "nobody answered") {
			t.Errorf("%s failed for the wrong reason: %v", name, err)
		}
	}

	// And the refusal is in the record, whichever way it went.
	var timeouts int
	for _, e := range a.CommandLog() {
		if e.Origin == "ai" && strings.Contains(e.Line, "timeout") {
			timeouts++
		}
	}
	if timeouts != len(writes) {
		t.Errorf("logged %d timeouts, want %d — a write nobody approved still "+
			"belongs in the record", timeouts, len(writes))
	}
}

// A declined write must not run, and must tell the model not to retry.
func TestDeclinedWriteDoesNotRun(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	// Answer "no" as soon as the dialog is raised.
	a.emit = func(event string, payload any) {
		if event != "prompt:mcpwrite" {
			return
		}
		p, _ := payload.(MCPWritePrompt)
		go func() { _ = a.AnswerMCPWrite(p.ID, false) }()
	}

	_, err := a.approveWrite(writeRequest{hostID: "h1", tool: "svc_control", summary: "restart nginx"})
	if err == nil {
		t.Fatal("a declined write must fail")
	}
	if !strings.Contains(err.Error(), "Do not retry") {
		t.Errorf("the model should be told not to retry: %v", err)
	}
}

// The dialog has to carry what will actually happen. It is the only place a
// person sees the real command or the real diff — the MCP client's own
// confirmation shows the arguments a model composed, which is a different thing.
func TestApprovalPromptCarriesTheRealChange(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	var got MCPWritePrompt
	a.emit = func(event string, payload any) {
		if event != "prompt:mcpwrite" {
			return
		}
		got, _ = payload.(MCPWritePrompt)
		go func() { _ = a.AnswerMCPWrite(got.ID, true) }()
	}

	if _, err := a.approveWrite(writeRequest{
		hostID: "h1", tool: "svc_control",
		summary: "restart nginx.service",
		command: "systemctl restart -- nginx.service",
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got.Command != "systemctl restart -- nginx.service" {
		t.Errorf("the prompt must carry the literal command, got %q", got.Command)
	}
	if got.Host != "prod" {
		t.Errorf("the prompt must name the host a person recognises, got %q", got.Host)
	}
}

// Nothing an MCP client sends may influence the mode.
func TestApprovalModeCannotBeReachedOverTheWire(t *testing.T) {
	a := appWithSettings(t)
	for _, tool := range registered(t, a).Tools() {
		name := strings.ToLower(tool.Name)
		for _, verb := range []string{"policy", "approve", "bypass", "mode", "permission"} {
			if strings.Contains(name, verb) {
				t.Errorf("%q looks like it reaches the approval policy. The mode is "+
					"set in the app and nowhere else (§4.3)", tool.Name)
			}
		}
		props, _ := tool.InputSchema["properties"].(map[string]any)
		for arg := range props {
			switch strings.ToLower(arg) {
			case "bypass", "approve", "approved", "force", "mode", "elevate", "sudo", "confirm":
				t.Errorf("%s takes an argument named %q. A model that can ask for its "+
					"own approval is the injection path this design exists to close",
					tool.Name, arg)
			}
		}
	}
}

// Relaxed modes expire on their own. A window nobody remembers opening is the
// one that causes the incident.
func TestRelaxedModeExpires(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	a.SetMCPWritePolicy("h1", WriteBypass, 60)
	if got := a.policyFor("h1").Mode; got != WriteBypass {
		t.Fatalf("mode = %q, want bypass", got)
	}

	// Reach past the stored expiry rather than waiting for it.
	s := a.settings.Get().MCP
	s.Write["h1"] = config.MCPWritePolicy{Mode: WriteBypass, Until: time.Now().Add(-time.Second).Unix()}
	if err := a.settings.SetMCP(s); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := a.policyFor("h1").Mode; got != WriteAsk {
		t.Errorf("an expired window must read as ask, got %q", got)
	}
}

// A window longer than the ceiling is clamped, not honoured.
func TestWriteWindowIsBounded(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	a.SetMCPWritePolicy("h1", WriteBypass, 60*24*365)
	until := time.Unix(a.policyFor("h1").Until, 0)
	if until.After(time.Now().Add(maxWriteWindow + time.Minute)) {
		t.Errorf("window runs to %v, past the %v ceiling", until, maxWriteWindow)
	}
}

// Bypass skips the dialog. That is the point of it, and the log still records
// every call — with bypass on, attribution is what is left instead of defence.
func TestBypassSkipsTheDialogButNotTheLog(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)
	a.SetMCPWritePolicy("h1", WriteBypass, 30)

	raised := false
	a.emit = func(event string, _ any) {
		if event == "prompt:mcpwrite" {
			raised = true
		}
	}

	out, err := a.approveWrite(writeRequest{hostID: "h1", tool: "svc_control", summary: "restart nginx"})
	if err != nil {
		t.Fatalf("bypass should approve: %v", err)
	}
	if raised {
		t.Error("bypass raised a dialog")
	}
	if out != outcomeAuto {
		t.Errorf("outcome = %q, want auto-approved", out)
	}

	var found bool
	for _, e := range a.CommandLog() {
		if e.Origin == "ai" && strings.Contains(e.Line, "auto-approved") {
			found = true
		}
	}
	if !found {
		t.Error("a bypassed write must still be in the record")
	}
}

// Sharing a host to be read must not also let it be changed.
func TestSharingForReadingDoesNotAllowWriting(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)
	if got := a.policyFor("h1").Mode; got != WriteAsk {
		t.Errorf("a freshly shared host is %q; sharing to read is not sharing to write", got)
	}
}

func seedSharedHost(t *testing.T, a *App) {
	t.Helper()
	if err := a.hosts.Upsert(config.Host{
		ID: "h1", Name: "prod", Hostname: "10.0.0.5", Port: 22, User: "junwon",
		Auth: []config.AuthMethod{config.AuthAgent},
	}); err != nil {
		t.Fatalf("seed host: %v", err)
	}
	a.SetMCPHost("h1", true)
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

// logResult carries the only real logic in the log tools: what to keep, what to
// say when nothing matched.
func TestLogResultShapesOutputForAModel(t *testing.T) {
	body := "line one\nERROR connection refused\nline three\n"

	t.Run("keeps every line when nothing filters", func(t *testing.T) {
		got := logResult("nginx", body, "")
		lines, _ := got["lines"].([]string)
		if len(lines) != 3 {
			t.Fatalf("lines = %v", lines)
		}
		if _, noted := got["note"]; noted {
			t.Error("a non-empty log should not carry the empty note")
		}
	})

	t.Run("grep is case-insensitive and reports what it scanned", func(t *testing.T) {
		got := logResult("nginx", body, "error")
		lines, _ := got["lines"].([]string)
		if len(lines) != 1 || !strings.Contains(lines[0], "connection refused") {
			t.Fatalf("lines = %v", lines)
		}
		if got["matched"] != 1 || got["scanned"] != 3 {
			t.Errorf("matched/scanned = %v/%v, want 1/3", got["matched"], got["scanned"])
		}
	})

	// An empty result is the answer most likely to be misread: a model handed
	// nothing concludes the service never logged and stops looking.
	t.Run("explains an empty result", func(t *testing.T) {
		got := logResult("nginx", "", "")
		if lines, _ := got["lines"].([]string); len(lines) != 0 {
			t.Fatalf("lines = %v", lines)
		}
		note, _ := got["note"].(string)
		if !strings.Contains(note, "svc_list") {
			t.Errorf("the note should suggest checking the unit name: %q", note)
		}
	})

	// The tail explains a failure; the head is start-up noise.
	t.Run("keeps the tail when capping", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < maxLogLines+50; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		got := logResult("nginx", b.String(), "")
		lines, _ := got["lines"].([]string)
		if len(lines) != maxLogLines {
			t.Fatalf("kept %d lines, want %d", len(lines), maxLogLines)
		}
		if lines[len(lines)-1] != fmt.Sprintf("line %d", maxLogLines+49) {
			t.Errorf("the last line was dropped: %q", lines[len(lines)-1])
		}
	})
}

// A malformed time expression makes journalctl fail in a way that reads to a
// model like the unit is broken, so it is refused before it is sent.
func TestJournalTimeExpressionsAreValidated(t *testing.T) {
	for _, ok := range []string{"", "-10m", "1 hour ago", "2026-08-10 09:00:00", "-1h"} {
		if !safeJournalArg(ok) {
			t.Errorf("%q should be accepted", ok)
		}
	}
	for _, bad := range []string{"$(whoami)", "`id`", "a;b", "x|y", strings.Repeat("a", 41)} {
		if safeJournalArg(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}
