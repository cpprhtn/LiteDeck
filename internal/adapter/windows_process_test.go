package adapter

import (
	"strings"
	"testing"
)

// winProcessCapture assembles the tagged stream from the individually captured
// blocks.
//
// Every block is real output from the Windows box; only the tag lines are added
// here, and splitTaggedBlocks has its own test. Capturing the composed script as
// one file would freeze a script that is built in Go and would drift from it
// silently.
func winProcessCapture(t *testing.T) []byte {
	t.Helper()
	var sb strings.Builder
	for _, b := range []struct{ tag, file string }{
		{"proc", "win32-process.out"},
		{"ps", "get-process.out"},
		{"cpu", "perf-proc.out"},
		{"mem", "osinfo.out"},
	} {
		sb.WriteString("#" + b.tag + "\n")
		sb.Write(winGolden(t, b.file))
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

// nowMillis is a fixed clock just after the capture, so elapsed times are
// positive and reproducible. The processes in the fixture started around
// 1786204835819 (/Date(...)/ in win32-process.out).
const winCaptureNowMillis = 1786215000000

// winFixtureCount reports how many records a captured JSON array holds.
//
// Expectations are derived from the fixture rather than written as literals. The
// captures are snapshots of a machine that is running: refreshing them changes the
// process count and which services happen to be stopped, and a test full of
// hardcoded totals fails on the next capture for no reason anyone can act on. The
// first version of this file did exactly that.
func winFixtureCount(t *testing.T, file string) int {
	t.Helper()
	rows, err := decodeJSONArray[map[string]any](winGolden(t, file))
	if err != nil {
		t.Fatalf("count %s: %v", file, err)
	}
	return len(rows)
}

func TestParseWindowsProcesses(t *testing.T) {
	want := winFixtureCount(t, "win32-process.out")

	procs, err := ParseWindowsProcesses(winProcessCapture(t), winCaptureNowMillis)
	if err != nil {
		t.Fatalf("ParseWindowsProcesses: %v", err)
	}
	if len(procs) != want {
		t.Fatalf("got %d processes, capture has %d — rows were dropped", len(procs), want)
	}
	if procs == nil {
		t.Fatal("nil slice; the frontend unmounts on JSON null")
	}

	byPID := map[int]ProcessInfo{}
	for _, p := range procs {
		byPID[p.PID] = p
	}

	// Sorted by PID, so the tree builder sees parents before children and the
	// table does not reshuffle between refreshes.
	for i := 1; i < len(procs); i++ {
		if procs[i-1].PID > procs[i].PID {
			t.Fatalf("not sorted by PID at index %d", i)
		}
	}

	// The Idle process must carry its own counter reading, not the machine total.
	// _Total shares IDProcess 0 with Idle and sorts after it, so a map built
	// without filtering hands PID 0 the sum across every core.
	//
	// Both values are read from the fixture instead of written down, so this keeps
	// testing the collision after a re-capture changes the numbers.
	cpuRows, err := decodeJSONArray[winCPURow](winGolden(t, "perf-proc.out"))
	if err != nil {
		t.Fatalf("read perf-proc: %v", err)
	}
	var idleCPU, totalCPU float64
	for _, r := range cpuRows {
		switch r.Name {
		case "Idle":
			idleCPU = r.PercentProcessorTime
		case "_Total":
			totalCPU = r.PercentProcessorTime
		}
	}
	if idleCPU == 0 || totalCPU == 0 {
		t.Fatal("fixture lacks the Idle/_Total rows this test exists to distinguish")
	}

	idle, ok := byPID[0]
	if !ok {
		t.Fatal("PID 0 missing")
	}
	if idle.CPU == totalCPU {
		t.Errorf("PID 0 CPU = %v, which is _Total's value — the aggregate row leaked in", idle.CPU)
	}
	if idle.CPU != idleCPU {
		t.Errorf("PID 0 CPU = %v, want %v (Idle's own reading)", idle.CPU, idleCPU)
	}

	// Over 100 is correct on a multiprocessor machine and must not be clamped —
	// ps reports %cpu the same way.
	if idle.CPU <= 100 {
		t.Errorf("summed-across-cores value %v did not exceed 100", idle.CPU)
	}

	// A process with no CommandLine falls back to its image name rather than
	// leaving the column blank. CommandLine is null for system processes.
	sys, ok := byPID[4]
	if !ok {
		t.Fatal("PID 4 (System) missing")
	}
	if sys.Args == "" {
		t.Error("empty Args; should fall back to the image name")
	}
	if sys.Command != "System" {
		t.Errorf("PID 4 command = %q, want System", sys.Command)
	}

	// RSS is KiB in this struct because that is what ps reports; the CIM value is
	// bytes. Getting it wrong scales every row by 1024.
	if sys.RSS != 139264/1024 {
		t.Errorf("PID 4 RSS = %d KiB, want %d — WorkingSetSize is bytes",
			sys.RSS, 139264/1024)
	}

	// Elapsed comes from the /Date(ms)/ creation time.
	if sys.Elapsed <= 0 {
		t.Errorf("PID 4 elapsed = %d, want a positive age", sys.Elapsed)
	}

	// Nothing on Windows is a zombie, and reporting one would colour rows in the
	// table for a state that cannot occur.
	for _, p := range procs {
		if p.Zombie() {
			t.Errorf("PID %d reported as a zombie", p.PID)
			break
		}
	}

	// Memory percentage against total physical memory, which came from the mem
	// block. 8124848 KiB on this machine.
	var sawMem bool
	for _, p := range procs {
		if p.Mem > 0 {
			sawMem = true
			if p.Mem > 100 {
				t.Errorf("PID %d mem = %v%%", p.PID, p.Mem)
			}
		}
	}
	if !sawMem {
		t.Error("no memory percentages; the mem block did not reach the parser")
	}
}

// TestParseWindowsProcessesMissingCPUBlock covers a locked-down box that withholds
// the performance counter class. The table is still worth showing without it, so
// the parse degrades instead of failing.
func TestParseWindowsProcessesMissingCPUBlock(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("#proc\n")
	sb.Write(winGolden(t, "win32-process.out"))
	sb.WriteString("\n#ps\n")
	sb.Write(winGolden(t, "get-process.out"))
	sb.WriteString("\n#cpu\n")
	sb.Write(winGolden(t, "missing-cmdlet.err")) // not JSON
	sb.WriteString("\n#mem\n")
	sb.Write(winGolden(t, "osinfo.out"))

	procs, err := ParseWindowsProcesses([]byte(sb.String()), winCaptureNowMillis)
	if err != nil {
		t.Fatalf("a missing CPU block should not fail the parse: %v", err)
	}
	if want := winFixtureCount(t, "win32-process.out"); len(procs) != want {
		t.Fatalf("got %d processes, capture has %d", len(procs), want)
	}
	for _, p := range procs {
		if p.CPU != -1 {
			t.Errorf("PID %d CPU = %v, want -1 when the counter is unavailable", p.PID, p.CPU)
			break
		}
	}
}

func TestParseCIMDateMillis(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"/Date(1786204835819)/", 1786204835819},
		{"", 0},
		{"null", 0},
		{"2026-08-09T03:00:00Z", 0}, // not the form ConvertTo-Json emits
		{"/Date(0)/", 0},
		{"/Date(-1)/", 0},
	} {
		if got := ParseCIMDateMillis(tc.in); got != tc.want {
			t.Errorf("ParseCIMDateMillis(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSplitTaggedBlocks(t *testing.T) {
	got := splitTaggedBlocks("#a\n1\n2\n#b\n3\n")
	if got["a"] != "1\n2" {
		t.Errorf("block a = %q", got["a"])
	}
	if got["b"] != "3" {
		t.Errorf("block b = %q", got["b"])
	}
	// A JSON line beginning with # is not a tag, and a tag has no spaces in it.
	got = splitTaggedBlocks("#a\n{\"x\":1}\n# not a tag\n")
	if !strings.Contains(got["a"], `{"x":1}`) {
		t.Errorf("payload lost: %q", got["a"])
	}
}

func TestWindowsKillScript(t *testing.T) {
	s, err := WindowsKillScript(1234, false)
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if s != "Stop-Process -Id 1234" {
		t.Errorf("got %q", s)
	}
	s, _ = WindowsKillScript(1234, true)
	if s != "Stop-Process -Id 1234 -Force" {
		t.Errorf("force = %q", s)
	}

	// The kernel processes are the counterpart of PID 1: signalling them takes the
	// machine down, so the guard is in Go rather than in a dialog.
	for _, pid := range []int{0, 4} {
		if _, err := WindowsKillScript(pid, true); err == nil {
			t.Errorf("PID %d accepted; it is a kernel process", pid)
		}
	}
	for _, pid := range []int{-1, -9999} {
		if _, err := WindowsKillScript(pid, false); err == nil {
			t.Errorf("PID %d accepted", pid)
		}
	}
}

func TestWindowsPriorityScript(t *testing.T) {
	// The nice range the UI already sends, bucketed into the six classes Windows
	// actually has. Coarse by nature: forty niceness values onto six classes.
	for _, tc := range []struct {
		nice int
		want int
	}{
		{-20, 128},  // High
		{-10, 128},  // High
		{-5, 32768}, // AboveNormal
		{0, 32},     // Normal
		{5, 16384},  // BelowNormal
		{19, 64},    // Idle
	} {
		s, err := WindowsPriorityScript(1234, tc.nice)
		if err != nil {
			t.Fatalf("nice %d: %v", tc.nice, err)
		}
		if !strings.Contains(s, "Priority="+itoa(tc.want)) {
			t.Errorf("nice %d gave %q, want Priority=%d", tc.nice, s, tc.want)
		}
		if !strings.Contains(s, "ProcessId=1234") {
			t.Errorf("nice %d lost the pid: %q", tc.nice, s)
		}
	}
	if _, err := WindowsPriorityScript(0, 0); err == nil {
		t.Error("pid 0 accepted")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestWindowsProcessScriptShape(t *testing.T) {
	s := WindowsProcessScript()
	for _, tag := range []string{"#proc", "#ps", "#cpu", "#mem"} {
		if !strings.Contains(s, tag) {
			t.Errorf("script missing the %s block", tag)
		}
	}
	if !strings.Contains(s, "_Total") {
		t.Error("script does not exclude _Total; it collides with PID 0")
	}
	if strings.Count(s, "@(") < 4 {
		t.Error("not every block forces an array")
	}
}
