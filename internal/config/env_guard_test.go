package config

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCredentialFilesTracked fails if a developer credential file has been
// committed.
//
// .gitignore is a request, not a guarantee: `git add -f`, an editor that stages
// everything, or a rename past the pattern all get a secret into history, where
// removing it means rewriting published commits. A test is the guard, in keeping
// with the rule that guards live in code rather than in documentation.
//
// The check runs against the index, not the working tree — an ignored file that
// exists locally is the expected state and must not fail the build.
func TestNoCredentialFilesTracked(t *testing.T) {
	// From the repository root, explicitly. `go test` runs with the package
	// directory as the working directory, and `git ls-files` lists only what is
	// below the current directory — so the obvious version of this test inspects
	// internal/config/ and passes no matter what sits at the repo root. It did:
	// force-adding .env.local did not fail it until this line was added.
	rootOut, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		// Not a git checkout (a release tarball, a vendored copy). Nothing to
		// guard, and failing here would break builds that are perfectly fine.
		t.Skipf("not a git checkout: %v", err)
	}
	root := strings.TrimSpace(string(rootOut))

	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files unavailable: %v", err)
	}

	// Matched on the base name so a move to another directory does not slip past.
	forbidden := []string{".env.local", "id_litedeck_dev", "hosts.json"}

	for _, path := range strings.Split(string(out), "\x00") {
		if path == "" {
			continue
		}
		base := filepath.Base(path)
		for _, bad := range forbidden {
			// .env.local.example is committed on purpose and holds no values;
			// anything that merely starts with a forbidden name is not.
			if base == bad || strings.HasPrefix(base, bad+".") && !strings.HasSuffix(base, ".example") {
				t.Errorf("credential file is tracked by git: %s\n"+
					"remove it from the index (git rm --cached) before committing; "+
					"if it was ever pushed, treat the secret as compromised and rotate it", path)
			}
		}
		if strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_rsa") {
			if !strings.HasSuffix(base, ".pub") {
				t.Errorf("private key is tracked by git: %s", path)
			}
		}
	}
}
