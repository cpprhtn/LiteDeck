package adapter

// Windows health metrics, mapped onto the same Metrics shape the summary bar
// already renders.
//
// One round trip, like the Linux script, because this refreshes on a timer and a
// per-figure trip would multiply the cost of the one view that is always on
// screen.

import (
	"fmt"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter/windowspowershell"
)

// WindowsMetricsScript gathers CPU, memory, page file and disks.
//
// CPU comes from the formatted performance counter rather than from Get-Counter:
// Get-Counter is more precise but blocks for a full sample interval, and this runs
// every couple of seconds. The counter class answers instantly.
func WindowsMetricsScript() string {
	return strings.Join([]string{
		`Write-Output '#cpu'`,
		windowspowershell.JSON(`Get-CimInstance Win32_PerfFormattedData_PerfOS_Processor | `+
			`Where-Object { $_.Name -eq '_Total' } | Select-Object PercentProcessorTime`, 2),
		`Write-Output '#os'`,
		windowspowershell.JSON(`Get-CimInstance Win32_OperatingSystem | Select-Object `+
			`TotalVisibleMemorySize,FreePhysicalMemory,LastBootUpTime`, 2),
		`Write-Output '#page'`,
		windowspowershell.JSON(`Get-CimInstance Win32_PageFileUsage | `+
			`Select-Object AllocatedBaseSize,CurrentUsage`, 2),
		`Write-Output '#disk'`,
		windowspowershell.JSON(`Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3' | `+
			`Select-Object DeviceID,Size,FreeSpace`, 2),
		`Write-Output '#gpu'`,
		// Guarded rather than silenced: an unrecognised command is a PowerShell
		// error record, and those arrive as CLIXML on the same stream the rest of
		// this output has to survive on. Get-Command is the same test detect.go
		// uses for docker.
		`if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { ` +
			`nvidia-smi --query-gpu=index,name,utilization.gpu,fan.speed,temperature.gpu,memory.total,memory.used ` +
			`--format=csv,noheader,nounits }`,
	}, "; ")
}

type winOSRow struct {
	// Both are KiB, not bytes. Treating them as bytes understates memory by a
	// factor of 1024 and makes every machine look like it has 8 MB.
	TotalVisibleMemorySize int64  `json:"TotalVisibleMemorySize"`
	FreePhysicalMemory     int64  `json:"FreePhysicalMemory"`
	LastBootUpTime         string `json:"LastBootUpTime"`
}

type winCPUTotalRow struct {
	PercentProcessorTime float64 `json:"PercentProcessorTime"`
}

type winPageRow struct {
	// Megabytes.
	AllocatedBaseSize int64 `json:"AllocatedBaseSize"`
	CurrentUsage      int64 `json:"CurrentUsage"`
}

type winDiskRow struct {
	DeviceID  string `json:"DeviceID"` // "C:"
	Size      int64  `json:"Size"`     // bytes
	FreeSpace int64  `json:"FreeSpace"`
}

// ParseWindowsMetrics reads the output of WindowsMetricsScript.
//
// nowMillis is passed in so uptime is testable against a fixed capture.
func ParseWindowsMetrics(data []byte, nowMillis int64) (Metrics, error) {
	blocks := splitTaggedBlocks(string(data))

	m := Metrics{
		CPU:         -1,
		Filesystems: []Filesystem{},
		GPUs:        []GPU{},
		// Windows reports one aggregate percentage. There is no per-core
		// breakdown here and no user/system/iowait/steal split, so both say so
		// rather than arriving as an empty list of cores and four zeroes — the
		// zeroes would draw a machine doing nothing at all.
		Cores: []Core{},
		Split: UnknownSplit(),
		// None of these have a Windows source yet. Empty lists rather than nil
		// so the view can map over them, and -1 for the rate so it reads as
		// absent instead of idle.
		Net:        []NetIface{},
		DiskIO:     []DiskIO{},
		SwitchRate: -1,
		// Windows has no load average. Nothing here approximates it: the
		// processor queue length counter measures something else, and reporting
		// zero would read as an idle machine rather than as an absent figure. The
		// summary bar drops the tile instead.
		HasLoad: false,
	}

	if rows, err := decodeJSONArray[winCPUTotalRow]([]byte(blocks["cpu"])); err == nil && len(rows) > 0 {
		m.CPU = rows[0].PercentProcessorTime
	}

	osRows, err := decodeJSONArray[winOSRow]([]byte(blocks["os"]))
	if err != nil {
		return m, fmt.Errorf("adapter: parse Win32_OperatingSystem: %w", err)
	}
	if len(osRows) > 0 {
		o := osRows[0]
		m.MemTotal = o.TotalVisibleMemorySize * 1024
		m.MemAvailable = o.FreePhysicalMemory * 1024
		m.MemUsed = m.MemTotal - m.MemAvailable
		if m.MemTotal > 0 {
			m.MemPercent = float64(m.MemUsed) / float64(m.MemTotal) * 100
		}
		if ms := ParseCIMDateMillis(o.LastBootUpTime); ms > 0 && nowMillis > ms {
			m.UptimeSeconds = (nowMillis - ms) / 1000
		}
	}

	// The page file is the closest thing to swap. Several are possible — one per
	// volume — so they are summed, which is also what the Linux side reports.
	if rows, err := decodeJSONArray[winPageRow]([]byte(blocks["page"])); err == nil {
		for _, p := range rows {
			m.SwapTotal += p.AllocatedBaseSize * 1024 * 1024
			m.SwapUsed += p.CurrentUsage * 1024 * 1024
		}
	}

	if rows, err := decodeJSONArray[winDiskRow]([]byte(blocks["disk"])); err == nil {
		for _, d := range rows {
			if d.Size <= 0 {
				// A drive that reports no size is one the caller cannot read —
				// BitLocker-locked, or a card reader with nothing in it. Showing it
				// at 0% used would be a claim rather than a gap.
				continue
			}
			used := d.Size - d.FreeSpace
			m.Filesystems = append(m.Filesystems, Filesystem{
				Device: d.DeviceID,
				// Windows has no mount point separate from the drive letter, and
				// the summary bar labels the tile with this.
				MountPoint: d.DeviceID,
				Size:       d.Size,
				Used:       used,
				Available:  d.FreeSpace,
				Percent:    float64(used) / float64(d.Size) * 100,
			})
		}
	}

	// nvidia-smi is the same binary with the same CSV on both platforms, so the
	// rows go through the Linux parser rather than a second copy of it.
	m.GPUs = parseGPUs(strings.Split(blocks["gpu"], "\n"))

	return m, nil
}
