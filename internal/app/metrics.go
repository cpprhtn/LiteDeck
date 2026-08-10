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
}

func newCPUHistory() *cpuHistory {
	return &cpuHistory{byID: make(map[string]adapter.CPUTimes)}
}

func (h *cpuHistory) previous(hostID string) adapter.CPUTimes {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byID[hostID]
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

	// One round trip for everything. The script is a compile-time constant with
	// nothing interpolated, which is why passing it to `sh -c` does not violate
	// the argv-only rule (§3.2b) — there is no caller-supplied text in it.
	res, err := conn.Poll(ctx, "sh", "-c", adapter.MetricsScript)
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

	return MetricsView{
		Metrics: m,
		Disks:   adapter.InterestingFilesystems(m.Filesystems),
	}, nil
}
