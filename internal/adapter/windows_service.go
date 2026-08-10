package adapter

// Windows services, mapped onto the same shape the systemd adapter produces so
// the service view needs no idea which OS it is looking at.
//
// The source is Win32_Service, not Get-Service. Get-Service serialises Status and
// StartType as bare enum integers — a real capture returns {"Status":1} — and
// carries neither the description, the binary path nor the owning PID. Win32_Service
// gives State and StartMode as strings and includes all three.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter/linuxsystemd"
	"github.com/cpprhtn/LiteDeck/internal/adapter/windowspowershell"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// winServiceRow mirrors one element of the Win32_Service projection.
//
// DelayedAutoStart earns its place: without it, StartMode "Auto" with State
// "Stopped" reads as a service that ought to be running, and on a stock Windows
// box that is a handful of false positives — edgeupdate, MapsBroker and sppsvc are
// all Automatic (Delayed Start) and are meant to sit stopped.
type winServiceRow struct {
	Name             string `json:"Name"`
	DisplayName      string `json:"DisplayName"`
	State            string `json:"State"`     // Running, Stopped, Paused, Start Pending…
	StartMode        string `json:"StartMode"` // Boot, System, Auto, Manual, Disabled
	Status           string `json:"Status"`    // OK, Degraded, Error…
	ProcessID        int    `json:"ProcessId"`
	PathName         string `json:"PathName"`
	StartName        string `json:"StartName"`
	Description      string `json:"Description"`
	AcceptStop       bool   `json:"AcceptStop"`
	AcceptPause      bool   `json:"AcceptPause"`
	DelayedAutoStart bool   `json:"DelayedAutoStart"`
	// ExitCode is 1077 (ERROR_SERVICE_NEVER_STARTED) on roughly half the services
	// of an idle machine — 134 of 263 on the box this was written against — so it
	// is a failure signal only once that value is excluded.
	ExitCode                int `json:"ExitCode"`
	ServiceSpecificExitCode int `json:"ServiceSpecificExitCode"`
}

// errServiceNeverStarted is ERROR_SERVICE_NEVER_STARTED. It means "this has not
// run since boot", which for a manual-start service is the normal state.
const errServiceNeverStarted = 1077

// WindowsServiceListScript returns the script that lists services.
func WindowsServiceListScript() string {
	return windowspowershell.JSON(`Get-CimInstance Win32_Service | Select-Object `+
		`Name,DisplayName,State,StartMode,Status,ProcessId,PathName,StartName,`+
		`Description,AcceptStop,AcceptPause,DelayedAutoStart,ExitCode,ServiceSpecificExitCode`, 3)
}

// ParseWindowsServices converts the JSON into the shared ServiceUnit shape.
func ParseWindowsServices(data []byte) ([]linuxsystemd.ServiceUnit, error) {
	rows, err := decodeJSONArray[winServiceRow](data)
	if err != nil {
		return nil, fmt.Errorf("adapter: parse Win32_Service: %w", err)
	}

	out := make([]linuxsystemd.ServiceUnit, 0, len(rows))
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		out = append(out, linuxsystemd.ServiceUnit{
			Name: r.Name,
			// DisplayName, not Description. systemd's Description is a one-line
			// human name ("A high performance web server") and that is what the
			// table column is sized for; Win32_Service.Description is a
			// paragraph. DisplayName is the field that plays the same role.
			Description: r.DisplayName,
			// Every service the SCM knows about is loaded, so there is nothing
			// here matching systemd's not-found or masked. Reporting "loaded"
			// keeps ServiceUnit.Loaded() true, which is what the UI checks before
			// offering actions.
			Load:    "loaded",
			Active:  winActiveState(r),
			Sub:     strings.ToLower(r.State),
			Enabled: winEnabledState(r),
		})
	}
	return out, nil
}

// winActiveState maps to systemd's active/inactive/failed vocabulary.
func winActiveState(r winServiceRow) string {
	if winServiceFailed(r) {
		return "failed"
	}
	switch strings.ToLower(r.State) {
	case "running":
		return "active"
	case "start pending":
		return "activating"
	case "stop pending":
		return "deactivating"
	case "paused", "pause pending", "continue pending":
		// Paused is still loaded and holding resources, so it belongs on the
		// active side; Sub carries the detail.
		return "active"
	default: // Stopped
		return "inactive"
	}
}

