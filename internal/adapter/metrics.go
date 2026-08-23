package adapter

// The monitoring summary bar (§4.7): CPU, memory, disk, load and GPU.
//
// Positioned as a supporting feature, not a monitoring product. It answers "is
// this box healthy right now" at a glance; anything more belongs to a real
// monitoring stack, and §1.5 keeps LiteDeck out of that.

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MetricsScript collects everything the summary bar needs in one round trip.
//
// A shell script rather than argv, which is the one exception to the
// argv-only rule (§3.2b) — and it is safe for the reason the rule exists: this
// is a compile-time constant with nothing interpolated into it. Splitting it
// into six separate commands would cost six round trips every two seconds,
// and CPU has to be sampled twice anyway.
//
// Every line reads a file the kernel already keeps, so the whole snapshot costs
// one round trip and no process on the server worth measuring. The GPU is the
// exception and does not live here — see GPUStreamScript.
//
// The trailing `:` makes the script's exit status its own rather than the last
// command's. `cat /proc/loadavg` fails on a kernel without it, `df` fails on a
// container with a broken mount, and a shell reports whatever ran last — so a
// normal condition would show up in the Command Log as a red row with a failure
// count climbing behind it. That log is the one place this app asks to be
// believed, so a normal condition must not appear there as a failure.
const MetricsScript = `echo '#stat'; grep -E '^(cpu|procs_|ctxt)' /proc/stat 2>/dev/null
echo '#mem'; cat /proc/meminfo 2>/dev/null
echo '#load'; cat /proc/loadavg 2>/dev/null
echo '#up'; cat /proc/uptime 2>/dev/null
echo '#df'; df -P -B1 2>/dev/null
echo '#di'; df -P -i 2>/dev/null
echo '#net'; cat /proc/net/dev 2>/dev/null
echo '#io'; cat /proc/diskstats 2>/dev/null
echo '#fd'; cat /proc/sys/fs/file-nr 2>/dev/null
echo '#psi'; for z in cpu io memory; do echo "@$z"; cat /proc/pressure/$z 2>/dev/null; done
:`

// MetricsScriptWithGPU is MetricsScript with the card read inline, one
// nvidia-smi per poll.
//
// This was the only way the GPU was read until the feed arrived, and it is
// still the fallback: GPUStreamScript needs a channel and a server that lets
// nvidia-smi write a line at a time, and when either is missing the summary bar
// falls back here rather than dropping the tiles. Slower, never wrong.
//
// nvidia-smi is the one line that can hang. A wedged driver — an Xid fault, a
// card that has fallen off the bus — leaves it blocked in the kernel, and
// because this is one script the whole poll blocks with it: CPU, memory and disk
// would go dark for the twenty seconds of pollTimeout, at exactly the moment
// somebody needs them. `timeout` caps that at five seconds and costs an empty
// GPU section instead. It is a shell builtin test, so hosts without coreutils
// `timeout` fall through to running nvidia-smi bare rather than losing the
// feature.
//
// Windows has no cheap equivalent — a PowerShell job per poll is worse than the
// problem — so WindowsMetricsScript keeps only its Get-Command guard.
const MetricsScriptWithGPU = `echo '#stat'; grep -E '^(cpu|procs_|ctxt)' /proc/stat 2>/dev/null
echo '#mem'; cat /proc/meminfo 2>/dev/null
echo '#load'; cat /proc/loadavg 2>/dev/null
echo '#up'; cat /proc/uptime 2>/dev/null
echo '#df'; df -P -B1 2>/dev/null
echo '#di'; df -P -i 2>/dev/null
echo '#net'; cat /proc/net/dev 2>/dev/null
echo '#io'; cat /proc/diskstats 2>/dev/null
echo '#fd'; cat /proc/sys/fs/file-nr 2>/dev/null
echo '#psi'; for z in cpu io memory; do echo "@$z"; cat /proc/pressure/$z 2>/dev/null; done
echo '#gpu'; GPUTO=; command -v timeout >/dev/null 2>&1 && GPUTO='timeout 5'
$GPUTO nvidia-smi --query-gpu=` + gpuQueryFields + ` --format=csv,noheader,nounits 2>/dev/null
:`

// gpuQueryFields is the one list both readings ask for, so the inline poll and
// the feed can never drift into parsing different columns.
const gpuQueryFields = "index,name,utilization.gpu,fan.speed,temperature.gpu,memory.total,memory.used"

