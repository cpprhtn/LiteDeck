package app

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Compose against a real daemon and a real project (§4.5).
//
// The unit tests in the adapter prove the label string is parsed correctly from
// output that was captured once. These prove the rest of the claim: that the
// labels are actually there in what this fixture's Docker emits, and that
// addressing a project by name alone — no compose file, no working directory —
// restarts the containers it should and leaves the others running.

const composeFile = `services:
  web:
    image: alpine:3.20
    command: sleep 600
  cache:
    image: alpine:3.20
    command: sleep 600
`

// startComposeProject brings up a two-service project inside the fixture and
// returns nothing: everything the tests need comes back through LiteDeck.
//
// The file is written under /srv and the project is started from there, so that
// the later `--project-name` calls are genuinely running from somewhere else.
func startComposeProject(t *testing.T, project string) {
	t.Helper()
	runInDind(t, "pull", "-q", "alpine:3.20")

	script := "mkdir -p /srv/" + project + " && cat > /srv/" + project +
		"/compose.yaml <<'YAML'\n" + composeFile + "YAML"
	if out, err := exec.Command("docker", "exec", dindID, "sh", "-c", script).
		CombinedOutput(); err != nil {
		t.Fatalf("write compose file: %v\n%s", err, out)
	}

	runInDind(t, "compose", "--project-directory", "/srv/"+project,
		"--project-name", project, "up", "-d")
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", dindID, "docker", "compose",
			"--project-name", project, "down", "-t", "1").Run()
	})
}

// startedAt is Docker's own record of when the container last came up. The
// Status column ("Up 3 seconds") is too coarse to tell a restart from a
// container that was already running.
func startedAt(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", dindID, "docker", "inspect",
		"-f", "{{.State.StartedAt}}", name).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect %s: %v\n%s", name, err, out)
	}
	return strings.TrimSpace(string(out))
}

// waitRestarted gives the daemon a moment to record the new start time. The
// action returns when the CLI does, which is not quite the same instant.
func waitRestarted(t *testing.T, name, before string) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if startedAt(t, name) != before {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func TestListContainersCarriesComposeLabelsFromARealDaemon(t *testing.T) {
	a := dockerApp(t)
	startComposeProject(t, "litedeckshop")

	// A container started outside Compose, as the control: it must come back
	// with no project at all, or the menu would appear on things it cannot act
	// on.
	runInDind(t, "run", "-d", "--name", "litedeck-solo", "alpine:3.20", "sleep", "600")
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", dindID, "docker", "rm", "-f", "litedeck-solo").Run()
	})

	cs, err := a.ListContainers("dind")
	if err != nil {
		t.Fatal(err)
	}

	web, ok := findContainer(cs, "litedeckshop-web-1")
	if !ok {
		t.Fatalf("litedeckshop-web-1 missing from %d containers", len(cs))
	}
	if web.Compose == nil {
		t.Fatal("web has no compose project — the labels did not survive the round trip")
	}
	if web.Compose.Project != "litedeckshop" || web.Compose.Service != "web" {
		t.Errorf("Compose = %+v, want project litedeckshop service web", *web.Compose)
	}
	if web.Compose.OneOff {
		t.Error("OneOff = true for a declared service")
	}

	solo, ok := findContainer(cs, "litedeck-solo")
	if !ok {
		t.Fatal("litedeck-solo missing")
	}
	if solo.Compose != nil {
		t.Errorf("a plain `docker run` container came back with %+v", *solo.Compose)
	}
}

// The scope actually scopes: restarting one service must leave its siblings
// running, and restarting the project must take both.
func TestComposeRestartScopes(t *testing.T) {
	a := dockerApp(t)
	startComposeProject(t, "litedeckscope")

	const web, cache = "litedeckscope-web-1", "litedeckscope-cache-1"

	webBefore, cacheBefore := startedAt(t, web), startedAt(t, cache)
	if res := a.ComposeAction("dind", "litedeckscope", "cache", "restart", false); !res.OK {
		t.Fatalf("ComposeAction on one service: %+v", res)
	}
	if !waitRestarted(t, cache, cacheBefore) {
		t.Error("the service asked for did not restart")
	}
	if got := startedAt(t, web); got != webBefore {
		t.Errorf("restarting one service also restarted %s (%s → %s)", web, webBefore, got)
	}

	webBefore, cacheBefore = startedAt(t, web), startedAt(t, cache)
	if res := a.ComposeAction("dind", "litedeckscope", "", "restart", false); !res.OK {
		t.Fatalf("ComposeAction on the project: %+v", res)
	}
	if !waitRestarted(t, web, webBefore) {
		t.Error("the project restart missed web")
	}
	if !waitRestarted(t, cache, cacheBefore) {
		t.Error("the project restart missed cache")
	}
}

// The premise the whole design rests on: `--project-name` alone is enough.
// LiteDeck never reads the compose file, and the file may sit somewhere this
// account cannot reach — so if this ever needs the file, the feature is broken
// for exactly the servers it matters on.
func TestComposeRestartNeedsNoComposeFile(t *testing.T) {
	a := dockerApp(t)
	startComposeProject(t, "litedeckgone")

	// Take the file away entirely. Compose still knows the project from the
	// labels on the running containers.
	if out, err := exec.Command("docker", "exec", dindID,
		"rm", "-rf", "/srv/litedeckgone").CombinedOutput(); err != nil {
		t.Fatalf("remove the compose file: %v\n%s", err, out)
	}

	before := startedAt(t, "litedeckgone-web-1")
	if res := a.ComposeAction("dind", "litedeckgone", "", "restart", false); !res.OK {
		t.Fatalf("ComposeAction with no compose file present: %+v", res)
	}
	if !waitRestarted(t, "litedeckgone-web-1", before) {
		t.Error("nothing restarted")
	}
}

func TestComposeActionRejectsWhatIsNotOnTheAllowlist(t *testing.T) {
	a := dockerApp(t)

	// `down` would delete containers and networks. It is not a lifecycle verb
	// and must not be reachable through this entry point.
	for _, action := range []string{"down", "up", "run", "exec", "rm"} {
		if res := a.ComposeAction("dind", "shop", "", action, false); res.OK {
			t.Errorf("ComposeAction accepted %q", action)
		}
	}
	// And a project name is required — an empty one would address whatever
	// Compose guesses from the working directory.
	if res := a.ComposeAction("dind", "", "", "restart", false); res.OK {
		t.Error("ComposeAction accepted an empty project")
	}
}

// HasCompose has to be false when the plugin is missing, or the UI offers a
// menu whose every entry fails. Checked against the fixture, which has it.
func TestDetectFindsCompose(t *testing.T) {
	a := dockerApp(t)
	info, err := a.DetectHost("dind")
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasDocker {
		t.Fatal("HasDocker = false on the docker fixture")
	}
	if !info.HasCompose {
		t.Error("HasCompose = false, but the fixture ships the v2 plugin")
	}
}