// winServiceFailed decides what the "failed only" filter shows.
//
// Windows has no single field for it, and the obvious guesses are all wrong on a
// healthy machine: Status is "OK" for every service on an idle box, and ExitCode
// is 1077 for half of them. What is left is a service configured to start at boot,
// not marked delayed, and nevertheless stopped — one service out of 263 on the
// machine this was measured against, which is the kind of number a filter can be
// useful with.
func winServiceFailed(r winServiceRow) bool {
	if r.Status != "" && !strings.EqualFold(r.Status, "OK") {
		return true
	}
	if r.ExitCode != 0 && r.ExitCode != errServiceNeverStarted {
		return true
	}
	if r.ServiceSpecificExitCode != 0 {
		return true
	}
	return strings.EqualFold(r.StartMode, "Auto") &&
		!r.DelayedAutoStart &&
		strings.EqualFold(r.State, "Stopped")
}

// winEnabledState maps StartMode onto systemd's install-state vocabulary.
func winEnabledState(r winServiceRow) string {
	switch strings.ToLower(r.StartMode) {
	case "auto":
		if r.DelayedAutoStart {
			// Distinguished because it changes what "stopped" means: a delayed
			// service that is not running has probably just not been asked yet.
			return i18n.S("enabled (지연 시작)")
		}
		return "enabled"
	case "manual":
		// Not systemd's "disabled": the service can still be started, by a user
		// or by a trigger. "static" is the closest systemd word — installed,
		// runnable, not started at boot.
		return "manual"
	case "disabled":
		return "disabled"
	case "boot", "system":
		// Driver-level, started by the kernel loader. Cannot be enabled or
		// disabled the way a normal service can.
		return "static"
	default:
		return strings.ToLower(r.StartMode)
	}
}

// WindowsServiceActionScript returns the script for one service action.
//
// The verb is chosen from a fixed set rather than interpolated, and the name goes
// through PowerShell single-quoting. -Name is used rather than positionally so a
// name beginning with a dash cannot become a parameter — the same reason the POSIX
// side insists on `--`.
func WindowsServiceActionScript(action, name string) (string, error) {
	cmd, ok := map[string]string{
		"start":   "Start-Service",
		"stop":    "Stop-Service",
		"restart": "Restart-Service",
	}[action]
	if !ok {
		return "", fmt.Errorf("adapter: unsupported service action %q", action)
	}
	q, err := windowspowershell.SingleQuote(name)
	if err != nil {
		return "", err
	}
	// -Force on stop and restart so a service with dependents does not fail with
	// a prompt that -NonInteractive would turn into an error. Start needs none.
	if action == "stop" || action == "restart" {
		return cmd + " -Name " + q + " -Force", nil
	}
	return cmd + " -Name " + q, nil
}

// WindowsServiceStartTypeScript returns the script that changes whether a service
// starts at boot — the Windows counterpart of systemctl enable/disable.
func WindowsServiceStartTypeScript(name string, enable bool) (string, error) {
	q, err := windowspowershell.SingleQuote(name)
	if err != nil {
		return "", err
	}
	// Automatic, not AutomaticDelayedStart: Set-Service cannot express the delayed
	// variant, and silently converting a delayed service to a plain automatic one
	// would change when it starts without saying so. Re-enabling something that
	// was delayed is a real, if minor, behaviour change and belongs in the notes
	// rather than being hidden here.
	kind := "Automatic"
	if !enable {
		kind = "Disabled"
	}
	return "Set-Service -Name " + q + " -StartupType " + kind, nil
}

// decodeJSONArray unmarshals PowerShell's ConvertTo-Json output into a slice.
//
// ConvertTo-Json emits a bare object when the pipeline yields exactly one item and
// nothing at all for zero. Real capture, one service selected:
//
//	{"Name":"AarSvc_30c48f","Status":1}
//
// Commands built through windowspowershell.JSON wrap the pipeline in @(…) so this
// cannot happen, but golden files and any hand-written script can still be in
// either shape, and a parser that only accepts the array form fails on the one
// server that happens to have a single match.
func decodeJSONArray[T any](data []byte) ([]T, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return []T{}, nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var rows []T
		if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []T{}
		}
		return rows, nil
	}

	var one T
	if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
		return nil, err
	}
	return []T{one}, nil
}