// GPUStreamScript keeps one nvidia-smi alive and lets it report on a loop.
//
// nvidia-smi initialises NVML on startup and tears it down on exit, and that
// setup — not the reading — is nearly all of what an invocation costs. Called
// once per poll it was paying that price every two seconds, forever, on a
// machine whose whole job is to be busy with something else. `-l` keeps one
// process and one NVML session, so each further sample is a library call.
//
// `dmon` is the tool NVIDIA documents for this and it is the wrong one here: it
// reports a fixed column set with no fan speed, no card name and no VRAM in
// bytes. Streaming the same --query-gpu list keeps every figure the tiles
// already show.
//
// The `command -v` guard is what keeps a card-less server quiet. Without it the
// stream would end on nvidia-smi's exit 127 and the app would have to tell a
// missing program apart from a broken one; with it, no card is a clean exit 0
// and an empty feed, which is exactly what it means.
//
// stdbuf is a hedge, not a requirement. A program writing to a pipe usually
// switches to block buffering, which would hold samples back until 4KB of them
// had piled up; `-oL` asks for line buffering instead. Where coreutils stdbuf
// is missing the command still runs, and a feed that goes quiet is caught by
// the staleness check on the other end rather than by trusting this line.
const GPUStreamScript = `command -v nvidia-smi >/dev/null 2>&1 || exit 0
SB=; command -v stdbuf >/dev/null 2>&1 && SB='stdbuf -oL'
exec $SB nvidia-smi --query-gpu=` + gpuQueryFields + ` --format=csv,noheader,nounits -l 2`

// GPUStreamInterval is the `-l` period above. The reader needs it to tell a
// feed that is merely between samples from one that has stopped answering.
const GPUStreamInterval = 2 * time.Second

// CPUTimes is one sample of the aggregate CPU counters.
//
// The counters are monotonic totals since boot, so a single reading says
// nothing about current load — usage is the ratio of deltas between two
// samples. That is why Metrics carries the raw sample forward.
type CPUTimes struct {
	Total uint64 `json:"total"`
	Idle  uint64 `json:"idle"`

	// The buckets behind that total. The parser was already walking every one
	// of these fields to sum them and throwing the individual numbers away,
	// which is why keeping them costs nothing.
	//
	// They are what makes a percentage mean something. A box at 90% that is all
	// IOWait is not short of CPU, it is waiting for a disk; one at 90% Steal is
	// not busy at all, its hypervisor is handing the time to somebody else. Both
	// look exactly like "busy" until they are split apart.
	User   uint64 `json:"user"`
	System uint64 `json:"system"`
	IOWait uint64 `json:"iowait"`
	Steal  uint64 `json:"steal"`
}

// Split reports where the time between two samples went, in percent.
func (c CPUTimes) Split(prev CPUTimes) CPUSplit {
	if prev.Total == 0 || c.Total <= prev.Total {
		return UnknownSplit()
	}
	total := float64(c.Total - prev.Total)
	pct := func(now, before uint64) float64 {
		if now < before {
			return 0
		}
		return clampPercent(float64(now-before) / total * 100)
	}
	return CPUSplit{
		User:   pct(c.User, prev.User),
		System: pct(c.System, prev.System),
		IOWait: pct(c.IOWait, prev.IOWait),
		Steal:  pct(c.Steal, prev.Steal),
	}
}

// UnknownSplit is the breakdown of a platform that does not report one, or of
// a first sample with nothing to difference against. Not a zero value: four
// zeroes draw a bar that says the machine is doing nothing, which is a claim,
// and the whole point of these figures is that they are the difference between
// "idle", "waiting on a disk" and "not being given any CPU at all".
func UnknownSplit() CPUSplit {
	return CPUSplit{User: -1, System: -1, IOWait: -1, Steal: -1}
}

// CPUSplit is the breakdown between two samples. -1 where it cannot be computed
// yet, the same convention CPU itself uses.
type CPUSplit struct {
	User   float64 `json:"user"`
	System float64 `json:"system"`
	IOWait float64 `json:"iowait"`
	Steal  float64 `json:"steal"`
}

