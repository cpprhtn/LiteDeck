package app

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// A live endpoint for verifying against a real MCP client.
//
// Skipped unless LITEDECK_MCP_LIVE is set, so it never runs in CI. It exists
// because the unit tests prove the protocol is right and prove nothing about
// whether Claude Code can actually talk to it — the distinction that has cost
// this project four releases before (see arch A-31).
//
//	LITEDECK_MCP_LIVE=1 LITEDECK_MCP_PORT=8779 go test ./internal/app/ -run TestLiveMCPEndpoint -v -timeout 30m
func TestLiveMCPEndpoint(t *testing.T) {
	if os.Getenv("LITEDECK_MCP_LIVE") == "" {
		t.Skip("set LITEDECK_MCP_LIVE=1 to stand up a live endpoint")
	}

	dir := os.Getenv("LITEDECK_MCP_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	a := New()
	a.configDir = dir
	a.settings = config.OpenSettings(dir)
	store, err := config.Open(dir)
	if err != nil {
		t.Fatalf("open hosts: %v", err)
	}
	a.hosts = store
	a.mgr = sshcore.NewManager(sshcore.ManagerOptions{}, a.emitConnectionState)
	defer a.mgr.Close()

	// Two hosts: one shared, one not. The refusal path is the more important of
	// the two to see a real client hit.
	for _, h := range []config.Host{
		{ID: "demo", Name: "demo", Hostname: "10.0.0.5", Port: 22, User: "junwon",
			Auth: []config.AuthMethod{config.AuthAgent}},
		{ID: "prod", Name: "prod-locked", Hostname: "10.0.0.6", Port: 22, User: "junwon",
			Auth: []config.AuthMethod{config.AuthAgent}},
	} {
		if err := store.Upsert(h); err != nil {
			t.Fatalf("seed %s: %v", h.ID, err)
		}
	}
	a.SetMCPHost("demo", true)

	if port := os.Getenv("LITEDECK_MCP_PORT"); port != "" {
		s := a.settings.Get().MCP
		fmt.Sscanf(port, "%d", &s.Port)
		if err := a.settings.SetMCP(s); err != nil {
			t.Fatalf("set port: %v", err)
		}
	}

	// The approval wait is two minutes in the app; a live check should not have
	// to sit through it to see the gate hold.
	if ms := os.Getenv("LITEDECK_MCP_APPROVAL_MS"); ms != "" {
		var n int
		fmt.Sscanf(ms, "%d", &n)
		defer WriteApprovalTimeoutForTest(time.Duration(n) * time.Millisecond)()
	}
	// A second host that is shared for reading but left at the default write
	// mode, so the gate can be exercised without touching a real server.
	a.SetMCPHost("demo", true)

	state := a.SetMCPEnabled(true)
	defer a.stopMCP()
	if !state.Running {
		t.Fatalf("endpoint did not start: %s", state.Error)
	}

	fmt.Printf("\nLIVE_URL=%s\nLIVE_TOKEN=%s\nLIVE_SNIPPET=%s\n\n",
		state.URL, state.Token, state.Snippet)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	// What the client actually asked for, so the run can be checked afterwards.
	for _, e := range a.CommandLog() {
		if e.Origin == "ai" {
			fmt.Printf("AI_CALL %s %s\n", e.HostID, e.Line)
		}
	}
}
