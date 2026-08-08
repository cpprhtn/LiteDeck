package adapter

import (
	"strings"
	"testing"
)

func winMetricsCapture(t *testing.T) []byte {
	t.Helper()
	var sb strings.Builder
	for _, b := range []struct{ tag, file string }{
		{"cpu", "perf-cpu.out"},
		{"os", "osinfo.out"},
		{"page", "pagefile.out"},
		{"disk", "logicaldisk.out"},
	} {
		sb.WriteString("#" + b.tag + "\n")
		sb.Write(winGolden(t, b.file))
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

func TestParseWindowsMetrics(t *testing.T) {
	m, err := ParseWindowsMetrics(winMetricsCapture(t), winCaptureNowMillis)
	if err != nil {
		t.Fatalf("ParseWindowsMetrics: %v", err)
	}

	// Memory. TotalVisibleMemorySize and FreePhysicalMemory are KiB; reading them
	// as bytes makes an 8 GB machine report 8 MB.
	osRows, err := decodeJSONArray[winOSRow](winGolden(t, "osinfo.out"))
	if err != nil || len(osRows) == 0 {
		t.Fatalf("read osinfo: %v", err)
	}
	wantTotal := osRows[0].TotalVisibleMemorySize * 1024
	if m.MemTotal != wantTotal {
		t.Errorf("memTotal = %d, want %d (the CIM value is KiB)", m.MemTotal, wantTotal)
	}
	if m.MemTotal < 1<<30 {
		t.Errorf("memTotal = %d — under a gigabyte means the unit conversion is wrong", m.MemTotal)
	}
	if m.MemUsed != m.MemTotal-m.MemAvailable {
		t.Errorf("memUsed %d != total %d - available %d", m.MemUsed, m.MemTotal, m.MemAvailable)
	}
	if m.MemPercent <= 0 || m.MemPercent > 100 {
		t.Errorf("memPercent = %v", m.MemPercent)
	}

	// CPU comes from the _Total instance of the counter class.
	if m.CPU < 0 || m.CPU > 100 {
		t.Errorf("cpu = %v, want a percentage", m.CPU)
	}

	// No load average on Windows, and the flag is what stops the summary bar
	// rendering 0.00 as though the machine were idle.
	if m.HasLoad {
		t.Error("hasLoad true on Windows; there is no load average to report")
	}

	// Page file stands in for swap; the fixture has one at 1920 MB allocated.
	if m.SwapTotal <= 0 {
		t.Error("swapTotal = 0; the page file block did not parse")
	}
	if m.SwapUsed <= 0 || m.SwapUsed > m.SwapTotal {
		t.Errorf("swapUsed = %d against total %d", m.SwapUsed, m.SwapTotal)
	}

	// Uptime from LastBootUpTime, which is a /Date(ms)/ value.
	if m.UptimeSeconds <= 0 {
		t.Errorf("uptimeSeconds = %d, want a positive age", m.UptimeSeconds)
	}

	// Disks. A single drive serialises as a bare object, not an array.
	if len(m.Filesystems) == 0 {
		t.Fatal("no filesystems; a single disk arrives as a bare object and must still parse")
	}
	d := m.Filesystems[0]
	if d.MountPoint != "C:" || d.Device != "C:" {
		t.Errorf("disk = device:%q mount:%q, want C:", d.Device, d.MountPoint)
	}
	if d.Size <= 0 || d.Used+d.Available != d.Size {
		t.Errorf("disk sizes inconsistent: size=%d used=%d avail=%d", d.Size, d.Used, d.Available)
	}
	if d.Percent <= 0 || d.Percent > 100 {
		t.Errorf("disk percent = %v", d.Percent)
	}

	// Nil slices become JSON null and unmount the view.
	if m.Filesystems == nil {
		t.Error("nil Filesystems slice")
	}
}

// TestParseWindowsMetricsMissingBlocks covers a box that withholds the counter
// class or the page file. The bar is still worth drawing, so the parse degrades.
func TestParseWindowsMetricsMissingBlocks(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("#os\n")
	sb.Write(winGolden(t, "osinfo.out"))
	sb.WriteString("\n#disk\n")
	sb.Write(winGolden(t, "logicaldisk.out"))
	sb.WriteString("\n")

	m, err := ParseWindowsMetrics([]byte(sb.String()), winCaptureNowMillis)
	if err != nil {
		t.Fatalf("missing blocks should not fail the parse: %v", err)
	}
	if m.CPU != -1 {
		t.Errorf("cpu = %v, want -1 when the counter is unavailable", m.CPU)
	}
	if m.SwapTotal != 0 {
		t.Errorf("swapTotal = %d with no page file block", m.SwapTotal)
	}
	if m.MemTotal <= 0 {
		t.Error("memory should still have parsed")
	}
	if len(m.Filesystems) == 0 {
		t.Error("disks should still have parsed")
	}
}

func TestWindowsMetricsScriptShape(t *testing.T) {
	s := WindowsMetricsScript()
	for _, tag := range []string{"#cpu", "#os", "#page", "#disk"} {
		if !strings.Contains(s, tag) {
			t.Errorf("script missing the %s block", tag)
		}
	}
	if !strings.Contains(s, "_Total") {
		t.Error("CPU block does not select the _Total instance")
	}
	if !strings.Contains(s, "DriveType=3") {
		t.Error("disk block does not filter to local fixed disks")
	}
	if strings.Count(s, "@(") < 4 {
		t.Error("not every block forces an array")
	}
}

// TestLinuxMetricsStillReportsLoad guards the flag from the other side: adding
// HasLoad must not have turned the load tile off for Linux.
func TestLinuxMetricsStillReportsLoad(t *testing.T) {
	m, err := ParseMetrics(loadGolden(t, "metrics", "snapshot.txt"), CPUTimes{})
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}
	if !m.HasLoad {
		t.Error("hasLoad false on Linux; the summary bar would hide a figure it has")
	}
	if m.Load1 <= 0 && m.Load5 <= 0 && m.Load15 <= 0 {
		t.Error("all load figures zero; the fixture or the parser changed")
	}
}
