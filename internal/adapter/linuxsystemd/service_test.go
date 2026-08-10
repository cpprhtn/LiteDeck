package linuxsystemd

import (
	"os"
	"path/filepath"
	"testing"
)

// Golden files hold real output captured from real distributions (§10).
// Supporting a new distribution means adding a directory here, not editing
// parser code.
const goldenRoot = "../../../testdata/golden"

func golden(t *testing.T, distro, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenRoot, distro, name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return b
}

func byName(units []ServiceUnit) map[string]ServiceUnit {
	m := make(map[string]ServiceUnit, len(units))
	for _, u := range units {
		m[u.Name] = u
	}
	return m
}

func TestParseListUnitsJSON(t *testing.T) {
	units, err := ParseListUnits(golden(t, "ubuntu-22.04", "list-units.json"))
	if err != nil {
		t.Fatalf("ParseListUnits: %v", err)
	}
	if len(units) != 32 {
		t.Errorf("got %d units, want 32", len(units))
	}

	m := byName(units)
	worker, ok := m["litedeck-worker.service"]
	if !ok {
		t.Fatal("litedeck-worker.service missing")
	}
	if worker.Active != "active" || worker.Sub != "running" {
		t.Errorf("worker = %+v, want active/running", worker)
	}
	if worker.Description != "LiteDeck fixture worker" {
		t.Errorf("description = %q", worker.Description)
	}
	// list-units carries no install state — that is the whole reason a second
	// command is needed.
	if worker.Enabled != "" {
		t.Errorf("Enabled = %q from list-units alone; want empty", worker.Enabled)
	}
}

func TestParseUnitFilesJSON(t *testing.T) {
	files, err := ParseUnitFiles(golden(t, "ubuntu-22.04", "list-unit-files.json"))
	if err != nil {
		t.Fatalf("ParseUnitFiles: %v", err)
	}
	if len(files) != 98 {
		t.Errorf("got %d unit files, want 98", len(files))
	}
	if got := files["litedeck-worker.service"].Enabled; got != "enabled" {
		t.Errorf("worker Enabled = %q, want enabled", got)
	}
	if got := files["litedeck-idle.service"].Enabled; got != "disabled" {
		t.Errorf("idle Enabled = %q, want disabled", got)
	}
	if !files["autovt@.service"].Template {
		t.Error("autovt@.service not recognised as a template")
	}
}

