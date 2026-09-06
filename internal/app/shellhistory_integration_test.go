package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/config"
)

// The switch is on the binding, not on a screen.
//
// /rpc reaches every binding directly in server mode, so a feature guarded only
// by a hidden tab is a door with the handle taken off. This is the one test
// that would fail if the gate ever moved into the UI.
func TestShellHistoryIsRefusedUntilSwitchedOn(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)

	off, err := a.HostShellHistory("fixture", false)
	if err != nil {
		t.Fatalf("HostShellHistory: %v", err)
	}
	if off.Allowed {
		t.Error("a host nobody switched on reported itself as allowed")
	}
	if len(off.Commands) != 0 {
		t.Errorf("read %d commands from a host that was never switched on", len(off.Commands))
	}

	if err := a.SetShellHistoryAllowed("fixture", true); err != nil {
		t.Fatalf("SetShellHistoryAllowed: %v", err)
	}
	on, err := a.HostShellHistory("fixture", false)
	if err != nil {
		t.Fatalf("after switching on: %v", err)
	}
	if !on.Allowed {
		t.Error("the switch did not take")
	}
	t.Logf("fixture: file=%q commands=%d timed=%v secrets=%d",
		on.File, len(on.Commands), on.Timed, on.Secrets)

	// And it can be switched back off.
	if err := a.SetShellHistoryAllowed("fixture", false); err != nil {
		t.Fatalf("switching off: %v", err)
	}
	if again, _ := a.HostShellHistory("fixture", false); again.Allowed {
		t.Error("switching it off did not take")
	}
}

// Root's history is a separate ask, never a fallback. Reading it without being
// asked would hand over what somebody did after `sudo -i`, which is exactly the
// part the sudo journal cannot see.
func TestRootHistoryIsNotReadUnlessAsked(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	if err := a.SetShellHistoryAllowed("fixture", true); err != nil {
		t.Fatal(err)
	}

	plain, err := a.HostShellHistory("fixture", false)
	if err != nil {
		t.Fatalf("HostShellHistory: %v", err)
	}
	if plain.Root {
		t.Error("root's history was read without anybody asking for it")
	}
}
