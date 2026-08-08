package adapter

import (
	"testing"
)

// Expected values come from the golden file itself, not from a separate
// inspection of the same server: uptime, load and free memory all move between
// two commands, and hard-coding numbers from an earlier look makes the test
// fail for reasons that have nothing to do with the parser.
func TestParseMetricsGolden(t *testing.T) {
	m, err := ParseMetrics(loadGolden(t, "metrics", "snapshot.txt"), CPUTimes{})
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}

	// The first sample cannot know CPU usage: the counters are totals since
	// boot. Reporting 0 would be a lie the sparkline then draws.
	if m.CPU != -1 {
		t.Errorf("CPU on the first sample = %v, want -1", m.CPU)
	}
	if m.CPUTimes.Total == 0 || m.CPUTimes.Idle == 0 {
		t.Errorf("CPU counters not read: %+v", m.CPUTimes)
	}
	if m.CPUTimes.Idle >= m.CPUTimes.Total {
		t.Errorf("idle %d >= total %d", m.CPUTimes.Idle, m.CPUTimes.Total)
	}

	// meminfo is kB; everything above this layer is bytes.
	const wantTotal = 16426540 * 1024
	if m.MemTotal != wantTotal {
		t.Errorf("MemTotal = %d, want %d bytes", m.MemTotal, wantTotal)
	}
	if m.MemAvailable != 15815008*1024 {
		t.Errorf("MemAvailable = %d", m.MemAvailable)
	}
	if m.MemUsed != m.MemTotal-m.MemAvailable {
		t.Errorf("MemUsed = %d, want total-available", m.MemUsed)
	}
	if m.MemPercent < 0 || m.MemPercent > 100 {
		t.Errorf("MemPercent = %v", m.MemPercent)
	}

	if m.Load1 != 0.00 || m.Load5 != 0.03 || m.Load15 != 0.01 {
		t.Errorf("load = %v %v %v", m.Load1, m.Load5, m.Load15)
	}
	if m.UptimeSeconds != 392 {
		t.Errorf("uptime = %d, want 392", m.UptimeSeconds)
	}

	if len(m.Filesystems) < 3 {
		t.Fatalf("only %d filesystems parsed", len(m.Filesystems))
	}
	var root *Filesystem
	for i := range m.Filesystems {
		if m.Filesystems[i].MountPoint == "/" {
			root = &m.Filesystems[i]
		}
	}
	if root == nil {
		t.Fatal("/ missing from the filesystem list")
	}
	if root.Size != 82946555904 || root.Used != 68331667456 {
		t.Errorf("root fs = %+v", *root)
	}
	if root.Percent < 82 || root.Percent > 84 {
		t.Errorf("root fs percent = %v, want ~83", root.Percent)
	}
}

// TestCPUUsageNeedsTwoSamples pins the reason Metrics carries the raw counters
// forward: /proc/stat holds totals since boot, so one reading says nothing.
func TestCPUUsageNeedsTwoSamples(t *testing.T) {
	// 100 ticks elapsed, 25 of them idle → 75% busy.
	prev := CPUTimes{Total: 1000, Idle: 800}
	now := CPUTimes{Total: 1100, Idle: 825}
	if got := now.Usage(prev); got < 74.9 || got > 75.1 {
		t.Errorf("Usage = %v, want 75", got)
	}

	// No previous sample.
	if got := now.Usage(CPUTimes{}); got != -1 {
		t.Errorf("Usage with no previous sample = %v, want -1", got)
	}
	// Counters went backwards — the server rebooted between polls. Reporting a
	// number here would draw a wild spike on the sparkline.
	if got := prev.Usage(now); got != -1 {
		t.Errorf("Usage with rewound counters = %v, want -1", got)
	}
	// Two identical samples: no time passed, nothing to divide by.
	if got := now.Usage(now); got != -1 {
		t.Errorf("Usage with identical samples = %v, want -1", got)
	}
}

// TestParseCPULineCountsIowaitAsIdle: a box waiting on a slow disk is not busy,
// and counting iowait as busy makes it look pegged.
func TestParseCPULineCountsIowaitAsIdle(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	got := parseCPULine("cpu  100 0 50 700 100 0 50 0")
	if got.Total != 1000 {
		t.Errorf("Total = %d, want 1000", got.Total)
	}
	if got.Idle != 800 {
		t.Errorf("Idle = %d, want 800 (idle 700 + iowait 100)", got.Idle)
	}
}

func TestParseCPULineIgnoresJunk(t *testing.T) {
	for _, in := range []string{"", "not a cpu line", "cpu0 1 2 3 4 5", "cpu"} {
		if got := parseCPULine(in); got.Total != 0 {
			t.Errorf("parseCPULine(%q) = %+v, want zero", in, got)
		}
	}
}

// TestInterestingFilesystems: a container mounts a dozen tmpfs and overlay
// filesystems. Showing them all buries the one disk that can actually fill up.
func TestInterestingFilesystems(t *testing.T) {
	m, err := ParseMetrics(loadGolden(t, "metrics", "snapshot.txt"), CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	got := InterestingFilesystems(m.Filesystems)

	for _, fs := range got {
		switch fs.Device {
		case "tmpfs", "shm", "overlay", "devtmpfs", "udev", "none":
			t.Errorf("pseudo-filesystem %s (%s) survived the filter", fs.Device, fs.MountPoint)
		}
		if fs.MountPoint == "/dev" || fs.MountPoint == "/dev/shm" || fs.MountPoint == "/run" {
			t.Errorf("%s survived the filter", fs.MountPoint)
		}
	}
	if len(got) == 0 {
		t.Fatal("the filter removed everything; the real disk is gone too")
	}
	// Fullest first: the one about to cause an incident belongs at the top.
	for i := 1; i < len(got); i++ {
		if got[i-1].Percent < got[i].Percent {
			t.Errorf("not sorted by fullness: %v then %v", got[i-1].Percent, got[i].Percent)
		}
	}
}

func TestParseMetricsRejectsGarbage(t *testing.T) {
	if _, err := ParseMetrics([]byte("nothing useful here\n"), CPUTimes{}); err == nil {
		t.Error("garbage input was accepted")
	}
}

// Mount points can contain spaces, and df puts the mount point last precisely
// so the rest of the columns stay parseable.
func TestParseDFMountPointWithSpaces(t *testing.T) {
	lines := []string{
		"Filesystem 1-blocks Used Available Capacity Mounted on",
		"/dev/sda1 1000 400 600 40% /mnt/my backup drive",
	}
	got := parseDF(lines)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].MountPoint != "/mnt/my backup drive" {
		t.Errorf("mount point = %q", got[0].MountPoint)
	}
}

func TestMetricsMarshalsAsArrays(t *testing.T) {
	m, err := ParseMetrics([]byte("#mem\nMemTotal: 100 kB\n"), CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "Metrics.Filesystems with no df output", m.Filesystems)
	assertArray(t, "InterestingFilesystems(empty)", InterestingFilesystems(nil))
}
