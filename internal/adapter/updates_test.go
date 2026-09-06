package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goldenUpdates(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "updates", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func TestUpdatesAvailableReadsTheRealFile(t *testing.T) {
	updates, security := ParseUpdatesAvailable(goldenUpdates(t, "ubuntu-24.04-updates-available"))

	if updates != 63 {
		t.Errorf("updates = %d, want 63", updates)
	}
	// The only "security" number in this file is the ESM line, and those are
	// updates this machine cannot install — it has no subscription. Reporting
	// 5 would send somebody looking for work that is not available to them.
	if security != -1 {
		t.Errorf("security = %d, want -1: the ESM line is not a security count "+
			"this machine can act on", security)
	}
}

func TestSecurityCountIsReadWhenItIsReallyThere(t *testing.T) {
	// The standard Ubuntu wording, which the captured server did not happen to
	// have. Kept alongside the golden file rather than instead of it: the
	// fixture proves the trap, this proves the feature.
	const text = "12 updates can be applied immediately.\n" +
		"3 of these updates are security updates.\n"
	updates, security := ParseUpdatesAvailable(text)
	if updates != 12 || security != 3 {
		t.Errorf("updates=%d security=%d, want 12 and 3", updates, security)
	}
}

func TestUnreadableWordingIsNotZero(t *testing.T) {
	// The file follows the distribution and the system locale. Not parsing is a
	// normal outcome, and it must not come out as "nothing to update".
	for _, text := range []string{
		"",
		"업데이트 63개를 지금 적용할 수 있습니다.",
		"Expanded Security Maintenance for Applications is not enabled.\n",
	} {
		updates, security := ParseUpdatesAvailable(text)
		if updates != -1 || security != -1 {
			t.Errorf("%q: updates=%d security=%d, want -1 and -1", text, updates, security)
		}
	}
}

func TestRebootPackagesAreDeduplicated(t *testing.T) {
	pkgs := ParseRebootPkgs(goldenUpdates(t, "ubuntu-24.04-reboot-required.pkgs"))

	// Eleven lines, six distinct packages. The file is appended to, so
	// linux-base is in it once per kernel that wanted a restart.
	want := []string{
		"linux-image-6.8.0-134-generic",
		"linux-base",
		"linux-image-6.8.0-136-generic",
		"libc6",
		"linux-image-6.8.0-137-generic",
		"linux-image-6.8.0-138-generic",
		"linux-image-6.8.0-139-generic",
	}
	if len(pkgs) != len(want) {
		t.Fatalf("got %d packages %v, want %d", len(pkgs), pkgs, len(want))
	}
	for i := range want {
		if pkgs[i] != want[i] {
			// First-seen order is kept because it is roughly chronological, and
			// that ordering is what makes "five kernels went by" readable.
			t.Errorf("package %d = %q, want %q", i, pkgs[i], want[i])
		}
	}

	kernels := 0
	for _, p := range pkgs {
		if strings.HasPrefix(p, "linux-image-") {
			kernels++
		}
	}
	if kernels != 5 {
		t.Errorf("found %d kernels, want 5 — the number of skipped restarts is the finding", kernels)
	}
}

func TestRebootPackagesToleratesAnEmptyFile(t *testing.T) {
	if got := ParseRebootPkgs("\n\n  \n"); len(got) != 0 {
		t.Errorf("got %v, want nothing", got)
	}
}
