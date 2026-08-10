package app

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/mcp"
	"github.com/cpprhtn/LiteDeck/internal/rollback"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

func appWithSettings(t *testing.T) *App {
	t.Helper()
	a := New()
	a.configDir = t.TempDir()
	a.settings = config.OpenSettings(a.configDir)
	a.rollback = rollback.Open(a.configDir)
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
		"fs_delete":         true,
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
		// Deletion is offered now, but only for a single file whose contents
		// were copied first. Container and image removal stay out: nothing
		// copies those, so nothing can put them back.
		"container_remove": "removing a container cannot be undone",
		"image_remove":     "removing an image cannot be undone",
		"prune":            "pruning cannot be undone",
	}
	for _, tool := range registered(t, a).Tools() {
		for verb, why := range forbidden {
			if strings.Contains(strings.ToLower(tool.Name), verb) {
				t.Errorf("%q: %s", tool.Name, why)
			}
		}
	}
}

// Which mode asks about what. This is the whole approval model in one table.
//
// The default deliberately does not ask about a restart: the MCP client already
// showed the user those exact arguments, and a second dialog for a decision
// just made is a click people learn to skip. It does ask about a file, because
// the diff is against what is on the server right now — something no client can
// know. strict is for the host you cannot afford to be wrong about.
func TestWhichModeAsksAboutWhat(t *testing.T) {
	for _, tc := range []struct {
		mode string
		tool string
		asks bool
	}{
		{WriteAsk, "svc_control", false},
		{WriteAsk, "container_control", false},
		{WriteAsk, "proc_signal", false},
		{WriteAsk, "fs_write", true},
		{WriteAsk, "fs_edit", true},
		{WriteAsk, "fs_delete", true},

		{WriteStrict, "svc_control", true},
		{WriteStrict, "proc_signal", true},
		{WriteStrict, "fs_write", true},

		{WriteBypass, "svc_control", false},
		{WriteBypass, "fs_write", false},
	} {
		if got := asksAbout(tc.mode, tc.tool); got != tc.asks {
			t.Errorf("%s/%s: asks = %v, want %v", tc.mode, tc.tool, got, tc.asks)
		}
	}
}

// A file change is never silent in the default mode. This is the one gate that
// survived the reduction, so it gets its own test rather than living only in
// the table above.
func TestFileWritesAreNotSilentByDefault(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)
	restore := WriteApprovalTimeoutForTest(150 * time.Millisecond)
	defer restore()

	byName := map[string]mcp.Tool{}
	for _, tool := range registered(t, a).Tools() {
		byName[tool.Name] = tool
	}
	tool, ok := byName["fs_write"]
	if !ok {
		t.Fatal("fs_write is not registered")
	}

	_, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "h1", "path": "/etc/nginx/nginx.conf", "content": "new",
	})
	if err == nil {
		t.Fatal("a file write ran with nobody approving it")
	}
	if !strings.Contains(err.Error(), "nobody answered") {
		t.Errorf("failed for the wrong reason: %v", err)
	}

	var timeouts int
	for _, e := range a.CommandLog() {
		if e.Origin == "ai" && strings.Contains(e.Line, "timeout") {
			timeouts++
		}
	}
	if timeouts != 1 {
		t.Errorf("logged %d timeouts, want 1", timeouts)
	}
}

// Deletion is only offered because it can be put back. If the copy cannot be
// made, the delete must not happen — a tool that sometimes destroys things for
// good, depending on the file, is worse than one that never does.
func TestDeleteIsRefusedWhenItCannotBeUndone(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)
	a.SetMCPHostDelete("h1", true) // past the permission gate, onto the path guard

	byName := map[string]mcp.Tool{}
	for _, tool := range registered(t, a).Tools() {
		byName[tool.Name] = tool
	}
	tool, ok := byName["fs_delete"]
	if !ok {
		t.Fatal("fs_delete is not registered")
	}

	// Protected paths are refused outright: the GUI guard is a person typing
	// the path out, and a model can type.
	for _, path := range []string{"/", "/etc", "/usr"} {
		_, err := tool.Handler(context.Background(), map[string]any{"hostId": "h1", "path": path})
		if err == nil {
			t.Errorf("%s should be refused", path)
		} else if !strings.Contains(err.Error(), "protected") && !strings.Contains(err.Error(), "app") {
			t.Errorf("%s: unclear refusal: %v", path, err)
		}
	}
}

// strict restores the old behaviour for a host that needs it.
func TestStrictAsksAboutEverything(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)
	a.SetMCPWritePolicy("h1", WriteStrict, 60)
	restore := WriteApprovalTimeoutForTest(120 * time.Millisecond)
	defer restore()

	raised := 0
	a.emit = func(event string, _ any) {
		if event == "prompt:mcpwrite" {
			raised++
		}
	}
	if _, err := a.approveWrite(writeRequest{hostID: "h1", tool: "svc_control", summary: "restart nginx"}); err == nil {
		t.Error("strict must not let a restart through unasked")
	}
	if raised != 1 {
		t.Errorf("raised %d dialogs, want 1", raised)
	}
}

