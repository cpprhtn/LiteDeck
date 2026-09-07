package app

import (
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
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

// Every row of an untimed history has a nil time, and the old check reached
// through the pointer to ask whether it was zero. That is a panic, on the
// common case, in the binding the whole feature goes through — and no fixture
// caught it because the container has no history file to read.
func TestUntimedHistoryDoesNotPanic(t *testing.T) {
	if anyTimed(adapter.ParseBashHistory("docker ps\nls -la\n")) {
		t.Error("a file with no timestamps was reported as timed")
	}
	if !anyTimed(adapter.ParseBashHistory("#1788000000\ndocker ps\n")) {
		t.Error("a file with a timestamp was reported as untimed")
	}
	// Mixed: bash writes a stamp only for the commands that had one.
	if !anyTimed(adapter.ParseBashHistory("ls\n#1788000000\ndocker ps\n")) {
		t.Error("a stamp after an unstamped line was missed")
	}
}

// The whole path, on a real server: SFTP writes a history file, the binding
// reads it back, and every line's directory is checked against where a shell
// would actually have been standing.
//
// The unit tests cover the walker. This covers everything around it — the read,
// the parse, the home the replay starts from, the masking and the ordering —
// which is where the last two defects in this feature actually lived.
func TestShellHistoryResolvesEveryOperandFormOnARealServer(t *testing.T) {
	a := connectedApp(t)
	a.settings = config.OpenSettings(a.configDir)
	if err := a.SetShellHistoryAllowed("fixture", true); err != nil {
		t.Fatalf("SetShellHistoryAllowed: %v", err)
	}
	home, err := a.HomeDir("fixture")
	if err != nil {
		t.Fatalf("HomeDir: %v", err)
	}

	// Written newest-last, the way a shell appends.
	// Every operand is exercised from somewhere it can be told apart from. A
	// `cd ~/work` run while already at home would pass even if the code joined
	// onto the current directory instead of home, which is exactly the bug it
	// is here to catch.
	lines := []string{
		"cd work",            // relative, from home
		"echo relative",      //
		"cd sub/deeper",      // multi-segment relative
		"echo deep",          //
		"cd ../..",           // back up two
		"echo up",            //
		"cd /etc",            // absolute
		"echo absolute",      //
		"cd ~/work",          // under home, from /etc — not from home
		"echo tildesub",      //
		"cd /etc/../var",     // needs cleaning
		"echo cleaned",       //
		"cd ~",               // home, from /var
		"echo tilde",         //
		"cd /usr",            // somewhere that is not home
		"cd",                 // bare, from /usr
		"echo bare",          //
		"cd $NOWHERE",        // unfollowable
		"echo afterunknown",  //
		"cd /srv",            // absolute re-anchors
		"echo reanchored",    //
		"mysql -pHUNTER2 db", // masked
	}
	want := map[string]string{
		"relative":     home + "/work",
		"deep":         home + "/work/sub/deeper",
		"up":           home + "/work",
		"absolute":     "/etc",
		"tildesub":     home + "/work",
		"cleaned":      "/var",
		"tilde":        home,
		"bare":         home,
		"afterunknown": home, // the shell moved; this cannot say where
		"reanchored":   "/srv",
	}

	client, err := a.mgr.SFTP("fixture")
	if err != nil {
		t.Fatalf("SFTP: %v", err)
	}
	path := home + "/.bash_history"
	f, err := client.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if _, err := f.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
	t.Cleanup(func() { _ = client.Remove(path) })

	view, err := a.HostShellHistory("fixture", false)
	if err != nil {
		t.Fatalf("HostShellHistory: %v", err)
	}
	if !view.Allowed || len(view.Commands) == 0 {
		t.Fatalf("아무것도 못 읽었다: allowed=%v n=%d", view.Allowed, len(view.Commands))
	}

	got := map[string]adapter.ShellCommand{}
	for _, c := range view.Commands {
		if after, ok := strings.CutPrefix(c.Command, "echo "); ok {
			got[after] = c
		}
	}
	for marker, wantPWD := range want {
		c, ok := got[marker]
		if !ok {
			t.Errorf("%q 줄이 없다", marker)
			continue
		}
		if c.PWD != wantPWD {
			t.Errorf("%q → %q, 기대 %q", marker, c.PWD, wantPWD)
		}
	}
	// The one line after an unfollowable move must say it is unsure, and the
	// absolute move after it must put the replay back on solid ground.
	if got["afterunknown"].PWDCertain {
		t.Error("따라가지 못한 cd 뒤인데 확신한다고 되어 있다")
	}
	if !got["reanchored"].PWDCertain {
		t.Error("절대경로 cd 뒤인데 확신을 되찾지 못했다")
	}
	if view.Secrets != 1 {
		t.Errorf("가린 비밀 %d건, 기대 1건", view.Secrets)
	}
	for _, c := range view.Commands {
		if strings.Contains(c.Command, "HUNTER2") {
			t.Error("비밀번호가 가려지지 않고 나왔다")
		}
	}
}
