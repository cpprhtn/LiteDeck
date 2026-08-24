package adapter

import "testing"

func TestParseKumaMetricsGolden(t *testing.T) {
	snap, err := ParseKumaMetrics(loadGolden(t, "kuma", "metrics.txt"))
	if err != nil {
		t.Fatalf("ParseKumaMetrics: %v", err)
	}
	if len(snap.Monitors) != 4 {
		t.Fatalf("got %d monitors, want 4: %+v", len(snap.Monitors), snap.Monitors)
	}
	if snap.Up != 1 || snap.Down != 1 || snap.Pending != 1 || snap.Maintenance != 1 {
		t.Errorf("counts = up %d, down %d, pending %d, maintenance %d",
			snap.Up, snap.Down, snap.Pending, snap.Maintenance)
	}

	// Failing first. Whoever reads this is looking for what is broken.
	if snap.Monitors[0].Name != "db, replica" {
		t.Errorf("first row = %q, want the down monitor", snap.Monitors[0].Name)
	}

	byName := map[string]KumaMonitor{}
	for _, m := range snap.Monitors {
		byName[m.Name] = m
	}

	// A comma inside a label value must not cut the label set in half — the
	// reason the label reader is hand-written rather than a strings.Split.
	down, ok := byName["db, replica"]
	if !ok {
		t.Fatalf("comma in a monitor name split the label set: %+v", snap.Monitors)
	}
	if down.Status != "down" {
		t.Errorf("db status = %q", down.Status)
	}
	if down.Target != "10.0.0.7:5432" {
		t.Errorf("port monitor target = %q", down.Target)
	}
	// NaN is what Kuma writes for a monitor that did not answer. Zero would
	// read as an instant reply, which is the opposite of what happened.
	if down.ResponseMs != 0 {
		t.Errorf("down monitor carried a response time: %v", down.ResponseMs)
	}

	// Escaped quotes survive.
	if _, ok := byName[`nightly "backup"`]; !ok {
		t.Errorf("escaped quotes in a name were not decoded: %+v", snap.Monitors)
	}

	blog := byName["blog"]
	if blog.Status != "up" || blog.ResponseMs != 132 {
		t.Errorf("blog = %+v", blog)
	}
	// An http monitor's target is its URL, not "null:null" — Kuma fills the
	// labels that do not apply with the literal string "null".
	if blog.Target != "https://blog.example.com" {
		t.Errorf("http monitor target = %q", blog.Target)
	}
	if blog.CertDays != 61 || !blog.HasCert {
		t.Errorf("blog cert = %d days (has=%v)", blog.CertDays, blog.HasCert)
	}
	if byName["cache"].Status != "pending" {
		t.Errorf("2 should be pending, got %q", byName["cache"].Status)
	}
	// Pending is a monitor mid-retry, not a monitor that is down. Counting it
	// as down would page somebody for a blip.
	if byName["cache"].Down() {
		t.Error("pending counted as down")
	}
	if byName[`nightly "backup"`].Status != "maintenance" {
		t.Errorf("3 should be maintenance, got %q", byName[`nightly "backup"`].Status)
	}
}

// A future Kuma adding a series must not empty the view.
func TestParseKumaMetricsIgnoresUnknownSeries(t *testing.T) {
	in := []byte(`# HELP something_new A series this parser has never seen
# TYPE something_new counter
something_new{monitor_name="blog"} 5
process_cpu_seconds_total 12.5
monitor_status{monitor_name="blog",monitor_type="http",monitor_url="https://x",monitor_hostname="null",monitor_port="null"} 1
`)
	snap, err := ParseKumaMetrics(in)
	if err != nil {
		t.Fatalf("ParseKumaMetrics: %v", err)
	}
	if len(snap.Monitors) != 1 || snap.Up != 1 {
		t.Fatalf("unknown series changed the reading: %+v", snap)
	}
}

func TestParseKumaMetricsEmpty(t *testing.T) {
	snap, err := ParseKumaMetrics(nil)
	if err != nil {
		t.Fatalf("ParseKumaMetrics: %v", err)
	}
	// A nil slice would marshal to `null` and make the frontend's .length throw.
	if snap.Monitors == nil {
		t.Error("empty read produced a nil slice rather than an empty one")
	}
}