// Core is one logical CPU.
//
// The aggregate hides the thing people most need to see: thirty-two cores at
// "40%" is either every core half-busy or one core pinned at 100% and the rest
// idle, and those are different problems with different fixes. The second is
// the common one, because it is what a single-threaded bottleneck looks like.
type Core struct {
	Index int      `json:"index"`
	Usage float64  `json:"usage"` // -1 until a second sample
	Times CPUTimes `json:"-"`
}

// Usage returns busy percentage between two samples, or -1 when it cannot be
// computed — the first reading after connecting, or a counter that went
// backwards because the server rebooted.
func (c CPUTimes) Usage(prev CPUTimes) float64 {
	if prev.Total == 0 || c.Total <= prev.Total {
		return -1
	}
	totalDelta := float64(c.Total - prev.Total)
	idleDelta := float64(c.Idle - prev.Idle)
	if c.Idle < prev.Idle {
		return -1
	}
	usage := (1 - idleDelta/totalDelta) * 100
	return clampPercent(usage)
}

// Filesystem is one mounted filesystem.
type Filesystem struct {
	Device     string  `json:"device"`
	MountPoint string  `json:"mountPoint"`
	Size       int64   `json:"size"`
	Used       int64   `json:"used"`
	Available  int64   `json:"available"`
	Percent    float64 `json:"percent"`

	// Inodes are the other way a filesystem fills up, and the one nobody
	// checks. A disk with room and no inodes left cannot create a file, and
	// every tool that reports "no space left on device" says exactly the same
	// thing as if it were out of bytes. Servers that write many small files —
	// logs, mail queues, session stores — hit this first. Zero where the
	// filesystem does not have inodes at all, which is normal for btrfs.
	InodesTotal   int64   `json:"inodesTotal,omitempty"`
	InodesUsed    int64   `json:"inodesUsed,omitempty"`
	InodesPercent float64 `json:"inodesPercent,omitempty"`
}

// NetIface is one interface's counters.
//
// The counters are cumulative since boot, so the rates are differences between
// samples — the same shape as CPU, and for the same reason.
//
// Errors and drops are kept as raw totals rather than rates because what
// matters is whether they are *climbing*: a card that dropped four hundred
// packets last March is not a problem, and one dropping four a second is.
type NetIface struct {
	Name    string `json:"name"`
	RxBytes uint64 `json:"rxBytes"`
	TxBytes uint64 `json:"txBytes"`
	RxErrs  uint64 `json:"rxErrs"`
	TxErrs  uint64 `json:"txErrs"`
	RxDrop  uint64 `json:"rxDrop"`
	TxDrop  uint64 `json:"txDrop"`

	// Bytes per second between the last two samples. -1 before the second.
	RxRate float64 `json:"rxRate"`
	TxRate float64 `json:"txRate"`
}

// DiskIO is one block device's traffic.
//
// CPU's iowait says the machine is waiting on storage. It does not say which
// storage, and on a host with a fast root disk and a slow array that is the
// whole question.
type DiskIO struct {
	Name       string `json:"name"`
	ReadBytes  uint64 `json:"readBytes"`
	WriteBytes uint64 `json:"writeBytes"`
	// Bytes per second between the last two samples. -1 before the second.
	ReadRate  float64 `json:"readRate"`
	WriteRate float64 `json:"writeRate"`
}

// Pressure is one PSI resource (§/proc/pressure).
//
// Utilisation says how much of a thing is being used; pressure says how much
// time was lost waiting for it. A machine can sit at 100% CPU and be perfectly
// happy — that is a machine doing its job — while one at 40% with rising
// pressure has tasks queueing. The kernel keeps the averages itself, so a
// freshly opened connection gets the last five minutes for free.
//
// Some is "at least one task was stalled"; Full is "every task was", which on
// CPU is never reported and on memory or IO means the machine did nothing at
// all for that fraction of the time.
type Pressure struct {
	Some10  float64 `json:"some10"`
	Some60  float64 `json:"some60"`
	Some300 float64 `json:"some300"`
	Full10  float64 `json:"full10"`
}

