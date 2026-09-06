package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/rollback"
)

// run_command against a real server.
//
// The gate tests prove it refuses. This proves it works, which is the half a
// refusal test cannot reach: a tool broken badly enough to refuse everything
// passes every one of them.
func TestRunCommandRunsAndIsRecorded(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	a.rollback = rollback.Open(a.configDir)
	a.SetMCPHost("fixture", true)
	a.SetMCPHostExec("fixture", true)

	asked := make(chan struct{}, 1)
	previous := a.emit
	a.emit = func(event string, payload any) {
		if event == "prompt:mcpwrite" {
			if p, ok := payload.(MCPWritePrompt); ok {
				select {
				case asked <- struct{}{}:
				default:
				}
				go func() { _ = a.AnswerMCPWrite(p.ID, true) }()
			}
			return
		}
		previous(event, payload)
	}

	tool := toolNamed(t, a, "run_command")
	out, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "fixture",
		// A pipe on purpose. Argv-only execution cannot carry one, and that is
		// most of the reason a person reaches for this tool instead of the
		// narrow ones.
		"command": "printf 'a\nb\nc\n' | wc -l",
	})
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result is %T, not a map", out)
	}
	if m["ok"] != true || m["exitCode"] != 0 {
		t.Fatalf("command failed: %+v", m)
	}
	if got := strings.TrimSpace(m["stdout"].(string)); got != "3" {
		t.Errorf("stdout = %q, want 3 — the pipe did not survive", got)
	}

	select {
	case <-asked:
	default:
		t.Error("the command ran without anyone being asked")
	}

	// The point of putting execution here rather than leaving it to the
	// terminal tab: the terminal records nothing, and this has to.
	var found bool
	for _, e := range a.CommandLog() {
		if strings.Contains(e.Line, "wc -l") && e.Status == "ok" {
			found = true
		}
	}
	if !found {
		t.Error("the command is not in the Command Log — the tool's whole justification is that it would be")
	}
}

// A non-zero exit is an answer, not a transport failure. A model that gets an
// error instead of the exit code cannot tell "grep found nothing" from "the
// connection dropped", and will retry the one it should not.
func TestRunCommandReportsExitCodeRatherThanFailing(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	a.rollback = rollback.Open(a.configDir)
	a.SetMCPHost("fixture", true)
	a.SetMCPHostExec("fixture", true)

	previous := a.emit
	a.emit = func(event string, payload any) {
		if event == "prompt:mcpwrite" {
			if p, ok := payload.(MCPWritePrompt); ok {
				go func() { _ = a.AnswerMCPWrite(p.ID, true) }()
			}
			return
		}
		previous(event, payload)
	}

	tool := toolNamed(t, a, "run_command")
	out, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "fixture", "command": "exit 3",
	})
	if err != nil {
		t.Fatalf("a non-zero exit was reported as a tool error: %v", err)
	}
	m := out.(map[string]any)
	if m["ok"] != false {
		t.Errorf("ok = %v, want false", m["ok"])
	}
	if m["exitCode"] != 3 {
		t.Errorf("exitCode = %v, want 3", m["exitCode"])
	}
}

// A declined command must not have run. The error alone does not prove that:
// an implementation that runs first and asks afterwards returns the same error
// and leaves the server changed.
func TestDeclinedCommandNeverRuns(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	a.rollback = rollback.Open(a.configDir)
	a.SetMCPHost("fixture", true)
	a.SetMCPHostExec("fixture", true)

	previous := a.emit
	a.emit = func(event string, payload any) {
		if event == "prompt:mcpwrite" {
			if p, ok := payload.(MCPWritePrompt); ok {
				go func() { _ = a.AnswerMCPWrite(p.ID, false) }()
			}
			return
		}
		previous(event, payload)
	}

	mark := "/tmp/litedeck-declined-marker"
	t.Cleanup(func() { a.DeletePaths("fixture", []string{mark}, false, "") })

	tool := toolNamed(t, a, "run_command")
	if _, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "fixture", "command": "touch " + mark,
	}); err == nil {
		t.Fatal("a declined command reported success")
	}
	if st, _ := a.StatPath("fixture", mark); st.Exists {
		t.Error("the command ran anyway — approval is being asked after the fact")
	}
}

// Output has to be cut somewhere, and the answer has to say so. A truncated
// answer that looks complete is worse than no answer: a model reading the tail
// of a log it never received will describe what it half saw.
func TestLongOutputIsCutAndSaysSo(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	a.rollback = rollback.Open(a.configDir)
	a.SetMCPHost("fixture", true)
	a.SetMCPHostExec("fixture", true)
	autoApproveWrites(t, a)

	tool := toolNamed(t, a, "run_command")
	out, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "fixture", "command": "head -c 200000 /dev/zero | tr '\\0' 'x'",
	})
	if err != nil {
		t.Fatalf("run_command: %v", err)
	}
	m := out.(map[string]any)
	if got := len(m["stdout"].(string)); got != maxCommandOutput {
		t.Errorf("stdout = %d bytes, want the cap %d", got, maxCommandOutput)
	}
	if m["truncated"] != true {
		t.Errorf("output was cut without saying so: %+v", m)
	}
}

// A command that outruns its wait has to come back as a timeout that names
// itself. "context deadline exceeded" tells a model nothing it can act on, and
// the thing it most needs to know is not in it: the command is still running
// on the server.
func TestSlowCommandTimesOutAndSaysWhatHappened(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	a.rollback = rollback.Open(a.configDir)
	a.SetMCPHost("fixture", true)
	a.SetMCPHostExec("fixture", true)
	autoApproveWrites(t, a)

	started := time.Now()
	tool := toolNamed(t, a, "run_command")
	_, err := tool.Handler(context.Background(), map[string]any{
		"hostId": "fixture", "command": "sleep 20", "timeoutSeconds": float64(1),
	})
	if err == nil {
		t.Fatal("a command that outran its wait reported success")
	}
	if waited := time.Since(started); waited > 10*time.Second {
		t.Errorf("waited %v — the timeout was not applied", waited)
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("error does not say the command outlives the wait: %v", err)
	}
}

// autoApproveWrites answers every approval dialog with yes.
func autoApproveWrites(t *testing.T, a *App) {
	t.Helper()
	previous := a.emit
	a.emit = func(event string, payload any) {
		if event == "prompt:mcpwrite" {
			if p, ok := payload.(MCPWritePrompt); ok {
				go func() { _ = a.AnswerMCPWrite(p.ID, true) }()
			}
			return
		}
		previous(event, payload)
	}
}
