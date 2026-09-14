package app

import (
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/webrpc"
)

// The web transport reflects over the App to expose its bindings, so the danger
// is a method becoming remotely reachable that never should be. A full pin of
// the exposed set (there are ~86) would break on every ordinary new binding and
// train people to update it without thinking — the opposite of a review gate.
// The bindings are, after all, exactly what the desktop UI already does; a new
// one is not a new risk. What IS a risk is narrow and structural, so that is
// what this floor guards:
//
//   - lifecycle methods (Startup/Shutdown), caught by the "takes a
//     context.Context" rule, must stay unreachable — /rpc/Shutdown would tear
//     down every connection;
//   - the Bench* render-benchmark spike must stay unreachable.
//
// Run against the real App, not a stand-in, so a real method that slips the net
// is caught here rather than in production.
func TestWebRPCExposureFloor(t *testing.T) {
	exposed := map[string]bool{}
	for _, m := range webrpc.New(New()).Methods() {
		exposed[m] = true
	}

	// Nothing that tears the process down, or that is dev-only spike infra.
	for _, banned := range []string{"Startup", "Shutdown"} {
		if exposed[banned] {
			t.Errorf("%s is reachable over /rpc — it must never be", banned)
		}
	}
	for m := range exposed {
		if strings.HasPrefix(m, "Bench") {
			t.Errorf("%s (benchmark spike) is reachable over /rpc", m)
		}
	}

	// The bindings the whole UI depends on must be reachable, or the web app
	// cannot boot or connect. A representative floor, not an exhaustive list.
	for _, needed := range []string{
		"Bootstrap", "Platform", "ListHosts", "ConnectHost", "DisconnectHost",
		"AnswerHostKey", "AnswerSecret", "CommandLog", "HostMetrics",
		"OpenTerminal", "WriteTerminal", "SaveTextFile", "DeletePaths",
	} {
		if !exposed[needed] {
			t.Errorf("%s is not reachable over /rpc — the web UI needs it", needed)
		}
	}

	// A catastrophic drop (the reflection walk breaks, exposing nothing or
	// almost nothing) should fail loudly rather than silently shipping a UI
	// that cannot call anything.
	if n := len(exposed); n < 60 {
		t.Errorf("only %d methods exposed — the binding surface looks broken", n)
	}
}

// Every binding that reaches the local filesystem must refuse in server mode.
//
// "Local" is the machine running the app. On the desktop that is the person's
// own laptop and these are ordinary features; in server mode it is the server
// box, and /rpc turns them into "read and write the server's disk" for anyone
// who can log into the web UI. A web session already holds every SSH host it
// has credentials for, so the new thing it gains here is the server process's
// own filesystem — its keys, its settings.json with the MCP token, its
// hosts.json.
//
// Pinned by name rather than detected, because there is no way to detect it:
// the list is what somebody has to think about when they add a binding that
// opens a file. A new one failing this test is the point.
func TestLocalFilesystemBindingsRefuseInServerMode(t *testing.T) {
	a := New()
	a.headless = true

	if _, err := a.StartUpload("h", []string{"/etc/hostname"}, "/tmp"); err == nil {
		t.Error("StartUpload read the server's own disk in server mode")
	}
	if _, err := a.StartDownload("h", []string{"/etc/hostname"}, t.TempDir()); err == nil {
		t.Error("StartDownload wrote to the server's own disk in server mode")
	}
	if _, err := a.ImportSSHConfig(); err == nil {
		t.Error("ImportSSHConfig read the server account's ~/.ssh/config in server mode")
	}
	if got := a.ReportSample(RenderSample{}); got != "" {
		t.Errorf("ReportSample wrote under the server account's cache: %q", got)
	}

	// The refusal has to come from the mode. Each message names server mode, so
	// a guard that started refusing everywhere would still pass the checks
	// above and fail here — which is the failure that removes a feature rather
	// than fixing one.
	for name, err := range map[string]error{
		"StartUpload":     mustErr(a.StartUpload("h", []string{"/etc/hostname"}, "/tmp")),
		"StartDownload":   mustErr(a.StartDownload("h", []string{"/etc/hostname"}, t.TempDir())),
		"ImportSSHConfig": errOf(a.ImportSSHConfig()),
	} {
		if err == nil || !strings.Contains(err.Error(), "서버 모드") {
			t.Errorf("%s refused for some other reason: %v", name, err)
		}
	}
}

func mustErr(_ []string, err error) error            { return err }
func errOf(_ ImportSSHConfigResult, err error) error { return err }