// GPU is one NVIDIA card.
//
// NVIDIA only, deliberately: nvidia-smi ships with every driver install and
// answers one line per card over a plain SSH connection. AMD and Intel expose
// nothing comparable without a package that is not there by default, and §1.5
// keeps LiteDeck from carrying an agent to fill the gap. A host with no cards
// reports none and the summary bar drops the tiles, the same way it drops load
// average on Windows.
type GPU struct {
	Index int    `json:"index"`
	Name  string `json:"name"`

	// Utilization, Fan and TempC are -1 where the card does not report the
	// figure — the same convention CPU uses before its second sample. Fan speed
	// is the common one: passively cooled datacentre cards (Tesla, A100) and
	// laptop hybrids answer "[N/A]", and a 0 there reads as a stopped fan on a
	// card that is about to cook.
	Utilization float64 `json:"utilization"` // percent
	Fan         float64 `json:"fan"`         // percent of maximum speed
	TempC       float64 `json:"tempC"`

	MemTotal   int64   `json:"memTotal"` // bytes
	MemUsed    int64   `json:"memUsed"`
	MemPercent float64 `json:"memPercent"`
}

// Metrics is one snapshot of a server's health.
type Metrics struct {
	// CPU is -1 until a second sample exists; the UI shows a dash rather than
	// a misleading zero.
	CPU      float64  `json:"cpu"`
	CPUTimes CPUTimes `json:"cpuTimes"`

	MemTotal     int64   `json:"memTotal"` // bytes
	MemAvailable int64   `json:"memAvailable"`
	MemUsed      int64   `json:"memUsed"`
	MemPercent   float64 `json:"memPercent"`

	SwapTotal int64 `json:"swapTotal"`
	SwapUsed  int64 `json:"swapUsed"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
	// HasLoad is false where the concept does not exist. Windows has no load
	// average and nothing that stands in for one, so the summary bar drops the
	// tile rather than showing 0.00 — which reads as an idle machine instead of as
	// a figure that was never available.
	HasLoad bool `json:"hasLoad"`

	UptimeSeconds int64 `json:"uptimeSeconds"`

	Filesystems []Filesystem `json:"filesystems"`

	// Cores is every logical CPU, in kernel order. Empty on a kernel that does
	// not break /proc/stat down that way.
	Cores []Core `json:"cores"`
	// Split says where the CPU time went — see CPUTimes.
	Split CPUSplit `json:"split"`

	// The memory breakdown. MemUsed already subtracts what the kernel would
	// hand back, but "70% used" still says nothing about whether that is a
	// program holding it or a page cache that will evaporate the moment
	// anything asks. These are what answers that.
	MemBuffers int64 `json:"memBuffers"`
	MemCached  int64 `json:"memCached"`
	MemShared  int64 `json:"memShared"`
	MemDirty   int64 `json:"memDirty"`

	// Runnable and Blocked are vmstat's r and b: tasks wanting a CPU and tasks
	// stuck waiting on IO. Between them they say which of the two a slow
	// machine is short of, and both come out of the /proc/stat this already
	// reads — they were being thrown away.
	Runnable int `json:"runnable"`
	Blocked  int `json:"blocked"`
	// ContextSwitches is cumulative since boot; the app differences it.
	ContextSwitches uint64  `json:"-"`
	SwitchRate      float64 `json:"switchRate"`

	// Net and DiskIO carry cumulative counters plus the rate since the last
	// sample.
	Net    []NetIface `json:"net"`
	DiskIO []DiskIO   `json:"diskIO"`

	// FDUsed and FDMax are /proc/sys/fs/file-nr. Running out of file
	// descriptors takes a server down in a way that looks like nothing else is
	// wrong, and the limit is invisible until it is reached.
	FDUsed int64 `json:"fdUsed"`
	FDMax  int64 `json:"fdMax"`

	// PSI is zero on a kernel built without it (CONFIG_PSI) or older than 4.20.
	HasPSI    bool     `json:"hasPSI"`
	PSICPU    Pressure `json:"psiCPU"`
	PSIIO     Pressure `json:"psiIO"`
	PSIMemory Pressure `json:"psiMemory"`

	// GPUs is empty on the overwhelming majority of servers, which is why the
	// summary bar treats it as an optional section rather than a fixed tile.
	GPUs []GPU `json:"gpus"`
}

// ParseMetrics reads the output of MetricsScript.
//
// prev is the previous CPU sample; pass a zero value on the first call.
func ParseMetrics(data []byte, prev CPUTimes) (Metrics, error) {
	m := Metrics{
		CPU:         -1,
		Filesystems: []Filesystem{},
		GPUs:        []GPU{},
		Cores:       []Core{},
		Net:         []NetIface{},
		DiskIO:      []DiskIO{},
		Split:       UnknownSplit(),
		SwitchRate:  -1,
	}

	sections := splitSections(data)

	if line := firstNonEmpty(sections["stat"]); line != "" {
		m.CPUTimes = parseCPULine(line)
		m.CPU = m.CPUTimes.Usage(prev)
		m.Split = m.CPUTimes.Split(prev)
	}
	for _, line := range sections["stat"] {
		if idx, t, ok := parseCPULineAt(line); ok {
			m.Cores = append(m.Cores, Core{Index: idx, Usage: -1, Times: t})
		}
	}

	mem := parseMeminfo(sections["mem"])
	m.MemTotal = mem["MemTotal"]
	m.MemAvailable = mem["MemAvailable"]
	if m.MemTotal > 0 {
		// MemAvailable, not MemFree: free memory excludes cache the kernel
		// would hand back on demand, so it reads as "almost out of RAM" on
		// every healthy server.
		m.MemUsed = m.MemTotal - m.MemAvailable
		m.MemPercent = clampPercent(float64(m.MemUsed) / float64(m.MemTotal) * 100)
	}
	m.MemBuffers = mem["Buffers"]
	// SReclaimable is slab the kernel gives back under pressure — the same kind
	// of "used but not really" as the page cache, and free(1) counts it here too.
	m.MemCached = mem["Cached"] + mem["SReclaimable"]
	m.MemShared = mem["Shmem"]
	m.MemDirty = mem["Dirty"]
	m.SwapTotal = mem["SwapTotal"]
	if free, ok := mem["SwapFree"]; ok && m.SwapTotal > 0 {
		m.SwapUsed = m.SwapTotal - free
	}

	if line := firstNonEmpty(sections["load"]); line != "" {
		f := strings.Fields(line)
		if len(f) >= 3 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
			m.Load5, _ = strconv.ParseFloat(f[1], 64)
			m.Load15, _ = strconv.ParseFloat(f[2], 64)
			// Set only once the figures actually parsed. A kernel without
			// /proc/loadavg is unusual but the flag has to mean "these numbers are
			// real" rather than "this is Linux".
			m.HasLoad = true
		}
	}

	if line := firstNonEmpty(sections["up"]); line != "" {
		if f := strings.Fields(line); len(f) >= 1 {
			if secs, err := strconv.ParseFloat(f[0], 64); err == nil {
				m.UptimeSeconds = int64(secs)
			}
		}
	}

	m.Filesystems = parseDF(sections["df"])
	m.GPUs = parseGPUs(sections["gpu"])

	// procs_running counts tasks that want a CPU right now; procs_blocked
	// counts tasks parked in uninterruptible IO. These lines ride the /proc/stat
	// section the CPU figures already come from.
	for _, line := range sections["stat"] {
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "procs_running":
			m.Runnable = int(n)
		case "procs_blocked":
			m.Blocked = int(n)
		case "ctxt":
			m.ContextSwitches = n
		}
	}

	applyInodes(&m, sections["di"])
	m.Net = parseNetDev(sections["net"])
	m.DiskIO = parseDiskstats(sections["io"])
	m.FDUsed, m.FDMax = parseFileNr(sections["fd"])
	m.HasPSI, m.PSICPU, m.PSIIO, m.PSIMemory = parsePSI(sections["psi"])

	if m.MemTotal == 0 && len(m.Filesystems) == 0 {
		return m, fmt.Errorf("adapter: metrics output had nothing usable")
	}
	return m, nil
}

// splitSections cuts the combined output on the #markers.
func splitSections(data []byte) map[string][]string {
	out := make(map[string][]string)
	current := ""

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		if current != "" {
			out[current] = append(out[current], line)
		}
	}
	return out
}

// parseCPULine reads the aggregate "cpu" row of /proc/stat.
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
//
// iowait counts as idle: the CPU is not doing work, and counting it as busy
// makes a box waiting on a slow disk look pegged.
func parseCPULine(line string) CPUTimes {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return CPUTimes{}
	}
	var t CPUTimes
	for i, v := range f[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		// Fields beyond guest are already included in user/nice on modern
		// kernels; summing everything present is still the conventional total.
		t.Total += n
		// Column order is fixed by the kernel: user nice system idle iowait
		// irq softirq steal guest guest_nice.
		switch i {
		case 0:
			t.User += n
		case 1:
			t.User += n // nice is user time that was asked to wait its turn
		case 2:
			t.System += n
		case 3:
			t.Idle += n
		case 4:
			t.Idle += n // iowait counts as idle for "busy", and is broken out below
			t.IOWait = n
		case 7:
			t.Steal = n
		}
	}
	return t
}

// parseCPULineAt reads one `cpuN` row, returning the core number.
func parseCPULineAt(line string) (int, CPUTimes, bool) {
	f := strings.Fields(line)
	if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") || f[0] == "cpu" {
		return 0, CPUTimes{}, false
	}
	idx, err := strconv.Atoi(strings.TrimPrefix(f[0], "cpu"))
	if err != nil {
		return 0, CPUTimes{}, false
	}
	// The aggregate parser only rejects a row whose first field is not exactly
	// "cpu", so feed it a renamed copy rather than duplicating the column walk.
	return idx, parseCPULine("cpu " + strings.Join(f[1:], " ")), true
}

// parseMeminfo reads /proc/meminfo into bytes, keyed by field name.
func parseMeminfo(lines []string) map[string]int64 {
	out := make(map[string]int64, 8)
	for _, line := range lines {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo is in kB; everything above this layer works in bytes.
		if len(f) > 1 && strings.EqualFold(f[1], "kB") {
			n *= 1024
		}
		out[key] = n
	}
	return out
}

// parseDF reads `df -P -B1` output.
//
// -P forces the POSIX single-line format: without it, a long device name wraps
// onto its own line and every subsequent column shifts. -B1 gives bytes, so
// there is no human-readable suffix to parse back.
func parseDF(lines []string) []Filesystem {
	out := []Filesystem{}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		if i == 0 && strings.EqualFold(f[0], "Filesystem") {
			continue // header
		}
		size, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		avail, err3 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		mount := strings.Join(f[5:], " ") // mount points can contain spaces

		fs := Filesystem{
			Device: f[0], MountPoint: mount,
			Size: size, Used: used, Available: avail,
		}
		if size > 0 {
			fs.Percent = clampPercent(float64(used) / float64(size) * 100)
		}
		out = append(out, fs)
	}
	return out
}

// parseGPUs reads `nvidia-smi --format=csv,noheader,nounits` rows.
//
// The section is empty on the hosts without a card, which is the common case
// and not an error: the driver is absent, nvidia-smi is not on PATH, and the
// shell wrote its complaint to the dropped stderr.
//
// nounits gives bare numbers, and memory is in MiB — the one unit the flag
// cannot spell out, so it is converted here rather than left for the UI.
func parseGPUs(lines []string) []GPU {
	out := []GPU{}
	for i, line := range lines {
		if g, ok := ParseGPULine(line, i); ok {
			out = append(out, g)
		}
	}
	return out
}

// ParseGPULine reads one CSV row. fallbackIndex names the card when the index
// column is unreadable — the row still describes a real card.
//
// Exported because the feed sees rows one at a time as they arrive and has no
// section to hand over whole.
func ParseGPULine(line string, fallbackIndex int) (GPU, bool) {
	f := strings.Split(line, ",")
	if len(f) < 7 {
		return GPU{}, false
	}
	for j := range f {
		f[j] = strings.TrimSpace(f[j])
	}

	// The index column is authoritative but a driver that answered a row
	// without one still names a real card; fall back to position.
	idx, err := strconv.Atoi(f[0])
	if err != nil {
		idx = fallbackIndex
	}
	g := GPU{
		Index:       idx,
		Name:        f[1],
		Utilization: parseGPUFloat(f[2]),
		Fan:         parseGPUFloat(f[3]),
		TempC:       parseGPUFloat(f[4]),
	}
	if total := parseGPUFloat(f[5]); total >= 0 {
		g.MemTotal = int64(total) * 1024 * 1024
	}
	if used := parseGPUFloat(f[6]); used >= 0 {
		g.MemUsed = int64(used) * 1024 * 1024
	}
	if g.MemTotal > 0 {
		g.MemPercent = clampPercent(float64(g.MemUsed) / float64(g.MemTotal) * 100)
	}
	return g, true
}

// parseGPUFloat returns -1 for the "[N/A]" and "[Not Supported]" placeholders
// nvidia-smi prints where a card cannot answer, keeping them distinct from a
// genuine zero.
func parseGPUFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}

// InterestingFilesystems drops the ones nobody wants in a summary bar.
//
// A container or a modern desktop mounts dozens of tmpfs, overlay and cgroup
// filesystems. Showing them all buries the one line that matters — the disk
// that can actually fill up.
func InterestingFilesystems(all []Filesystem) []Filesystem {
	skipDevices := []string{"tmpfs", "devtmpfs", "shm", "overlay", "udev", "none"}
	skipMounts := []string{"/dev", "/sys", "/proc", "/run", "/snap"}

	out := []Filesystem{}
	seen := make(map[string]bool)
	for _, fs := range all {
		if fs.Size == 0 || seen[fs.MountPoint] {
			continue
		}
		if slicesContainsFold(skipDevices, fs.Device) {
			continue
		}
		if hasAnyPrefix(fs.MountPoint, skipMounts) {
			continue
		}
		// The same device mounted twice (bind mounts, /etc/hosts in a
		// container) would otherwise appear as separate disks.
		if seen[fs.Device+"|"+strconv.FormatInt(fs.Size, 10)] {
			continue
		}
		seen[fs.MountPoint] = true
		seen[fs.Device+"|"+strconv.FormatInt(fs.Size, 10)] = true
		out = append(out, fs)
	}

	// Fullest first: the one about to cause an incident goes at the top.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Percent > out[j].Percent })
	return out
}

func slicesContainsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if s == p || strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}

func firstNonEmpty(lines []string) string {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// applyInodes folds `df -P -i` onto the filesystems already parsed from
// `df -P -B1`. Two commands rather than one because df cannot report bytes and
// inodes together, and joining on the mount point is safe: a mount point is
// unique within one df run by definition.
func applyInodes(m *Metrics, lines []string) {
	byMount := make(map[string][3]int64, len(lines))
	for i, line := range lines {
		if i == 0 && strings.HasPrefix(line, "Filesystem") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		// Mount point is the last field: it can contain spaces, and everything
		// before it is fixed-width columns.
		mount := strings.Join(f[5:], " ")
		total, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			// btrfs and some network filesystems report "-" here. Not an error;
			// they genuinely have no inode table to run out of.
			continue
		}
		byMount[mount] = [3]int64{total, used, 0}
	}
	for i := range m.Filesystems {
		v, ok := byMount[m.Filesystems[i].MountPoint]
		if !ok || v[0] <= 0 {
			continue
		}
		m.Filesystems[i].InodesTotal = v[0]
		m.Filesystems[i].InodesUsed = v[1]
		m.Filesystems[i].InodesPercent = clampPercent(float64(v[1]) / float64(v[0]) * 100)
	}
}

// parseNetDev reads /proc/net/dev.
//
// The header is two lines and the interface name is followed by a colon that
// may or may not have a space after it — "eth0: 123" and "eth0:123" are both
// produced, the second when the byte count is wide enough to fill the column.
func parseNetDev(lines []string) []NetIface {
	out := []NetIface{}
	for _, line := range lines {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue // the two header lines
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "" || len(f) < 16 {
			continue
		}
		// The loopback interface always carries traffic and never says anything
		// about the network, so it is dropped rather than shown at the top of
		// every list.
		if name == "lo" {
			continue
		}
		num := func(i int) uint64 {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			return v
		}
		out = append(out, NetIface{
			Name:    name,
			RxBytes: num(0), RxErrs: num(2), RxDrop: num(3),
			TxBytes: num(8), TxErrs: num(10), TxDrop: num(11),
			RxRate: -1, TxRate: -1,
		})
	}
	return out
}

// diskSectorBytes is the unit /proc/diskstats counts in. It is 512 regardless
// of the device's real sector size — the kernel normalises it.
const diskSectorBytes = 512

// parseDiskstats reads /proc/diskstats, keeping whole disks.
//
// Partitions are dropped: they double-count against the disk they sit on, so a
// machine with sda1..sda3 would appear to be doing four times the IO it is.
func parseDiskstats(lines []string) []DiskIO {
	// Names first, so a partition can be recognised by the disk it belongs to
	// rather than by guessing from its spelling.
	all := make(map[string]bool, len(lines))
	for _, line := range lines {
		if f := strings.Fields(line); len(f) >= 3 {
			all[f[2]] = true
		}
	}

	out := []DiskIO{}
	seen := map[string]bool{}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		name := f[2]
		if skipDisk(name, all) {
			continue
		}
		read, err1 := strconv.ParseUint(f[5], 10, 64)
		written, err2 := strconv.ParseUint(f[9], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if read == 0 && written == 0 {
			// Devices that have never been touched are noise: a host with
			// twenty unused loop devices would bury the disk that matters.
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, DiskIO{
			Name:       name,
			ReadBytes:  read * diskSectorBytes,
			WriteBytes: written * diskSectorBytes,
			ReadRate:   -1,
			WriteRate:  -1,
		})
	}
	return out
}

// skipDisk drops partitions and virtual devices.
//
// A partition's traffic is already counted against the disk it sits on, so
// keeping both reports several times the IO that happened.
//
// It is recognised by finding the disk, not by reading the name. "ends in a
// digit" was the first attempt and it is wrong on every device that is named
// with one: md0 is a RAID array, nbd0 a network block device, and both were
// being dropped — the array in particular is exactly the disk somebody wants
// to watch. A name is a partition only when some *other* device in the same
// listing is a prefix of it and the rest is digits, optionally after the "p"
// that nvme and mmc use as a separator.
func skipDisk(name string, all map[string]bool) bool {
	switch {
	case strings.HasPrefix(name, "loop"), strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "zram"):
		// Not storage anybody is watching.
		return true
	case strings.HasPrefix(name, "dm-"):
		// Device mapper stacks on a physical device that is already in this
		// list, so counting both doubles the traffic. The name it would show
		// ("dm-0") is not one anybody recognises either.
		return true
	}
	for i := 1; i < len(name); i++ {
		rest := name[i:]
		if strings.HasPrefix(rest, "p") && len(rest) > 1 {
			if all[name[:i]] && isDigits(rest[1:]) {
				return true // nvme0n1p1 under nvme0n1
			}
		}
		if all[name[:i]] && isDigits(rest) {
			return true // sda1 under sda
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseFileNr reads /proc/sys/fs/file-nr: allocated, free, maximum.
//
// The middle figure is descriptors allocated but no longer in use, which the
// kernel keeps for reuse — so the number in use is the first minus the second.
func parseFileNr(lines []string) (used, max int64) {
	f := strings.Fields(firstNonEmpty(lines))
	if len(f) < 3 {
		return 0, 0
	}
	alloc, _ := strconv.ParseInt(f[0], 10, 64)
	unused, _ := strconv.ParseInt(f[1], 10, 64)
	max, _ = strconv.ParseInt(f[2], 10, 64)
	if used = alloc - unused; used < 0 {
		used = 0
	}
	// Modern kernels put an effectively infinite number here — 2^63-1 was what
	// a real Ubuntu 24.04 host reported. A percentage against that is always
	// zero and a "2,864 / 9223372036854775807" is worse than saying nothing, so
	// an absurd ceiling is reported as no ceiling and the view shows the count
	// alone.
	if max > fdMaxSane {
		max = 0
	}
	return used, max
}

// fdMaxSane is the largest file-max worth treating as a limit. Well above any
// real tuning — a busy server sets a few million — and far below the 2^63-1
// that means "no limit".
const fdMaxSane = 1 << 32

// parsePSI reads the three /proc/pressure files, which the script tags with
// "@cpu", "@io" and "@memory" because they are separate files sharing one
// section.
func parsePSI(lines []string) (has bool, cpu, io, mem Pressure) {
	var cur *Pressure
	for _, line := range lines {
		if zone, ok := strings.CutPrefix(strings.TrimSpace(line), "@"); ok {
			switch zone {
			case "cpu":
				cur = &cpu
			case "io":
				cur = &io
			case "memory":
				cur = &mem
			default:
				cur = nil
			}
			continue
		}
		if cur == nil {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		kind := f[0] // "some" or "full"
		get := func(key string) float64 {
			for _, kv := range f[1:] {
				if k, v, ok := strings.Cut(kv, "="); ok && k == key {
					n, err := strconv.ParseFloat(v, 64)
					if err == nil {
						return n
					}
				}
			}
			return 0
		}
		has = true
		switch kind {
		case "some":
			cur.Some10, cur.Some60, cur.Some300 = get("avg10"), get("avg60"), get("avg300")
		case "full":
			cur.Full10 = get("avg10")
		}
	}
	return has, cpu, io, mem
}