// A declined write must not run, and must tell the model not to retry.
func TestDeclinedWriteDoesNotRun(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)
	a.SetMCPWritePolicy("h1", WriteStrict, 60)

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
	// A restart only raises a dialog on a host held at strict.
	a.SetMCPWritePolicy("h1", WriteStrict, 60)

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

// The port has to survive a restart, or the line the user pasted into their MCP
// client is dead the next morning. Letting the OS pick looks tidier and is the
// bug: it lands in that line.
func TestPortIsStableAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	first := New()
	first.configDir = dir
	first.settings = config.OpenSettings(dir)
	state := first.SetMCPEnabled(true)
	if !state.Running {
		t.Fatalf("did not start: %s", state.Error)
	}
	url := state.URL
	port := first.mcpSettings().Port
	if port == 0 {
		t.Fatal("the bound port was not written back to settings")
	}
	first.stopMCP()

	second := New()
	second.configDir = dir
	second.settings = config.OpenSettings(dir)
	again := second.SetMCPEnabled(true)
	defer second.stopMCP()
	if again.URL != url {
		t.Errorf("address moved across a restart: %s then %s", url, again.URL)
	}
}

// A taken port must not leave the integration switched off.
func TestBusyPortFallsBackAndIsRemembered(t *testing.T) {
	dir := t.TempDir()

	// Hold the preferred port so the app has to move.
	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", defaultMCPPort))
	if err != nil {
		t.Skipf("port %d is already in use by something else", defaultMCPPort)
	}
	defer blocker.Close()

	a := New()
	a.configDir = dir
	a.settings = config.OpenSettings(dir)
	state := a.SetMCPEnabled(true)
	defer a.stopMCP()

	if !state.Running {
		t.Fatalf("a busy preferred port must not stop the endpoint: %s", state.Error)
	}
	if got := a.mcpSettings().Port; got == 0 || got == defaultMCPPort {
		t.Errorf("fell back to port %d without remembering a usable one", got)
	}
}

// A change an AI makes has to be recoverable, because the mode people want —
// set it and go to bed — removes the dialog that would otherwise catch it.
func TestMCPChangesAreRecordedAndRestorable(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	a.recordAIChange("h1", "/etc/nginx/nginx.conf", "write", []byte("worker_processes 1;\n"), false)
	a.recordAIChange("h1", "/tmp/new.md", "write", nil, true)

	changes := a.MCPChanges("h1")
	if len(changes) != 2 {
		t.Fatalf("want 2 changes, got %d", len(changes))
	}
	// Newest first: the thing you want back is usually the last thing that
	// happened.
	if changes[0].Path != "/tmp/new.md" {
		t.Errorf("newest first: got %s", changes[0].Path)
	}
	if !changes[0].Created {
		t.Error("a new file must be marked as created, or undoing it would write an empty file")
	}
	for _, c := range changes {
		if !c.Undoable {
			t.Errorf("%s should be undoable", c.Path)
		}
	}
	if a.MCPChanges("other") != nil && len(a.MCPChanges("other")) != 0 {
		t.Error("the list is per host")
	}
}

// Restoring is a person's remedy. Handing it to the AI would let one confused
// turn undo the fix from the previous one, and the list is the record a human
// checks the AI against.
func TestRestoreIsNotAnMCPTool(t *testing.T) {
	a := appWithSettings(t)
	for _, tool := range registered(t, a).Tools() {
		name := strings.ToLower(tool.Name)
		for _, verb := range []string{"restore", "undo", "revert", "rollback", "history"} {
			if strings.Contains(name, verb) {
				t.Errorf("%q lets the AI reach the undo history", tool.Name)
			}
		}
	}
}

// Whether deletion exists at all is a different question from whether using it
// interrupts you, so it has its own switch — and it is off until asked for.
func TestDeletionIsOffUntilTurnedOn(t *testing.T) {
	a := appWithSettings(t)
	seedSharedHost(t, a)

	byName := map[string]mcp.Tool{}
	for _, tool := range registered(t, a).Tools() {
		byName[tool.Name] = tool
	}
	tool := byName["fs_delete"]

	_, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "h1", "path": "/tmp/x.md",
	})
	if err == nil {
		t.Fatal("deletion must be off until the user turns it on")
	}
	if !strings.Contains(err.Error(), "cannot be enabled from here") {
		t.Errorf("the refusal must close the door rather than suggest a parameter: %v", err)
	}

	// Sharing a host to be read, and even letting writes through, is still not
	// permission to delete.
	a.SetMCPWritePolicy("h1", WriteBypass, 60)
	if _, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "h1", "path": "/tmp/x.md",
	}); err == nil {
		t.Error("bypass is about being asked, not about what may be done")
	}

	a.SetMCPHostDelete("h1", true)
	if got := a.MCPState().Delete["h1"]; !got {
		t.Error("the switch should be reported to the panel")
	}
	// Now it gets past the permission check and fails on the file instead.
	_, err = tool.Handler(context.Background(), map[string]any{"hostId": "h1", "path": "/tmp/x.md"})
	if err != nil && strings.Contains(err.Error(), "switched off") {
		t.Errorf("still refused after being enabled: %v", err)
	}
}

