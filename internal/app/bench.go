package app

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// A synthetic process table for the M0 risk ④ spike (§12): how much does it
// cost to push a few thousand rows from Go, across the webview IPC boundary,
// and into a virtualised table every two seconds?
//
// It is deliberately shaped like the real thing — the columns of §4.4 and the
// churn of a live server — so the numbers transfer. Nothing here ships; the
// real ProcessOps adapter replaces it.

// ProcessRow mirrors one row of the process view (§4.4), matching the fields
// that `ps -eo pid,ppid,user,%cpu,%mem,rss,stat,etimes,comm,args` yields.
type ProcessRow struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	Mem     float64 `json:"mem"`
	RSS     int64   `json:"rss"`
	State   string  `json:"state"`
	Elapsed int64   `json:"elapsed"`
	Command string  `json:"command"`
	Args    string  `json:"args"`
}

// Snapshot is the whole table — the naive approach §3.2(e) currently implies.
type Snapshot struct {
	Rows  []ProcessRow `json:"rows"`
	Seq   int          `json:"seq"`
	GenNs int64        `json:"genNs"` // Go-side generation cost, excluding marshalling
}

// Diff carries only what changed. If the snapshot path proves too expensive,
// this is what §3.2(e) has to become.
type Diff struct {
	Upserted []ProcessRow `json:"upserted"`
	Removed  []int        `json:"removed"`
	Total    int          `json:"total"`
	Seq      int          `json:"seq"`
	GenNs    int64        `json:"genNs"`
}

var (
	users    = []string{"root", "www-data", "postgres", "redis", "litedeck", "systemd+", "nobody"}
	states   = []string{"S", "R", "D", "Z", "I", "Ssl", "Sl"}
	commands = []string{
		"nginx", "postgres", "redis-server", "dockerd", "containerd", "sshd",
		"systemd", "systemd-journal", "python3", "node", "java", "go", "bash",
		"kworker", "rcu_sched", "myapp", "prometheus", "grafana-server",
	}
)

type procSim struct {
	mu      sync.Mutex
	rows    map[int]ProcessRow
	nextPID int
	seq     int
	rng     *rand.Rand
}

func newProcSim() *procSim {
	return &procSim{
		rows:    make(map[int]ProcessRow),
		nextPID: 100,
		rng:     rand.New(rand.NewSource(1)), // fixed seed: runs stay comparable
	}
}

func (s *procSim) spawn() ProcessRow {
	pid := s.nextPID
	s.nextPID++
	cmd := commands[s.rng.Intn(len(commands))]
	return ProcessRow{
		PID:     pid,
		PPID:    1,
		User:    users[s.rng.Intn(len(users))],
		CPU:     s.rng.Float64() * 12,
		Mem:     s.rng.Float64() * 8,
		RSS:     int64(s.rng.Intn(900_000) + 4_000),
		State:   states[s.rng.Intn(len(states))],
		Elapsed: int64(s.rng.Intn(9_000_000)),
		Command: cmd,
		// A realistic argv: long strings are most of the payload in practice.
		Args: fmt.Sprintf("/usr/lib/%s/%s --config /etc/%s/%s.conf --worker-id %d --log-level info",
			cmd, cmd, cmd, cmd, pid),
	}
}

// Resize grows or shrinks the table to exactly n rows.
func (s *procSim) Resize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.rows) < n {
		r := s.spawn()
		s.rows[r.PID] = r
	}
	for pid := range s.rows {
		if len(s.rows) <= n {
			break
		}
		delete(s.rows, pid)
	}
}

// tick advances the simulation by one polling interval: most rows move a
// little, a few appear and disappear. Returns what changed.
func (s *procSim) tick() ([]ProcessRow, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++

	pids := make([]int, 0, len(s.rows))
	for pid := range s.rows {
		pids = append(pids, pid)
	}
	sort.Ints(pids)

	// ~30% of rows see their CPU/memory move between polls, which is typical
	// for a busy host.
	var upserted []ProcessRow
	for _, pid := range pids {
		if s.rng.Float64() > 0.30 {
			continue
		}
		r := s.rows[pid]
		r.CPU = clamp(r.CPU+(s.rng.Float64()-0.5)*4, 0, 100)
		r.Mem = clamp(r.Mem+(s.rng.Float64()-0.5)*1.5, 0, 100)
		r.RSS += int64(s.rng.Intn(20_000) - 10_000)
		r.Elapsed += 2
		s.rows[pid] = r
		upserted = append(upserted, r)
	}

	// ~1% churn: processes come and go.
	churn := len(pids) / 100
	var removed []int
	for range churn {
		if len(pids) == 0 {
			break
		}
		victim := pids[s.rng.Intn(len(pids))]
		if _, ok := s.rows[victim]; ok {
			delete(s.rows, victim)
			removed = append(removed, victim)
		}
		r := s.spawn()
		s.rows[r.PID] = r
		upserted = append(upserted, r)
	}
	return upserted, removed
}

func (s *procSim) all() []ProcessRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProcessRow, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// BenchResize sets the simulated table size.
func (a *App) BenchResize(n int) int {
	if n < 0 {
		n = 0
	}
	if n > 100_000 {
		n = 100_000
	}
	a.sim.Resize(n)
	return n
}

// BenchSnapshot advances one tick and returns the entire table.
func (a *App) BenchSnapshot() Snapshot {
	start := time.Now()
	a.sim.tick()
	rows := a.sim.all()
	a.sim.mu.Lock()
	seq := a.sim.seq
	a.sim.mu.Unlock()
	return Snapshot{Rows: rows, Seq: seq, GenNs: time.Since(start).Nanoseconds()}
}

// BenchDiff advances one tick and returns only the changes.
func (a *App) BenchDiff() Diff {
	start := time.Now()
	upserted, removed := a.sim.tick()
	a.sim.mu.Lock()
	seq, total := a.sim.seq, len(a.sim.rows)
	a.sim.mu.Unlock()
	return Diff{
		Upserted: upserted,
		Removed:  removed,
		Total:    total,
		Seq:      seq,
		GenNs:    time.Since(start).Nanoseconds(),
	}
}