// TestMergeServices pins the union semantics: a disabled unit is never loaded,
// so it appears only in list-unit-files — and it still has to reach the view,
// because enabling it is the reason the user opened the tab.
func TestMergeServices(t *testing.T) {
	loaded, err := ParseListUnits(golden(t, "ubuntu-22.04", "list-units.json"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := ParseUnitFiles(golden(t, "ubuntu-22.04", "list-unit-files.json"))
	if err != nil {
		t.Fatal(err)
	}

	merged := MergeServices(loaded, files)
	m := byName(merged)

	worker := m["litedeck-worker.service"]
	if worker.Active != "active" || worker.Enabled != "enabled" {
		t.Errorf("worker = %+v, want runtime and install state combined", worker)
	}

	idle, ok := m["litedeck-idle.service"]
	if !ok {
		t.Fatal("litedeck-idle.service dropped by the merge; disabled units must survive")
	}
	if idle.Enabled != "disabled" {
		t.Errorf("idle Enabled = %q, want disabled", idle.Enabled)
	}
	if idle.Loaded() {
		t.Errorf("idle Load = %q, want empty (never loaded)", idle.Load)
	}

	if len(merged) < len(files) {
		t.Errorf("merged %d < %d unit files; the merge lost rows", len(merged), len(files))
	}
	for i := 1; i < len(merged); i++ {
		if merged[i-1].Name > merged[i].Name {
			t.Fatalf("merge output not sorted at %d: %q then %q", i, merged[i-1].Name, merged[i].Name)
		}
	}
}

// TestTableParserMatchesJSON is the fallback's correctness proof. The 22.04
// fixture was captured in both formats, so the table parser can be held to the
// JSON parser's output on identical system state.
func TestTableParserMatchesJSON(t *testing.T) {
	fromJSON, err := ParseListUnits(golden(t, "ubuntu-22.04", "list-units.json"))
	if err != nil {
		t.Fatal(err)
	}
	fromTable, err := ParseListUnitsTable(golden(t, "ubuntu-22.04", "list-units.txt"))
	if err != nil {
		t.Fatal(err)
	}

	if len(fromJSON) != len(fromTable) {
		t.Fatalf("json parsed %d units, table parsed %d", len(fromJSON), len(fromTable))
	}
	jm, tm := byName(fromJSON), byName(fromTable)
	for name, j := range jm {
		tbl, ok := tm[name]
		if !ok {
			t.Errorf("%s present in json, missing from table", name)
			continue
		}
		if j != tbl {
			t.Errorf("%s differs:\n  json  = %+v\n  table = %+v", name, j, tbl)
		}
	}

	fj, err := ParseUnitFiles(golden(t, "ubuntu-22.04", "list-unit-files.json"))
	if err != nil {
		t.Fatal(err)
	}
	ft, err := ParseUnitFilesTable(golden(t, "ubuntu-22.04", "list-unit-files.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fj) != len(ft) {
		t.Fatalf("json parsed %d unit files, table parsed %d", len(fj), len(ft))
	}
	for name, j := range fj {
		if tbl, ok := ft[name]; !ok || j != tbl {
			t.Errorf("%s differs:\n  json  = %+v\n  table = %+v", name, j, tbl)
		}
	}
}

// TestParseUbuntu2004Table covers the distribution that has no JSON at all.
func TestParseUbuntu2004Table(t *testing.T) {
	units, err := ParseListUnitsTable(golden(t, "ubuntu-20.04", "list-units.txt"))
	if err != nil {
		t.Fatalf("ParseListUnitsTable: %v", err)
	}
	if len(units) != 30 {
		t.Errorf("got %d units, want 30", len(units))
	}
	m := byName(units)
	if got := m["ssh.service"]; got.Active != "active" || got.Sub != "running" {
		t.Errorf("ssh.service = %+v, want active/running", got)
	}
	if got := m["emergency.service"].Description; got != "Emergency Shell" {
		t.Errorf("description = %q, want %q", got, "Emergency Shell")
	}

	files, err := ParseUnitFilesTable(golden(t, "ubuntu-20.04", "list-unit-files.txt"))
	if err != nil {
		t.Fatalf("ParseUnitFilesTable: %v", err)
	}
	if len(files) != 99 {
		t.Errorf("got %d unit files, want 99", len(files))
	}
	if got := files["litedeck-worker.service"].Enabled; got != "enabled" {
		t.Errorf("worker Enabled = %q, want enabled", got)
	}
}

func TestParseSystemdVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"systemd 249 (249.11-0ubuntu3.21)", 249, true},
		{"systemd 245 (245.4-4ubuntu3.24)", 245, true},
		{"systemd 239", 239, true},
		{"systemd 255.4-1", 255, true},
		{"", 0, false},
		{"not a version line", 0, false},
	}
	for _, c := range cases {
		got, err := ParseSystemdVersion(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseSystemdVersion(%q): %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseSystemdVersion(%q) = %d, want an error", c.in, got)
			continue
		}
		if got != c.want {
			t.Errorf("ParseSystemdVersion(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestSupportsJSONOutput records the version gate found during M0: systemd
// below 246 accepts --output=json and prints a plain table anyway, so the
// format must be chosen from the version, never probed from the output.
func TestSupportsJSONOutput(t *testing.T) {
	for v, want := range map[int]bool{239: false, 245: false, 246: true, 249: true, 255: true} {
		if got := SupportsJSONOutput(v); got != want {
			t.Errorf("SupportsJSONOutput(%d) = %v, want %v", v, got, want)
		}
	}
}
