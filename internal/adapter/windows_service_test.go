package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func winGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "windows", "golden", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

// TestParseWindowsServices runs against a real capture from a Windows 10 Pro box:
// 263 services, Korean display language.
func TestParseWindowsServices(t *testing.T) {
	raw := winGolden(t, "win32-service.out")
	units, err := ParseWindowsServices(raw)
	if err != nil {
		t.Fatalf("ParseWindowsServices: %v", err)
	}
	// Counted from the fixture rather than written down. These captures are
	// snapshots of a running machine — re-capturing changes how many services
	// there are and which ones happen to be stopped — so a literal here fails on
	// the next capture for a reason nobody can act on.
	want, err := decodeJSONArray[map[string]any](raw)
	if err != nil {
		t.Fatalf("count fixture: %v", err)
	}
	if len(units) != len(want) {
		t.Fatalf("got %d services, capture has %d — rows were dropped", len(units), len(want))
	}

	byName := map[string]int{}
	for i, u := range units {
		byName[u.Name] = i
	}

	// A stopped, manual-start service: the common case, and not a failure.
	alg, ok := byName["ALG"]
	if !ok {
		t.Fatal("ALG missing from the parsed list")
	}
	u := units[alg]
	if u.Description != "Application Layer Gateway Service" {
		t.Errorf("ALG description = %q; want the DisplayName, not the paragraph", u.Description)
	}
	if u.Active != "inactive" || u.Sub != "stopped" || u.Enabled != "manual" {
		t.Errorf("ALG = active:%q sub:%q enabled:%q", u.Active, u.Sub, u.Enabled)
	}
	if u.Failed() {
		t.Error("a stopped manual service reported as failed; 148 of them are stopped on an idle box")
	}
	if !u.Loaded() {
		t.Error("ALG not Loaded(); the UI hides actions for unloaded units")
	}

	// The one genuine candidate: Auto, not delayed, stopped.
	intel, ok := byName["Intel(R) Platform License Manager Service"]
	if !ok {
		t.Fatal("the service with parentheses and spaces in its name did not survive parsing")
	}
	if !units[intel].Failed() {
		t.Error("Auto + not-delayed + Stopped should be the failed signal")
	}

	// Delayed-start services must not be flagged. This is the assertion that
	// keeps the filter usable: they are stopped by design, and treating them as
	// failures produces the same false positives on every Windows machine.
	for _, name := range []string{"edgeupdate", "MapsBroker", "sppsvc"} {
		i, ok := byName[name]
		if !ok {
			t.Errorf("%s missing from the capture", name)
			continue
		}
		if units[i].Failed() {
			t.Errorf("%s flagged as failed; it is Automatic (Delayed Start)", name)
		}
		if !strings.HasPrefix(units[i].Enabled, "enabled") {
			t.Errorf("%s enabled = %q, want it to read as enabled", name, units[i].Enabled)
		}
	}

	// The failed filter has to stay a filter. Which services are stopped varies
	// between captures, so the assertion is on the proportion rather than a count:
	// the first version of this rule counted ExitCode 1077 as a failure and
	// flagged 135 of 263, which is not a signal, it is the list again.
	var failed []string
	for _, u := range units {
		if u.Failed() {
			failed = append(failed, u.Name)
		}
	}
	if len(failed) > len(units)/10 {
		t.Errorf("failed = %d of %d (%v) — too many to be a useful filter",
			len(failed), len(units), failed)
	}

	// And every one of them has to be a service that is set to start at boot,
	// is not delayed, and is not running. Anything else means the rule widened.
	rows, err := decodeJSONArray[winServiceRow](raw)
	if err != nil {
		t.Fatalf("re-read fixture: %v", err)
	}
	rowByName := map[string]winServiceRow{}
	for _, r := range rows {
		rowByName[r.Name] = r
	}
	for _, name := range failed {
		r := rowByName[name]
		explained := !strings.EqualFold(r.Status, "OK") ||
			(r.ExitCode != 0 && r.ExitCode != errServiceNeverStarted) ||
			r.ServiceSpecificExitCode != 0 ||
			(strings.EqualFold(r.StartMode, "Auto") && !r.DelayedAutoStart &&
				strings.EqualFold(r.State, "Stopped"))
		if !explained {
			t.Errorf("%s flagged failed but matches no rule: state=%q mode=%q delayed=%v exit=%d",
				name, r.State, r.StartMode, r.DelayedAutoStart, r.ExitCode)
		}
	}

	// Korean text has to survive intact; the encoding prelude is what makes that
	// true and this is the assertion that would catch its removal.
	var sawHangul bool
	for _, u := range units {
		if strings.Contains(u.Description, "서비스") || strings.Contains(u.Description, "관리") {
			sawHangul = true
			break
		}
	}
	if !sawHangul {
		t.Error("no Korean text in any description; the capture or the encoding is wrong")
	}

	// Nil slices become JSON null and take the frontend down with them.
	if units == nil {
		t.Error("nil slice returned")
	}
}