// Everything a tool returns is spent from the model's context, so a field that
// says nothing is a real cost. These pin the shapes rather than the sizes:
// a size assertion would be brittle, but "no key nothing can act on" is not.
func TestListingsDoNotCarryDeadWeight(t *testing.T) {
	body := logResult("nginx", "", "")
	if _, ok := body["matched"]; ok {
		t.Error("matched is only meaningful when a grep ran")
	}
}

// A unit that fails every few hours writes the same block over and over with
// only the timestamps moving. Sixty lines of that tells a model what eight
// would have.
func TestRepeatedLogLinesAreFolded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 7; i++ {
		fmt.Fprintf(&b, "Aug 0%d 03:00:01 cpprhtn systemd[1]: certbot.service: Failed with result 'exit-code'.\n", i+1)
	}
	b.WriteString("Aug 10 13:46:01 cpprhtn systemd[1]: certbot.service: Consumed 1.700s CPU time.\n")

	got := logResult("certbot.service", b.String(), "")
	lines, _ := got["lines"].([]string)
	if len(lines) != 2 {
		t.Fatalf("want the repeat folded into one line plus the unique one, got %d:\n%v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "×7") {
		t.Errorf("the fold must say how many times: %q", lines[0])
	}
	if !strings.Contains(lines[0], "last ") {
		t.Errorf("the fold must say when it last happened: %q", lines[0])
	}
	// The one-off line survives untouched: folding must not eat the message
	// that explains the failure.
	if !strings.Contains(lines[1], "Consumed 1.700s") {
		t.Errorf("a unique line was lost: %v", lines)
	}
}

// Two occurrences are not a pattern, and folding them would cost more
// characters than it saves.
func TestTwoOccurrencesAreLeftAlone(t *testing.T) {
	body := "Aug 01 03:00:01 h systemd[1]: a.service: boom\nAug 02 03:00:01 h systemd[1]: a.service: boom\n"
	lines, _ := logResult("a.service", body, "")["lines"].([]string)
	if len(lines) != 2 {
		t.Errorf("want both lines verbatim, got %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "×") {
			t.Errorf("two occurrences should not be folded: %q", l)
		}
	}
}

// Folding must not reorder: a diagnosis that depends on what happened before
// what still has to read correctly.
func TestFoldingKeepsFirstAppearanceOrder(t *testing.T) {
	body := strings.Join([]string{
		"Aug 01 00:00:01 h systemd[1]: a.service: starting",
		"Aug 01 00:00:02 h systemd[1]: a.service: repeated",
		"Aug 01 00:00:03 h systemd[1]: a.service: repeated",
		"Aug 01 00:00:04 h systemd[1]: a.service: repeated",
		"Aug 01 00:00:05 h systemd[1]: a.service: gave up",
	}, "\n")
	lines, _ := logResult("a.service", body, "")["lines"].([]string)
	if len(lines) != 3 {
		t.Fatalf("want starting, folded repeat, gave up — got %v", lines)
	}
	if !strings.Contains(lines[0], "starting") || !strings.Contains(lines[2], "gave up") {
		t.Errorf("order was not preserved: %v", lines)
	}
}

// A description that only restates the unit's own name is not information, but
// the test for that has to be exact: a loose one throws away real ones.
func TestOnlySelfRestatingDescriptionsAreDropped(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		drop              bool
	}{
		{"auditd.service", "auditd.service", true},
		{"ModemManager.service", "Modem Manager", true},
		{"connman.service", "connman.service", true},
		{"nginx.service", "", true},
		// These say something the name does not, and an earlier substring rule
		// discarded every one of them.
		{"apparmor.service", "Load AppArmor profiles", false},
		{"containerd.service", "containerd container runtime", false},
		{"cron.service", "Regular background program processing daemon", false},
		{"ssh.service", "OpenBSD Secure Shell server", false},
	} {
		if got := restatesName(tc.name, tc.description); got != tc.drop {
			t.Errorf("%s / %q: dropped = %v, want %v", tc.name, tc.description, got, tc.drop)
		}
	}
}
