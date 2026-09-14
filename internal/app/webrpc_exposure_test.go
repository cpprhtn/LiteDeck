package app

import (
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/webrpc"
)

// The web transport reflects over the App to expose its bindings, so the danger
// is a method becoming remotely reachable that never should be.
//
// # Why the whole set is pinned
//
// This used to be a floor — "at least sixty methods, and never Startup,
// Shutdown or Bench*" — on the reasoning that a full pin breaks on every
// ordinary new binding and trains people to update it without thinking.
//
// That reasoning had a hole, and the hole shipped. In server mode /rpc runs on
// the server box, so a binding that reaches "the local filesystem" reaches the
// server's own disk: its keys, its settings.json with the MCP token, its
// hosts.json. StartUpload, StartDownload and ImportSSHConfig all did, and no
// floor could have said so, because each of them was an ordinary new binding
// that the desktop UI already used. The thing that makes a binding dangerous is
// not its name and not how many there are — it is a question somebody has to
// answer once, when it is added.
//
// So the set is pinned and the failure asks the question. Updating the list is
// still a one-line edit; what changed is that the edit happens while the two
// questions below are on the screen.
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

	// The pin. Sorted, so a new entry lands next to its neighbours and the diff
	// shows one line.
	pinned := map[string]bool{}
	for _, m := range webRPCPinned {
		pinned[m] = true
	}
	for m := range exposed {
		if !pinned[m] {
			t.Errorf(`%s is newly reachable over /rpc and is not in webRPCPinned.

Two questions before you add it:

  1. Does it touch the machine running the app — its disk, its clipboard, its
     OS dialogs, its process? On the desktop that is the user's own laptop and
     it is an ordinary feature. In server mode it is the server box, and /rpc
     hands it to anyone who can log into the web UI. If the answer is yes, it
     needs an a.headless guard and a line in
     TestLocalFilesystemBindingsRefuseInServerMode.
  2. Does it tear anything down, or is it dev-only spike infrastructure? Then
     it must not be bound at all.

If neither applies it is an ordinary binding: add it to webRPCPinned.`, m)
		}
	}
	for _, m := range webRPCPinned {
		if !exposed[m] {
			t.Errorf("%s is pinned but no longer reachable over /rpc — "+
				"if the binding was removed on purpose, drop it from webRPCPinned", m)
		}
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

// webRPCPinned is every method the web transport exposes today.
//
// Maintained by hand. TestWebRPCExposureFloor says what to ask before adding a
// line, and the failure it prints asks it again at the moment it matters.
var webRPCPinned = []string{
	"AnswerHostKey", "AnswerMCPWrite", "AnswerSecret", "ApplyLanguage",
	"ApplyUpdate", "Bootstrap", "CancelPrompt", "CancelTransfer",
	"CheckForUpdate", "Chmod", "ClearCommandLog", "ClearFinishedTransfers",
	"CloseTerminal", "ColdStartMs", "CommandLog", "ComposeAction",
	"ConnectHost", "ContainerAction", "ContainerLogs", "DeleteHost",
	"DeletePaths", "DetectHost", "DisconnectHost", "DownloadUpdate",
	"EndSSHSession", "FollowContainerLog", "FollowServiceLog", "ForgetSecrets",
	"HomeDir", "HostCommandHistory", "HostDigest", "HostEvents",
	"HostLogins", "HostMetrics", "HostNetwork", "HostSecurity",
	"HostShellHistory", "HostShells", "HostState", "HostSudoState",
	"HostUpdates", "ImportSSHConfig", "KillProcess", "ListContainers",
	"ListDir", "ListHosts", "ListImages", "ListProcesses",
	"ListRunningContainers", "ListSSHSessions", "ListServices", "ListTerminals",
	"ListTimers", "ListVolumes", "LockSecurity", "MCPChanges",
	"MCPState", "MakeDir", "MarkHostSeen", "OpenTerminal",
	"PendingPrompts", "PickLocalDir", "PickLocalFiles", "PickLocalUploadDir",
	"PinMCPPort", "Platform", "PreviewFile", "ProcessExists",
	"PruneImages", "ReadClipboard", "ReadTextFile", "RefreshHostNetwork",
	"RememberSecurityLogins", "RemoveContainer", "RemoveImage", "RemoveVolume",
	"RenamePath", "Renice", "ReportSample", "ResizeTerminal",
	"RestoreMCPChange", "ResumeTransfer", "RevealFromTerminal", "RotateMCPToken",
	"SSHDConfig", "SaveHost", "SaveTextFile", "SecurityLogins",
	"ServiceAction", "ServiceLogTail", "SetLanguage", "SetMCPEnabled",
	"SetMCPHost", "SetMCPHostDelete", "SetMCPHostExec", "SetMCPWritePolicy",
	"SetShellHistoryAllowed", "SetTheme", "StartDownload", "StartUpload",
	"StatPath", "StopLogStream", "SudoUnlocked", "TerminalCwd",
	"Transfers", "TypedEntered", "TypedHistory", "UnlockSecurity",
	"UpdateStatus", "UploadFile", "WriteTerminal", "WriteTextFile",
}
