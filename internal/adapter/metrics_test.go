package adapter

import (
	"os"
	"strings"
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

// A host without a card is the common case: nvidia-smi is missing, the shell
// complains to the dropped stderr, and the section arrives empty. That is not
// an error, and it must not become a phantom card in the bar.
func TestParseGPUsAbsent(t *testing.T) {
	for _, in := range [][]string{nil, {""}, {"bash: nvidia-smi: command not found"}} {
		if got := parseGPUs(in); len(got) != 0 {
			t.Errorf("parseGPUs(%q) = %v, want none", in, got)
		}
	}
}

func TestParseGPUs(t *testing.T) {
	got := parseGPUs([]string{
		"0, NVIDIA GeForce RTX 4090, 37, 41, 55, 24564, 1228",
		"1, Tesla A100-SXM4-40GB, 100, [N/A], 71, 40960, 40960",
	})
	if len(got) != 2 {
		t.Fatalf("got %d cards, want 2", len(got))
	}

	// nounits gives bare numbers and memory in MiB; the conversion happens here
	// so the UI never has to know which column carries which unit.
	if got[0].Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("name = %q", got[0].Name)
	}
	if got[0].Utilization != 37 || got[0].Fan != 41 || got[0].TempC != 55 {
		t.Errorf("card 0 figures = %+v", got[0])
	}
	if got[0].MemTotal != 24564*1024*1024 || got[0].MemUsed != 1228*1024*1024 {
		t.Errorf("memory = %d/%d bytes", got[0].MemUsed, got[0].MemTotal)
	}
	if got[0].MemPercent < 4.9 || got[0].MemPercent > 5.1 {
		t.Errorf("memPercent = %v, want ~5", got[0].MemPercent)
	}

	// A passively cooled datacentre card reports no fan. Zero would read as a
	// stopped fan on a card that is about to cook, so it stays -1.
	if got[1].Fan != -1 {
		t.Errorf("missing fan = %v, want -1", got[1].Fan)
	}
	if got[1].Index != 1 || got[1].MemPercent != 100 {
		t.Errorf("card 1 = %+v", got[1])
	}
}

// The GPU section is optional, so its absence must leave the rest of the
// snapshot intact rather than failing the whole poll.
func TestParseMetricsGPUSection(t *testing.T) {
	m, err := ParseMetrics([]byte("#mem\nMemTotal: 100 kB\n#gpu\n0, NVIDIA A2, 0, [N/A], 33, 15356, 0\n"), CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.GPUs) != 1 || m.GPUs[0].Name != "NVIDIA A2" {
		t.Fatalf("GPUs = %+v", m.GPUs)
	}
	assertArray(t, "Metrics.GPUs with no gpu output", mustParse(t, "#mem\nMemTotal: 100 kB\n").GPUs)
}

