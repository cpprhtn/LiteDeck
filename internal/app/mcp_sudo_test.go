package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/shellquote"
)

// MCP elevates while the lock is open, and at no other time.
//
// The rule is deliberately the one the screen already shows: SudoUnlocked draws
// the padlock, so what the person sees is what the model gets. Anything looser
// would be a second, invisible notion of "allowed".
func TestMCPElevatesOnlyWhileTheLockIsOpen(t *testing.T) {
	a := New()
	gen := a.connGeneration("h")

	if a.mcpElevate("h") {
		t.Error("a fresh host let an MCP tool run as root")
	}

	// A password that merely worked for one action is not the user opening the
	// lock, and must not hand root to a model. The security tab draws the same
	// distinction for its own privileged half, and for the same reason: a poll
	// that happened to elevate is nobody's decision.
	a.unlocked.remember("h", gen, "pw")
	if a.mcpElevate("h") {
		t.Error("a remembered password opened MCP's path to root")
	}

	a.unlocked.put("h", gen, "pw")
	if !a.mcpElevate("h") {
		t.Error("the user opened the lock and MCP still refused to use it")
	}

	// The lock belongs to a connection. A reconnect is a different one.
	if _, ok := a.unlocked.getTurned("h", gen+1); ok {
		t.Error("the lock survived into another connection")
	}
}

// No tool call may make a password dialog appear.
//
// Nobody is watching the screen when a model decides to restart a unit. A
// dialog that arrives unbidden gets answered for the wrong reason, or teaches
// its reader to answer any dialog at all — which is worse than the thing it
// asked about. So the MCP path is wired to the variant that gives up instead.
func TestMCPCallsCannotReachThePasswordPrompt(t *testing.T) {
	src := readAppSource(t, "mcp_write.go")
	for _, call := range []string{"a.serviceAction(", "a.containerAction(", "a.killProcess("} {
		i := strings.Index(src, call)
		if i < 0 {
			t.Errorf("%s is no longer how MCP performs that action — check this still holds", call)
			continue
		}
		line := src[i:]
		if end := strings.IndexByte(line, '\n'); end > 0 {
			line = line[:end]
		}
		if !strings.Contains(line, "a.mcpElevate(") {
			t.Errorf("%s does not go through the lock: %s", call, strings.TrimSpace(line))
		}
		// The last argument is mayAsk. false is what forbids the dialog.
		if !strings.Contains(line, ", false)") {
			t.Errorf("%s may raise a password dialog: %s", call, strings.TrimSpace(line))
		}
	}

	// And the refusal is a question the UI already knows, not a Go error string.
	res := execFailure(ErrSudoLocked)
	if !res.NeedsElevation {
		t.Error("a shut lock did not come back as needing root")
	}
	if strings.Contains(res.Error, "app:") {
		t.Errorf("the model is shown an internal error: %q", res.Error)
	}
	if !errors.Is(ErrSudoLocked, ErrSudoLocked) {
		t.Error("ErrSudoLocked is not comparable, so callers cannot tell it apart")
	}
}

// The model is told the one thing that fixes it, and it is not a tool call.
//
// There is deliberately no tool that opens the lock. A model that could reach
// for one would be a model asking somebody for their sudo password, which is
// the exact shape of the thing this design exists to avoid.
func TestTheLockedAnswerPointsAtTheApp(t *testing.T) {
	if !strings.Contains(sudoLockedNote, "LiteDeck") {
		t.Errorf("the note does not say where to do it: %q", sudoLockedNote)
	}
	if !strings.Contains(strings.ToLower(sudoLockedNote), "never here") {
		t.Errorf("the note does not say the password is not typed to the model: %q", sudoLockedNote)
	}
	for _, tool := range registered(t, appWithSettings(t)).Tools() {
		name := strings.ToLower(tool.Name)
		for _, forbidden := range []string{"unlock", "sudo", "password", "elevate"} {
			if strings.Contains(name, forbidden) {
				t.Errorf("there is a tool called %q — a model must not be able to reach for the lock",
					tool.Name)
			}
		}
	}
}

// sudo is visible in the Command Log and the password is not.
//
// The log is the only place a person sees what this app did on their behalf,
// and "restarted nginx" and "restarted nginx as root" are different sentences.
// The logged line is the argv, so the word is there for free — and the password
// is not, because it travels on stdin. Both halves are pinned here because both
// are load-bearing and neither is obvious from reading the call.
func TestSudoShowsUpInTheCommandLineAndThePasswordDoesNot(t *testing.T) {
	line, err := shellquote.Join("sudo", "-S", "-p", "", "--", "systemctl", "restart", "nginx.service")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if !strings.HasPrefix(line, "sudo ") {
		t.Errorf("the logged line does not begin with sudo: %q", line)
	}
	if strings.Contains(line, "hunter2") {
		t.Error("the password reached argv")
	}
	// -p '' keeps sudo's own prompt out of stdout, where it would be read as
	// part of the command's output.
	if !strings.Contains(line, "-p ''") && !strings.Contains(line, `-p ""`) {
		t.Errorf("sudo will print its prompt into stdout: %q", line)
	}
}

// The one thing that cannot be put back does not get root.
//
// Every other write has something behind it — a rollback copy, or a unit that
// can be started again. A file deleted on a server this app does not back up is
// gone, and giving that root as well widens a single mistaken tool call from a
// directory to the filesystem.
func TestDeletionIsNeverElevated(t *testing.T) {
	src := readAppSource(t, "mcp_write.go")
	i := strings.Index(src, `"fs_delete"`)
	if i < 0 {
		t.Fatal("fs_delete is gone from the tool set — this test needs rewriting")
	}
	rest := src[i:]
	if end := strings.Index(rest, "\n\tmcp.AddTool("); end > 0 {
		rest = rest[:end]
	}
	if strings.Contains(rest, "mcpElevate") {
		t.Error("fs_delete asks for root")
	}
}

func readAppSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
