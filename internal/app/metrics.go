package app

import (
	"context"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The monitoring summary bar (§4.7).
//
// A supporting feature, not a monitoring product: it answers "is this box
// healthy right now" at a glance. Anything beyond that belongs to a real
// monitoring stack, and §1.5 keeps LiteDeck out of that business.

// cpuHistory remembers the previous /proc/stat sample per host.
//
// It has to live here rather than in the frontend because CPU usage is a ratio
// of deltas: the counters are totals since boot, so one reading alone says
// nothing. Keeping it server-side also means the number survives a tab switch.
type cpuHistory struct {
	mu   sync.Mutex
	byID map[string]adapter.CPUTimes
	// cores holds the same thing per logical CPU, so that "one core pinned" and
	// "every core half busy" can be told apart. Keyed by core index rather than
	// by position: a kernel that renumbers cores across a hotplug would
	// otherwise difference two different cores against each other and draw a
	// number that never happened.
	cores map[string]map[int]adapter.CPUTimes
}

func newCPUHistory() *cpuHistory {
	return &cpuHistory{
		byID:  make(map[string]adapter.CPUTimes),
		cores: make(map[string]map[int]adapter.CPUTimes),
	}
}

func (h *cpuHistory) previous(hostID string) adapter.CPUTimes {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byID[hostID]
}

// previousCore returns the last sample for one core, zero if never seen.
func (h *cpuHistory) previousCore(hostID string, idx int) adapter.CPUTimes {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cores[hostID][idx]
}

func (h *cpuHistory) recordCores(hostID string, cores []adapter.Core) {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := make(map[int]adapter.CPUTimes, len(cores))
	for _, c := range cores {
		next[c.Index] = c.Times
	}
	h.cores[hostID] = next
}

// record stores a sample only once it has been parsed successfully. Storing
// before parsing would leave a zero sample behind on failure, and the next poll
// would report -1 as though the host had just connected.
func (h *cpuHistory) record(hostID string, now adapter.CPUTimes) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byID[hostID] = now
}

func (h *cpuHistory) forget(hostID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byID, hostID)
	delete(h.cores, hostID)
}

// MetricsView is what the summary bar renders.
type MetricsView struct {
	adapter.Metrics
	// Disks is the filtered, sorted subset worth showing — a container mounts
	// a dozen tmpfs and overlay filesystems that would bury the real disk.
	Disks []adapter.Filesystem `json:"disks"`
}

// HostMetrics takes one health snapshot (§4.7).
func (a *App) HostMetrics(hostID string) (MetricsView, error) {
	info, err := a.requireCapability(hostID, adapter.CapMetrics, i18n.S("상태 정보"))
	if err != nil {
		return MetricsView{}, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return MetricsView{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	if info.Platform == adapter.PlatformWindows {
		out, err := a.runPowerShell(ctx, conn, sshcore.CommandPoll, adapter.WindowsMetricsScript())
		if err != nil {
			return MetricsView{}, err
		}
		m, err := adapter.ParseWindowsMetrics(out, time.Now().UnixMilli())
		if err != nil {
			return MetricsView{}, err
		}
		// No CPU history to record: the counter is already a percentage, so
		// there is no cumulative sample to difference against next time.
		return MetricsView{
			Metrics: m,
			Disks:   adapter.InterestingFilesystems(m.Filesystems),
		}, nil
	}

	// The card is read by a feed that outlives this poll, so that nvidia-smi is
	// started once rather than every two seconds. Whether it is answering
	// decides which script runs: when it is, the poll leaves the GPU out
	// entirely; when it is not — no feed yet, no channel to spare, output that
	// arrives in blocks instead of lines — the script still carries the inline
	// nvidia-smi it always did. The tiles never go blank on account of this.
	gpuRows, gpuLive := a.gpus.sample(hostID)
	script := adapter.MetricsScriptWithGPU
	if gpuLive {
		script = adapter.MetricsScript
	}

	// One round trip for everything. Both scripts are compile-time constants
	// with nothing interpolated, which is why passing one to `sh -c` does not
	// violate the argv-only rule (§3.2b) — there is no caller-supplied text.
	res, err := conn.Poll(ctx, "sh", "-c", script)
	if err != nil {
		return MetricsView{}, err
	}
	if !res.OK() && len(res.Stdout) == 0 {
		return MetricsView{}, res.Err()
	}

	m, err := adapter.ParseMetrics(res.Stdout, a.cpu.previous(hostID))
	if err != nil {
		return MetricsView{}, err
	}
	a.cpu.record(hostID, m.CPUTimes)
	// Per core, against that core's own previous sample. Done here rather than
	// in the parser because the parser has no memory — the counters are totals
	// since boot, so one reading alone says nothing (the same reason the
	// aggregate needs a history at all).
	for i := range m.Cores {
		m.Cores[i].Usage = m.Cores[i].Times.Usage(a.cpu.previousCore(hostID, m.Cores[i].Index))
	}
	a.cpu.recordCores(hostID, m.Cores)
	if gpuLive {
		// Never nil: the bar maps over this, and a host with no card has to
		// arrive as an empty list rather than as a missing one.
		m.GPUs = gpuRows
		if m.GPUs == nil {
			m.GPUs = []adapter.GPU{}
		}
	}

	// After the poll, not on connect: a poll is the evidence that somebody is
	// looking at this host. Cheap and idempotent once the feed is running.
	a.gpus.ensure(hostID, conn)

	return MetricsView{
		Metrics: m,
		Disks:   adapter.InterestingFilesystems(m.Filesystems),
	}, nil
}