func mustParse(t *testing.T, s string) Metrics {
	t.Helper()
	m, err := ParseMetrics([]byte(s), CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A percentage on its own does not say what to do about it.
//
// 90% that is all IOWait is not short of CPU, it is waiting for a disk; 90%
// Steal is not busy at all, its hypervisor is handing the time to somebody
// else — which is the one a cloud VM hits and the one that looks most like a
// machine that needs replacing. Both are indistinguishable from "busy" until
// they are split apart.
func TestCPUSplitSeparatesWaitingFromWorking(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	prev := parseCPULine("cpu 100 0 100 1000 100 0 0 100")
	// Everything below moves by 100 except idle, so each bucket is 25%.
	now := parseCPULine("cpu 200 0 200 1000 200 0 0 200")

	got := now.Split(prev)
	for name, v := range map[string]float64{
		"user": got.User, "system": got.System, "iowait": got.IOWait, "steal": got.Steal,
	} {
		if v < 24.9 || v > 25.1 {
			t.Errorf("%s = %v, want 25", name, v)
		}
	}
}

// Before a second sample there is nothing to difference, and a 0 there would
// draw an idle machine.
func TestCPUSplitIsUnknownOnTheFirstSample(t *testing.T) {
	now := parseCPULine("cpu 200 0 200 1000 200 0 0 200")
	if got := now.Split(CPUTimes{}); got.User != -1 || got.Steal != -1 {
		t.Errorf("%+v — the first sample must be unknown, not zero", got)
	}
}

// Thirty-two cores at "40%" is either every core half busy or one core pinned
// and the rest idle. Those are different problems, and the aggregate cannot
// tell them apart.
func TestParseMetricsReadsEveryCore(t *testing.T) {
	out := []byte("#stat\ncpu  100 0 100 1000 0 0 0 0\n" +
		"cpu0 50 0 50 500 0 0 0 0\ncpu1 50 0 50 500 0 0 0 0\n" +
		"#mem\nMemTotal: 1024 kB\n")
	m, err := ParseMetrics(out, CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Cores) != 2 {
		t.Fatalf("%d cores, want 2", len(m.Cores))
	}
	if m.Cores[0].Index != 0 || m.Cores[1].Index != 1 {
		t.Errorf("core indices %d,%d", m.Cores[0].Index, m.Cores[1].Index)
	}
	// The aggregate row must not be mistaken for a core. Its columns sum to
	// 1200 and each core's to 600, so the wrong row is unmistakable.
	for _, c := range m.Cores {
		if c.Times.Total != 600 {
			t.Errorf("core %d total %d, want 600 — the aggregate row (1200) leaked in",
				c.Index, c.Times.Total)
		}
	}
}

// "70% used" says nothing about whether that is a program holding it or a page
// cache that evaporates the moment anything asks for it.
func TestParseMetricsBreaksMemoryDown(t *testing.T) {
	out := []byte("#mem\nMemTotal: 1000 kB\nMemAvailable: 800 kB\n" +
		"Buffers: 50 kB\nCached: 300 kB\nSReclaimable: 100 kB\nShmem: 20 kB\nDirty: 5 kB\n")
	m, err := ParseMetrics(out, CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if m.MemBuffers != 50*1024 {
		t.Errorf("buffers %d", m.MemBuffers)
	}
	// Reclaimable slab is "used but not really", the same as page cache, and
	// free(1) counts it here too.
	if m.MemCached != 400*1024 {
		t.Errorf("cached %d, want Cached+SReclaimable", m.MemCached)
	}
	if m.MemShared != 20*1024 || m.MemDirty != 5*1024 {
		t.Errorf("shared %d dirty %d", m.MemShared, m.MemDirty)
	}
}

// /proc/stat's intr line carries one number per interrupt vector — hundreds of
// them on a real machine, and softirq is another. Reading the whole file to get
// the handful of rows that are wanted would put all of that on the wire every
// two seconds.
//
// Asserted as "filtered, and not for intr" rather than against the exact
// pattern, so that adding a row to the filter is not a test failure while
// dropping the filter still is.
func TestMetricsScriptDoesNotShipTheInterruptTable(t *testing.T) {
	for _, s := range []string{MetricsScript, MetricsScriptWithGPU} {
		if strings.Contains(s, "cat /proc/stat") {
			t.Errorf("the whole of /proc/stat is being read:\n%s", s)
		}
		if !strings.Contains(s, "grep") || !strings.Contains(s, "/proc/stat") {
			t.Errorf("/proc/stat is not filtered:\n%s", s)
		}
		for _, huge := range []string{"intr", "softirq"} {
			if strings.Contains(s, "|"+huge) || strings.Contains(s, huge+"|") {
				t.Errorf("%s is in the filter; that line is hundreds of numbers", huge)
			}
		}
	}
}

// Every platform has to answer with the same shape.
//
// Windows has no per-core breakdown and no user/system/iowait/steal split. A
// nil core list reaches the frontend as JSON null and the view maps over it; a
// zeroed split draws a bar claiming the machine is doing nothing at all, which
// is a statement rather than an absence.
func TestWindowsMetricsFillTheSameShape(t *testing.T) {
	m, err := ParseWindowsMetrics([]byte(""), 0)
	if err != nil {
		t.Fatal(err)
	}
	if m.Cores == nil {
		t.Error("Cores is nil — it reaches the view as null and the map throws")
	}
	if m.GPUs == nil || m.Filesystems == nil {
		t.Error("a list arrived as nil")
	}
	if m.Split.User != -1 || m.Split.Steal != -1 {
		t.Errorf("split %+v — zeroes say the machine is idle, which is a claim "+
			"Windows cannot make here", m.Split)
	}
}

// The Linux path has to say the same thing before its second sample.
func TestLinuxSplitStartsUnknown(t *testing.T) {
	m, err := ParseMetrics([]byte("#mem\nMemTotal: 1024 kB\n"), CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Cores == nil {
		t.Error("Cores is nil")
	}
	if m.Split.User != -1 {
		t.Errorf("split %+v with no stat section at all", m.Split)
	}
}

// The whole script, against output captured from a real Ubuntu 24.04 host.
// See testdata/golden/metrics/provenance.txt.
func TestParseMetricsGoldenFull(t *testing.T) {
	b, err := os.ReadFile("../../testdata/golden/metrics/ubuntu-24.04-full.txt")
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	m, err := ParseMetrics(b, CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}

	// 20 cores on that machine, per provenance.
	if len(m.Cores) != 20 {
		t.Errorf("%d cores, want 20", len(m.Cores))
	}
	if m.MemTotal == 0 || m.Load1 < 0 || m.UptimeSeconds == 0 {
		t.Errorf("basics missing: mem=%d load=%v up=%d", m.MemTotal, m.Load1, m.UptimeSeconds)
	}

	// Inodes ride a second df and are joined on the mount point.
	var withInodes int
	for _, f := range m.Filesystems {
		if f.InodesTotal > 0 {
			withInodes++
			if f.InodesUsed > f.InodesTotal {
				t.Errorf("%s: %d inodes used of %d", f.MountPoint, f.InodesUsed, f.InodesTotal)
			}
		}
	}
	if withInodes == 0 {
		t.Error("no filesystem carries an inode count; the df -i join failed")
	}

	// Loopback is dropped: it always carries traffic and never says anything
	// about the network.
	for _, n := range m.Net {
		if n.Name == "lo" {
			t.Error("loopback is in the interface list")
		}
	}
	if len(m.Net) == 0 {
		t.Fatal("no interfaces parsed")
	}
	// That host has a genuine rx-drop count on eno1. Reading it as zero would
	// be the whole feature failing quietly.
	var drops uint64
	for _, n := range m.Net {
		drops += n.RxDrop
	}
	if drops == 0 {
		t.Error("every interface reports zero drops; the column offset is wrong")
	}

	// Partitions double-count against their disk.
	for _, d := range m.DiskIO {
		if strings.HasPrefix(d.Name, "sda") && d.Name != "sda" {
			t.Errorf("%s is a partition and would double-count", d.Name)
		}
		if d.ReadBytes == 0 && d.WriteBytes == 0 {
			t.Errorf("%s has no traffic and should have been dropped", d.Name)
		}
	}
	if len(m.DiskIO) == 0 {
		t.Error("no disks parsed")
	}

	if m.FDUsed == 0 {
		t.Error("no file descriptors in use, which cannot be true of a running host")
	}
	// That kernel reports 2^63-1, which is not a limit anybody is approaching.
	if m.FDMax != 0 {
		t.Errorf("FDMax = %d; an unlimited ceiling should arrive as none", m.FDMax)
	}

	if !m.HasPSI {
		t.Error("PSI missing from a 6.8 kernel")
	}
	if m.Runnable < 1 {
		t.Errorf("procs_running = %d; the shell reading /proc/stat is itself runnable", m.Runnable)
	}
	if m.ContextSwitches == 0 {
		t.Error("ctxt is zero")
	}
}

// Partitions are counted against the disk they sit on, so keeping both would
// report several times the traffic that happened.
func TestSkipDiskDropsPartitions(t *testing.T) {
	// The listing a real machine produces: disks, their partitions, and the
	// virtual devices nobody watches.
	all := map[string]bool{}
	for _, n := range []string{
		"sda", "sda1", "sda2", "sdb", "nvme0n1", "nvme0n1p1", "nvme0n1p2",
		"loop3", "ram0", "dm-0", "zram0", "md0", "nbd0", "vda", "vda1", "xvda",
	} {
		all[n] = true
	}

	for _, name := range []string{"sda1", "sda2", "nvme0n1p1", "vda1", "loop3", "ram0", "dm-0", "zram0"} {
		if !skipDisk(name, all) {
			t.Errorf("%s should be skipped", name)
		}
	}
	// md0 is a RAID array and nbd0 a network block device. Both end in a digit
	// and neither is a partition — the first attempt read the name and dropped
	// exactly the disk somebody most wants to watch.
	for _, name := range []string{"sda", "sdb", "nvme0n1", "vda", "xvda", "md0", "nbd0"} {
		if skipDisk(name, all) {
			t.Errorf("%s is a whole device and should be kept", name)
		}
	}
}

// A partition is only a partition when its disk is in the same listing. A
// machine that exposes md0 and nothing named "md" must keep it.
func TestSkipDiskNeedsTheParentPresent(t *testing.T) {
	lonely := map[string]bool{"md0": true, "nbd0": true, "sda1": true}
	for name := range lonely {
		if skipDisk(name, lonely) {
			t.Errorf("%s has no parent in this listing and should be kept", name)
		}
	}
}

// A filesystem can run out of inodes with bytes to spare, and every tool then
// says "no space left on device" — the same words as running out of room.
func TestInodesJoinOnMountPoint(t *testing.T) {
	out := []byte("#df\nFilesystem 1024-blocks Used Available Capacity Mounted on\n" +
		"/dev/sda2 100 40 60 40% /\n" +
		"#di\nFilesystem Inodes IUsed IFree IUse% Mounted on\n" +
		"/dev/sda2 1000 999 1 100% /\n")
	m, err := ParseMetrics(out, CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Filesystems) != 1 {
		t.Fatalf("%d filesystems", len(m.Filesystems))
	}
	f := m.Filesystems[0]
	if f.InodesTotal != 1000 || f.InodesUsed != 999 {
		t.Fatalf("inodes %d/%d", f.InodesUsed, f.InodesTotal)
	}
	if f.InodesPercent < 99 {
		t.Errorf("inode use %v%%, want ~100 — a disk that cannot create a file", f.InodesPercent)
	}
}

// btrfs and several network filesystems have no inode table and report "-".
// That is not an error and must not become a zero that reads as "empty".
func TestInodesTolerateFilesystemsWithout(t *testing.T) {
	out := []byte("#df\nFilesystem 1024-blocks Used Available Capacity Mounted on\n" +
		"/dev/sda2 100 40 60 40% /\n" +
		"#di\nFilesystem Inodes IUsed IFree IUse% Mounted on\n" +
		"/dev/sda2 - - - - /\n")
	m, err := ParseMetrics(out, CPUTimes{})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Filesystems[0].InodesTotal; got != 0 {
		t.Errorf("InodesTotal = %d for a filesystem with no inode table", got)
	}
}
