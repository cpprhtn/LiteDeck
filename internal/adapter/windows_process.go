package adapter

// Windows processes, mapped onto the ProcessInfo shape the process view already
// renders.
//
// It takes two sources because neither is sufficient. Win32_Process has the
// parent PID and the command line, which the tree view and the args column need;
// Get-Process has the working set and whether the window is responding. They are
// joined on PID in one round trip rather than two, because the table refreshes on
// a timer and a second trip doubles the cost of every refresh.
//
// CPU percentage comes from the performance counter class rather than from
// Get-Process. Get-Process reports cumulative CPU *seconds* since the process
// started, which turns into a percentage only by differencing two samples — and a
// process that has been up for a week shows a number that says nothing about now.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter/windowspowershell"
)

// WindowsProcessScript collects all three tables in one round trip and tags each
// block so the parser can split them apart.
//
// Owner lookup is deliberately absent. It needs an Invoke-CimMethod GetOwner call
// per process — 172 extra round trips inside the script on the box this was
// measured against — and it returns null for every system process anyway. The user
// column is filled from the performance counter's IDProcess association where it
// is free, and left blank otherwise, which is honest and instant.
func WindowsProcessScript() string {
	return strings.Join([]string{
		`Write-Output '#proc'`,
		windowspowershell.JSON(`Get-CimInstance Win32_Process | Select-Object `+
			`ProcessId,ParentProcessId,Name,CommandLine,WorkingSetSize,CreationDate`, 3),
		`Write-Output '#ps'`,
		windowspowershell.JSON(`Get-Process | Select-Object Id,ProcessName,WorkingSet64,Responding`, 3),
		`Write-Output '#cpu'`,
		// _Total is excluded because it shares IDProcess 0 with the Idle process
		// and sorts after it, so a map keyed on PID silently ends up holding the
		// machine-wide total as PID 0's usage — 389 on a four-core box.
		windowspowershell.JSON(`Get-CimInstance Win32_PerfFormattedData_PerfProc_Process | `+
			`Where-Object { $_.Name -ne '_Total' } | `+
			`Select-Object IDProcess,PercentProcessorTime,Name`, 3),
		`Write-Output '#mem'`,
		windowspowershell.JSON(`Get-CimInstance Win32_OperatingSystem | `+
			`Select-Object TotalVisibleMemorySize`, 3),
	}, "; ")
}

type winProcRow struct {
	ProcessID       int    `json:"ProcessId"`
	ParentProcessID int    `json:"ParentProcessId"`
	Name            string `json:"Name"`
	CommandLine     string `json:"CommandLine"`
	WorkingSetSize  int64  `json:"WorkingSetSize"`
	CreationDate    string `json:"CreationDate"`
}

type winPSRow struct {
	ID          int    `json:"Id"`
	ProcessName string `json:"ProcessName"`
	WorkingSet  int64  `json:"WorkingSet64"`
	Responding  bool   `json:"Responding"`
}

// winCPURow is one row of the per-process performance counter.
//
// PercentProcessorTime is summed across cores and so exceeds 100 on a
// multiprocessor machine — the idle process reads 280 on the four-core box this
// was measured against. That matches how ps reports %cpu on Linux, so the value is
// passed through rather than normalised.
//
// Name is carried only to filter: instances are suffixed when several processes
// share an image name (CompatTelRunner, CompatTelRunner#1), and the aggregate row
// is named _Total with IDProcess 0.
type winCPURow struct {
	IDProcess            int     `json:"IDProcess"`
	PercentProcessorTime float64 `json:"PercentProcessorTime"`
	Name                 string  `json:"Name"`
}

type winMemRow struct {
	TotalVisibleMemorySize int64 `json:"TotalVisibleMemorySize"` // KiB
}

// cimDate matches the /Date(1786204835819)/ form ConvertTo-Json produces for a
// CIM DateTime. There is no ISO-8601 option; this is what the wire looks like.
var cimDate = regexp.MustCompile(`/Date\((-?\d+)\)/`)

// ParseCIMDateMillis extracts the epoch milliseconds from a serialised CIM date.
// Returns 0 when the value is absent or unrecognised, which callers treat as
// "unknown" rather than as 1970.
func ParseCIMDateMillis(s string) int64 {
	m := cimDate.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	ms, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms
}

