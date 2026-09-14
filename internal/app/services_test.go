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
