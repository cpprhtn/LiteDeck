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
const MetricsScript = `echo '#stat'; grep '^cpu' /proc/stat 2>/dev/null
echo '#mem'; cat /proc/meminfo 2>/dev/null
echo '#load'; cat /proc/loadavg 2>/dev/null
echo '#up'; cat /proc/uptime 2>/dev/null
echo '#df'; df -P -B1 2>/dev/null
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
const MetricsScriptWithGPU = `echo '#stat'; grep '^cpu' /proc/stat 2>/dev/null
echo '#mem'; cat /proc/meminfo 2>/dev/null
echo '#load'; cat /proc/loadavg 2>/dev/null
echo '#up'; cat /proc/uptime 2>/dev/null
echo '#df'; df -P -B1 2>/dev/null
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
		Split:       UnknownSplit(),
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
