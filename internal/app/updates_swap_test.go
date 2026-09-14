package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A swap that fails after the app has quit must put the app back.
//
// The helper moves the old build aside and the new one in. Either move can
// fail — a read-only DMG, a /Applications the user does not own, macOS App
// Translocation, Defender holding the file open — and the old script exited 1
// at that point with the app already gone. The user pressed "Install update"
// and their window disappeared for good.
func TestSwapHelperPutsTheAppBackWhenTheMoveFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sh helper is not the one that runs here")
	}
	// Every path that gives up after the wait loop has to relaunch first.
	for _, want := range []string{"relaunch_and_die", `mv "$2" "$old" || relaunch_and_die`} {
		if !strings.Contains(swapSh, want) {
			t.Errorf("the sh helper has no %q — a failed swap leaves nothing running:\n%s", want, swapSh)
		}
	}
	// The Windows one too. Its failure mode is the common one: Defender.
	for _, want := range []string{`|| (start "" "%~2" & exit /b 1)`} {
		if !strings.Contains(swapCmd, want) {
			t.Errorf("the cmd helper has no %q:\n%s", want, swapCmd)
		}
	}
}

// Refused before quitting rather than recovered after. The helper can put the
// old build back, but the user still watches the window vanish and return for
// nothing — and on a read-only mount it was never going to work.
func TestApplyRefusesWhereItCannotWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "litedeck")
	if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := canReplace(target); err != nil {
		t.Fatalf("refused a directory it can write: %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere, so there is nothing to refuse")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := canReplace(target); err == nil {
		t.Error("accepted a directory it cannot write — the app would quit and not come back")
	}
}
