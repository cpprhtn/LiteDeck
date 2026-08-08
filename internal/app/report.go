package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// A channel for the frontend to hand its own timings back to Go, so the
// benchmark can be read from a file instead of a screenshot. IPC and React
// commit cost can only be measured inside the webview, but they are useless if
// nobody can see them.
//
// Spike-only; deleted with the rest of the benchmark.

// RenderSample is one polling cycle as the frontend experienced it.
type RenderSample struct {
	Rows       int     `json:"rows"`
	Mode       string  `json:"mode"`
	IPCms      float64 `json:"ipcMs"`      // call → promise resolved
	ApplyMs    float64 `json:"applyMs"`    // merging a diff into the local table
	RenderMs   float64 `json:"renderMs"`   // React commit, forced with flushSync
	TotalMs    float64 `json:"totalMs"`    //
	Bytes      int     `json:"bytes"`      // approximate payload size
	ColdStart  float64 `json:"coldStart"`  // ms from process start to first frame
	SweepIndex int     `json:"sweepIndex"` //
}

type reporter struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// newReporter picks a path that is the same on every run.
//
// os.TempDir() is not: macOS hands a bundled app launched via Finder its own
// sandboxed TMPDIR under /var/folders, so the results landed somewhere the
// caller could not guess. LITEDECK_BENCH_OUT overrides for scripted runs.
func newReporter() *reporter {
	if p := os.Getenv("LITEDECK_BENCH_OUT"); p != "" {
		return &reporter{path: p}
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "litedeck")
	_ = os.MkdirAll(dir, 0o755)
	return &reporter{path: filepath.Join(dir, "bench.jsonl")}
}

func (r *reporter) write(s RenderSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		f, err := os.Create(r.path)
		if err != nil {
			return
		}
		r.f = f
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	r.f.Write(append(b, '\n'))
	r.f.Sync()
}

func (r *reporter) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
}

// BenchMode reports whether the app should show the benchmark instead of the
// real UI. Gated on the same environment variable that names the output file,
// so there is one switch rather than two to get out of sync.
func (a *App) BenchMode() bool {
	return os.Getenv("LITEDECK_BENCH_OUT") != ""
}

// ReportSample records one measured polling cycle.
func (a *App) ReportSample(s RenderSample) string {
	a.rep.write(s)
	return a.rep.path
}

// BenchSweepDone is called once the frontend has finished every configuration.
func (a *App) BenchSweepDone() string {
	a.rep.close()
	return a.rep.path
}
