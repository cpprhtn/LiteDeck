package app

import (
	"context"
	"strings"
	"testing"

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
