package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// A refusal has to be recognised as one, or the button that would fix it is
// never offered.
//
// `kill` and `renice` are shell builtins: ending another user's process comes
// back as "bash: line 1: kill: (78) - Operation not permitted", which none of
// the systemd-shaped markers matched. The process tab printed the raw line with
// no "retry as administrator" next to it.
func TestPermissionDeniedRecognisesEPERM(t *testing.T) {
	for _, stderr := range []string{
		"bash: line 1: kill: (78) - Operation not permitted",
		"renice: failed to set priority for 78 (process ID): Operation not permitted",
		"kill: (1) - Not owner",
		"Failed to restart nginx.service: Access denied",
		"Failed to restart nginx.service: Interactive authentication required.",
		"rm: cannot remove '/etc/hosts': Permission denied",
	} {
		if !isPermissionDenied(&sshcore.Result{Stderr: []byte(stderr)}) {
			t.Errorf("not recognised as a permission problem, so no retry is offered:\n  %s", stderr)
		}
	}

	// And an ordinary failure must not grow a privilege button it cannot use.
	for _, stderr := range []string{
		"kill: (99) - No such process",
		"Failed to restart nope.service: Unit nope.service not found.",
		"",
	} {
		if isPermissionDenied(&sshcore.Result{Stderr: []byte(stderr)}) {
			t.Errorf("offered administrator for something privileges will not fix:\n  %s", stderr)
		}
	}
}

// A sudo password that worked once must not be asked for again on the same
// connection — and must not open the security tab's locked half by itself.
//
// Two separate promises that used to be one field. The friction: restart a unit
// with the password, ask to read its log, and the same dialog came back a
// second later, because only the lock button ever filled the store. The danger
// of fixing that carelessly: a polled read that elevates would then report the
// tab as unlocked and reveal the privileged half nobody asked for.
func TestRememberedSudoPasswordIsNotATurnedLock(t *testing.T) {
	a := New()
	const gen = 7

	a.unlocked.remember("h", gen, "pw")
	if got, ok := a.unlocked.get("h", gen); !ok || got != "pw" {
		t.Error("a password that worked was not remembered, so the next action asks again")
	}
	if a.unlocked.isTurned("h", gen) {
		t.Error("remembering a password reported the lock as turned")
	}
	if _, ok := a.unlocked.getTurned("h", gen); ok {
		t.Error("the privileged half opened on a password nobody asked to use there")
	}

	// Turning it is what opens the tab.
	a.unlocked.put("h", gen, "pw")
	if !a.unlocked.isTurned("h", gen) {
		t.Error("turning the lock did not take")
	}
	// And a later remember must not close it again.
	a.unlocked.remember("h", gen, "pw")
	if !a.unlocked.isTurned("h", gen) {
		t.Error("an action downgraded a lock the user had opened")
	}

	// A new connection keeps neither.
	if _, ok := a.unlocked.get("h", gen+1); ok {
		t.Error("the password survived into a different connection")
	}
}