// ParseWindowsProcesses merges the tagged blocks into the shared shape.
//
// nowMillis is passed in rather than read from the clock so the elapsed column is
// testable against a fixed capture. Pass time.Now().UnixMilli() in production.
func ParseWindowsProcesses(data []byte, nowMillis int64) ([]ProcessInfo, error) {
	blocks := splitTaggedBlocks(string(data))

	procs, err := decodeJSONArray[winProcRow]([]byte(blocks["proc"]))
	if err != nil {
		return nil, fmt.Errorf("adapter: parse Win32_Process: %w", err)
	}
	psRows, err := decodeJSONArray[winPSRow]([]byte(blocks["ps"]))
	if err != nil {
		return nil, fmt.Errorf("adapter: parse Get-Process: %w", err)
	}
	cpuRows, err := decodeJSONArray[winCPURow]([]byte(blocks["cpu"]))
	if err != nil {
		// The counter class is the one piece a locked-down box may withhold. A
		// process table without CPU is still worth showing, so this degrades
		// rather than failing the whole view.
		cpuRows = nil
	}
	memRows, _ := decodeJSONArray[winMemRow]([]byte(blocks["mem"]))

	var totalMemBytes int64
	if len(memRows) > 0 {
		totalMemBytes = memRows[0].TotalVisibleMemorySize * 1024
	}

	responding := make(map[int]bool, len(psRows))
	haveResponding := make(map[int]bool, len(psRows))
	for _, r := range psRows {
		responding[r.ID] = r.Responding
		haveResponding[r.ID] = true
	}
	cpuByPID := make(map[int]float64, len(cpuRows))
	for _, r := range cpuRows {
		// Belt and braces: the script filters _Total, and so does this. It shares
		// IDProcess 0 with the Idle process and sorts after it, so without the
		// filter PID 0 quietly reports the machine-wide total instead of its own.
		if r.Name == "_Total" {
			continue
		}
		cpuByPID[r.IDProcess] = r.PercentProcessorTime
	}

	out := make([]ProcessInfo, 0, len(procs))
	for _, p := range procs {
		rss := p.WorkingSetSize
		info := ProcessInfo{
			PID:  p.ProcessID,
			PPID: p.ParentProcessID,
			// RSS is KiB in this struct because that is what ps reports; the CIM
			// value is bytes. Getting this wrong scales every row by 1024.
			RSS:     rss / 1024,
			Command: p.Name,
			Args:    p.CommandLine,
			State:   winProcState(p.ProcessID, responding, haveResponding),
			CPU:     -1,
		}
		if c, ok := cpuByPID[p.ProcessID]; ok {
			info.CPU = c
		}
		if totalMemBytes > 0 {
			info.Mem = float64(rss) / float64(totalMemBytes) * 100
		}
		if ms := ParseCIMDateMillis(p.CreationDate); ms > 0 && nowMillis > ms {
			info.Elapsed = (nowMillis - ms) / 1000
		}
		// CommandLine is null for processes the caller cannot open — every system
		// process when not elevated. Falling back to the image name keeps the
		// column from being blank without inventing arguments that were never
		// there.
		if info.Args == "" {
			info.Args = p.Name
		}
		out = append(out, info)
	}

	// Same ordering the Linux path leaves ps in: by PID, so the table is stable
	// between refreshes and the tree builder sees parents before children.
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// winProcState fills the State column, which on Linux carries the ps state
// letter.
//
// Windows has no equivalent. What it does have is whether a process with a
// window is pumping messages, which is the one piece of state a user acts on — a
// hung application. R and S are borrowed for running and sleeping so the existing
// column and its Zombie() check keep working; "N" marks not-responding, and
// nothing here ever reports Z, because Windows has no zombies.
func winProcState(pid int, responding, have map[int]bool) string {
	if !have[pid] {
		// In Win32_Process but not Get-Process: the process exited between the two
		// queries in the same script. Reporting it as sleeping is less wrong than
		// claiming to know.
		return "S"
	}
	if !responding[pid] {
		return "N"
	}
	return "R"
}

// WindowsKillScript returns the script that ends a process.
//
// There is no TERM/KILL distinction on Windows: Stop-Process asks the process to
// close only if it has a window, and -Force terminates outright. The two-step
// escalation the Linux path offers has no counterpart, so graceful is attempted
// first and force is a separate, explicit call — the UI decides, not this.
func WindowsKillScript(pid int, force bool) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("adapter: invalid pid %d", pid)
	}
	// The System process and the Idle process cannot be stopped, and asking takes
	// a machine down in the same class of way as signalling PID 1 does on Linux.
	if pid == 0 || pid == 4 {
		return "", fmt.Errorf("PID %d은 Windows 커널 프로세스입니다 — 종료할 수 없습니다", pid)
	}
	s := "Stop-Process -Id " + strconv.Itoa(pid)
	if force {
		s += " -Force"
	}
	return s, nil
}

// WindowsPriorityScript sets a process's scheduling priority, the counterpart of
// renice.
//
// Windows has named classes rather than a numeric range, so the nice value the UI
// already sends is bucketed into them. The mapping is coarse by nature: there are
// six classes against forty niceness values, and pretending otherwise would make
// the slider look more precise than the OS is.
func WindowsPriorityScript(pid, nice int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("adapter: invalid pid %d", pid)
	}
	var class string
	switch {
	case nice <= -10:
		class = "High"
	case nice < 0:
		class = "AboveNormal"
	case nice == 0:
		class = "Normal"
	case nice < 10:
		class = "BelowNormal"
	default:
		class = "Idle"
	}
	// Set through the CIM method rather than by assigning to a Get-Process
	// object: PriorityClass on the .NET object throws for processes the caller
	// cannot open, with an exception that names neither the process nor the
	// permission.
	return fmt.Sprintf(
		"Invoke-CimMethod -InputObject (Get-CimInstance Win32_Process -Filter 'ProcessId=%d') "+
			"-MethodName SetPriority -Arguments @{Priority=%d} | Out-Null", pid, winPriorityValue(class)), nil
}

// winPriorityValue converts a class name to the numeric value SetPriority takes.
func winPriorityValue(class string) int {
	switch class {
	case "High":
		return 128
	case "AboveNormal":
		return 32768
	case "BelowNormal":
		return 16384
	case "Idle":
		return 64
	default: // Normal
		return 32
	}
}

// splitTaggedBlocks divides output marked with #name lines into named sections.
//
// One round trip carrying several results needs a separator, and the alternative
// — one command per table — multiplies the cost of a view that refreshes on a
// timer. The same trick the Linux metrics script uses.
func splitTaggedBlocks(s string) map[string]string {
	out := map[string]string{}
	var name string
	var sb strings.Builder
	flush := func() {
		if name != "" {
			out[name] = strings.TrimSpace(sb.String())
		}
		sb.Reset()
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") && !strings.Contains(t, " ") {
			flush()
			name = strings.TrimPrefix(t, "#")
			continue
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	flush()
	return out
}