// TestParseWindowsServicesSingleObject covers ConvertTo-Json's shape change when
// the pipeline yields exactly one item. The fixture is a real capture.
func TestParseWindowsServicesSingleObject(t *testing.T) {
	// From the same projection the adapter reads. The Get-Service capture in
	// single-service.out is not interchangeable: its Status is an enum integer, so
	// running it through this parser tests the mismatch between two projections
	// rather than the shape change it is meant to cover. That mistake is what this
	// fixture exists to prevent repeating.
	raw := winGolden(t, "single-win32-service.out")
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		t.Fatal("fixture is an array; it is supposed to be the bare-object case")
	}

	units, err := ParseWindowsServices(raw)
	if err != nil {
		t.Fatalf("single object rejected: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1", len(units))
	}
	u := units[0]
	if u.Name != "AJRouter" || u.Description != "AllJoyn Router Service" {
		t.Errorf("got name:%q desc:%q", u.Name, u.Description)
	}
	// ExitCode is 1077 here, which is ERROR_SERVICE_NEVER_STARTED and normal for
	// a manual service that has not run.
	if u.Failed() {
		t.Error("flagged as failed on ExitCode 1077")
	}
	if u.Active != "inactive" || u.Enabled != "manual" {
		t.Errorf("active:%q enabled:%q", u.Active, u.Enabled)
	}
}

func TestParseWindowsServicesEmpty(t *testing.T) {
	for _, in := range []string{"", "  \r\n ", "null", "[]"} {
		units, err := ParseWindowsServices([]byte(in))
		if err != nil {
			t.Errorf("ParseWindowsServices(%q): %v", in, err)
		}
		if units == nil {
			t.Errorf("ParseWindowsServices(%q) returned nil, not an empty slice", in)
		}
		if len(units) != 0 {
			t.Errorf("ParseWindowsServices(%q) = %d units", in, len(units))
		}
	}
}

func TestWindowsServiceActionScript(t *testing.T) {
	for _, tc := range []struct {
		action, name, want string
	}{
		{"start", "nginx", "Start-Service -Name 'nginx'"},
		{"stop", "nginx", "Stop-Service -Name 'nginx' -Force"},
		{"restart", "nginx", "Restart-Service -Name 'nginx' -Force"},
		{"start", "Intel(R) Platform License Manager Service",
			"Start-Service -Name 'Intel(R) Platform License Manager Service'"},
	} {
		got, err := WindowsServiceActionScript(tc.action, tc.name)
		if err != nil {
			t.Errorf("%s %q: %v", tc.action, tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s %q =\n  %q\nwant\n  %q", tc.action, tc.name, got, tc.want)
		}
	}

	if _, err := WindowsServiceActionScript("mask", "nginx"); err == nil {
		t.Error("unsupported action accepted; the verb must come from a fixed set")
	}

	// Injection. The name is server- or user-supplied and lands inside a script,
	// so this is the same front line shellquote holds on POSIX.
	for _, hostile := range []string{
		`'; Stop-Computer; '`,
		"$(Invoke-Expression 'calc')",
		"a'b",
	} {
		got, err := WindowsServiceActionScript("stop", hostile)
		if err != nil {
			t.Errorf("SingleQuote rejected %q: %v", hostile, err)
			continue
		}
		// Every quote in the payload must be doubled, which means no odd-length
		// run of quotes can survive to close the literal early.
		body := strings.TrimSuffix(strings.TrimPrefix(got, "Stop-Service -Name "), " -Force")
		if !strings.HasPrefix(body, "'") || !strings.HasSuffix(body, "'") {
			t.Errorf("%q did not come out as a single-quoted literal: %q", hostile, got)
		}
		inner := body[1 : len(body)-1]
		if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
			t.Errorf("%q left an unescaped quote: %q", hostile, got)
		}
	}

	if _, err := WindowsServiceActionScript("stop", "svc\x00x"); err == nil {
		t.Error("NUL accepted in a service name")
	}
}

func TestWindowsServiceStartTypeScript(t *testing.T) {
	got, err := WindowsServiceStartTypeScript("nginx", true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got != "Set-Service -Name 'nginx' -StartupType Automatic" {
		t.Errorf("enable = %q", got)
	}
	got, err = WindowsServiceStartTypeScript("nginx", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got != "Set-Service -Name 'nginx' -StartupType Disabled" {
		t.Errorf("disable = %q", got)
	}
}

func TestWindowsServiceListScriptForcesArray(t *testing.T) {
	s := WindowsServiceListScript()
	if !strings.Contains(s, "@(") {
		t.Errorf("list script does not force an array: %q", s)
	}
	if !strings.Contains(s, "DelayedAutoStart") {
		t.Error("list script omits DelayedAutoStart; the failed filter needs it")
	}
	if !strings.Contains(s, "-Depth") {
		t.Error("list script leaves depth implicit; 5.1 defaults to 2 and truncates")
	}
}
