package app

import (
	"context"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The GPU feed (§4.7).
//
// The summary bar reads CPU, memory and disk out of files the kernel already
// keeps, so a poll costs nothing worth measuring. The GPU was the exception: it
// needs nvidia-smi, and nvidia-smi initialises NVML on startup and tears it
// down on exit. Called once per poll, on a two second cadence, it paid that
// setup cost forever on a machine whose entire job is to be busy with something
// else.
//
// So the card is read by one process that stays alive and reports on a loop.
// One NVML session, one channel, and each further sample is a library call.
//
// Everything here is written so that failure means *slower*, never *wrong*. A
// server that will not hold the channel, a nvidia-smi that buffers its output,
// a driver that dies and comes back — each of those drops the reading back to
// the inline query the app used before, which is unchanged and still correct.
// The tiles do not go blank; they go back to costing what they used to cost.

// gpuFeedGrace is how long a feed gets to produce its first row before it
// counts as not answering. Generous on purpose: it covers the `command -v`
// guard, nvidia-smi's own startup, and a slow link, and the only cost of
// waiting is that the inline query keeps running meanwhile.
const gpuFeedGrace = 10 * time.Second

// gpuFeedRetry is the wait before reopening a feed that had been working and
// then ended — a driver reload, a card reset. Long enough that a card stuck in
// a crash loop cannot turn this into a reconnect storm.
const gpuFeedRetry = 30 * time.Second

// gpuWatcher owns one feed per connected host.
type gpuWatcher struct {
	mu   sync.Mutex
	byID map[string]*gpuFeed
}

func newGPUWatcher() *gpuWatcher {
	return &gpuWatcher{byID: make(map[string]*gpuFeed)}
}

type gpuFeed struct {
	cancel context.CancelFunc

	mu       sync.Mutex
	stream   *sshcore.Stream
	starting bool // an ensure() is opening the channel right now

	// latest is the last complete sample; batch is the one still arriving.
	// Rows come in one at a time and nvidia-smi does not separate samples, so
	// the boundary is the index column going back down.
	latest  []adapter.GPU
	batch   []adapter.GPU
	settled bool // a boundary has been seen, so latest is a whole sample

	started time.Time
	lastRow time.Time
	rows    int

	// The three ways a feed stops being the thing that answers.
	//
	// noCard is the ordinary server: the stream ran, said nothing, exited. Not
	// an error and not worth retrying, because a card does not appear inside a
	// connection.
	//
	// stalled is nvidia-smi block-buffering into the pipe. The process is alive
	// and will go on being alive; it is the samples that are stuck. Nothing
	// about that resolves on a retry, so the host stays on the inline query.
	//
	// retryAt is everything else — it worked, then it stopped.
	noCard  bool
	stalled bool
	retryAt time.Time
}

// sample reports the current GPU rows and whether the feed is answering.
//
// A false second return is the caller's instruction to read the card inline
// this time round. It is deliberately also the answer before the first feed
// exists, so the very first poll after connecting shows the tiles immediately
// rather than a blank space that fills in two seconds later.
func (w *gpuWatcher) sample(hostID string) ([]adapter.GPU, bool) {
	w.mu.Lock()
	f := w.byID[hostID]
	w.mu.Unlock()
	if f == nil {
		return nil, false
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// A host with no card is a settled answer, not a missing one: the feed
	// already asked and nothing was there. Reporting it as live is what keeps
	// the ordinary server from running an nvidia-smi it does not have.
	if f.noCard {
		return nil, true
	}
	if f.stalled || !f.retryAt.IsZero() {
		return nil, false
	}

	since := f.lastRow
	grace := adapter.GPUStreamInterval * 3
	if f.rows == 0 {
		since, grace = f.started, gpuFeedGrace
	}
	if time.Since(since) > grace {
		// Alive but not talking. Close it — a buffered nvidia-smi would hold
		// the channel open for as long as the app runs otherwise — and leave
		// this host on the inline query until it reconnects.
		f.stalled = true
		if f.stream != nil {
			_ = f.stream.Close()
		}
		if f.cancel != nil {
			f.cancel()
		}
		return nil, false
	}
	if f.latest == nil {
		return nil, false
	}
	return append([]adapter.GPU(nil), f.latest...), true
}

// ensure starts a feed for the host if one is wanted and none is running.
//
// Called after each poll rather than on connect, because it is the poll that
// proves somebody is looking at this host. A host nobody has opened costs
// nothing.
func (w *gpuWatcher) ensure(hostID string, conn *sshcore.Conn) {
	w.mu.Lock()
	f := w.byID[hostID]
	switch {
	case f == nil:
		f = &gpuFeed{started: time.Now(), starting: true}
		w.byID[hostID] = f
	default:
		f.mu.Lock()
		skip := f.stream != nil || f.starting || f.noCard || f.stalled ||
			(!f.retryAt.IsZero() && time.Now().Before(f.retryAt))
		if !skip {
			f.starting = true
			f.retryAt = time.Time{}
			f.started = time.Now()
			f.rows = 0
		}
		f.mu.Unlock()
		if skip {
			w.mu.Unlock()
			return
		}
	}
	w.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := conn.OpenStreamOpts(ctx,
		sshcore.StreamOptions{Background: true},
		func(line string, isStderr bool) {
			if !isStderr {
				f.row(line)
			}
		},
		func(error) { f.closed() },
		// A compile-time constant with nothing interpolated, which is why `sh -c`
		// here does not violate the argv-only rule (§3.2b).
		"sh", "-c", adapter.GPUStreamScript,
	)
	if err != nil {
		// No channel to spare, or the server refused. Neither is worth an error
		// on screen — the inline query covers it — so this is only a note to try
		// again later.
		cancel()
		f.mu.Lock()
		f.starting = false
		f.retryAt = time.Now().Add(gpuFeedRetry)
		f.mu.Unlock()
		return
	}

	f.mu.Lock()
	f.cancel, f.stream, f.starting = cancel, stream, false
	f.mu.Unlock()
}

// row folds one CSV line into the sample being assembled.
func (f *gpuFeed) row(line string) {
	g, ok := adapter.ParseGPULine(line, len(f.batch))
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rows++
	f.lastRow = time.Now()

	// nvidia-smi prints one row per card per interval with nothing between
	// samples, so a card index that did not go up is where the next sample
	// began.
	if n := len(f.batch); n > 0 && g.Index <= f.batch[n-1].Index {
		f.latest, f.batch, f.settled = f.batch, nil, true
	}
	f.batch = append(f.batch, g)

	// Until the first boundary the app has never seen a whole sample, and
	// waiting for one would leave the tiles empty for an interval. Show the
	// rows as they arrive; the first boundary takes over from there.
	if !f.settled {
		f.latest = append([]adapter.GPU(nil), f.batch...)
	}
}

// closed records why the stream ended.
func (f *gpuFeed) closed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stream = nil
	if f.rows == 0 {
		// It ran and named no card. Whether that was a missing nvidia-smi, a
		// driver with no card under it, or a card that could not be read, the
		// useful conclusion is the same and retrying it every thirty seconds
		// for the life of the connection would only be noise.
		f.noCard = true
		return
	}
	f.retryAt = time.Now().Add(gpuFeedRetry)
}

// forget closes the feed for a host. Called when the connection goes away, so
// that the next one starts by asking again rather than trusting an answer from
// a server that may have had a card added since.
func (w *gpuWatcher) forget(hostID string) {
	w.mu.Lock()
	f := w.byID[hostID]
	delete(w.byID, hostID)
	w.mu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	stream, cancel := f.stream, f.cancel
	f.stream = nil
	f.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	if cancel != nil {
		cancel()
	}
}
