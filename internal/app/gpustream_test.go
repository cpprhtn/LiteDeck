package app

import (
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// One card, sampled twice, is one tile — not two.
//
// nvidia-smi prints a row per card per interval and nothing in between, so the
// only thing marking where one sample ends is the index column going back down.
// Get that wrong and a single-GPU box grows a second card every two seconds.
func TestGPUFeedSampleBoundary(t *testing.T) {
	f := &gpuFeed{started: time.Now()}
	const row0 = "0, NVIDIA GeForce RTX 4090, 31, 42, 55, 24564, 1024"
	const row0b = "0, NVIDIA GeForce RTX 4090, 77, 61, 68, 24564, 8192"

	f.row(row0)
	if got := len(f.latest); got != 1 {
		t.Fatalf("after one row: %d cards, want 1", got)
	}
	f.row(row0b)
	if got := len(f.latest); got != 1 {
		t.Fatalf("after a second sample of the same card: %d cards, want 1", got)
	}
	if f.latest[0].Utilization != 31 {
		t.Errorf("utilization %v — the completed sample should be the first one, "+
			"the second is still arriving", f.latest[0].Utilization)
	}
	f.row(row0)
	if got := f.latest[0].Utilization; got != 77 {
		t.Errorf("utilization %v after a third sample, want 77 — the feed is not "+
			"advancing", got)
	}
}

// Two cards must both appear, and must not be split across samples.
func TestGPUFeedTwoCards(t *testing.T) {
	f := &gpuFeed{started: time.Now()}
	rows := []string{
		"0, NVIDIA A100, 10, [N/A], 40, 40960, 1024",
		"1, NVIDIA A100, 20, [N/A], 45, 40960, 2048",
	}
	for range 2 { // two full samples
		for _, r := range rows {
			f.row(r)
		}
	}
	if got := len(f.latest); got != 2 {
		t.Fatalf("%d cards, want 2", got)
	}
	if f.latest[0].Index != 0 || f.latest[1].Index != 1 {
		t.Errorf("indices %d,%d — the sample was assembled out of order",
			f.latest[0].Index, f.latest[1].Index)
	}
	// Passively cooled cards answer [N/A] for the fan, which must stay distinct
	// from a genuine zero — a 0 there reads as a stopped fan.
	if f.latest[0].Fan != -1 {
		t.Errorf("fan %v for an [N/A] card, want -1", f.latest[0].Fan)
	}
}

// The first sample has to show before the boundary that completes it.
//
// Otherwise a single-GPU box shows nothing for one whole interval after
// connecting, which looks exactly like a card that is not detected.
func TestGPUFeedShowsTheFirstSampleImmediately(t *testing.T) {
	f := &gpuFeed{started: time.Now()}
	f.row("0, NVIDIA L4, 5, 30, 38, 23034, 512")
	if len(f.latest) != 1 {
		t.Fatalf("nothing to show after the first row ever received")
	}
	if f.latest[0].Name != "NVIDIA L4" {
		t.Errorf("name %q", f.latest[0].Name)
	}
}

// A server with no card is an answer, not a failure — and not one to keep
// asking about.
func TestGPUFeedNoCardIsLiveAndEmpty(t *testing.T) {
	w := newGPUWatcher()
	f := &gpuFeed{started: time.Now()}
	w.byID["h"] = f
	f.closed() // the stream ran, said nothing, exited

	rows, live := w.sample("h")
	if !live {
		t.Error("a host with no card should not fall back to running an " +
			"nvidia-smi it does not have")
	}
	if len(rows) != 0 {
		t.Errorf("%d cards on a host with none", len(rows))
	}
	if !f.noCard {
		t.Error("the feed did not record that this host has no card")
	}
}

// A feed that stops producing must hand the reading back, not freeze the tiles.
//
// This is the nvidia-smi that block-buffers into the pipe: the process is alive
// and stays alive, so nothing about it times out on its own. Without this check
// the summary bar would show a sample from minutes ago and go on showing it.
func TestGPUFeedStaleFallsBackToInline(t *testing.T) {
	w := newGPUWatcher()
	f := &gpuFeed{started: time.Now().Add(-time.Hour)}
	w.byID["h"] = f
	f.row("0, NVIDIA A100, 10, 50, 40, 40960, 1024")
	f.lastRow = time.Now().Add(-time.Hour)

	if _, live := w.sample("h"); live {
		t.Fatal("a feed an hour behind is still reported as answering")
	}
	if !f.stalled {
		t.Error("the stalled feed was left open; it holds a channel for the " +
			"life of the app")
	}
	// And it does not come back on its own — a buffering nvidia-smi will not
	// start line-buffering because we asked twice.
	if _, live := w.sample("h"); live {
		t.Error("the feed reported itself answering again after stalling")
	}
}

// Before any feed exists the poll has to read the card inline, so the first
// screen after connecting is not an empty space.
func TestGPUNoFeedMeansInline(t *testing.T) {
	w := newGPUWatcher()
	if _, live := w.sample("never-seen"); live {
		t.Error("a host with no feed reported as answering")
	}
}

// forget has to actually forget: a card can be added to a machine between
// connections, and "no card" must not outlive the connection that observed it.
func TestGPUForgetClearsNoCard(t *testing.T) {
	w := newGPUWatcher()
	f := &gpuFeed{started: time.Now()}
	w.byID["h"] = f
	f.closed()
	if _, live := w.sample("h"); !live {
		t.Fatal("setup: expected the no-card answer")
	}
	w.forget("h")
	if _, live := w.sample("h"); live {
		t.Error("the no-card verdict survived the connection that produced it")
	}
}

// The two readings must ask for the same columns in the same order. They are
// parsed by the same code, so a drift here is a silently mislabelled tile —
// temperature shown as fan speed, say.
func TestGPUScriptsAgreeOnColumns(t *testing.T) {
	for _, s := range []string{adapter.MetricsScriptWithGPU, adapter.GPUStreamScript} {
		if !strings.Contains(s, "--query-gpu=index,name,utilization.gpu,fan.speed,temperature.gpu,memory.total,memory.used") {
			t.Errorf("column list drifted in:\n%s", s)
		}
		if !strings.Contains(s, "--format=csv,noheader,nounits") {
			t.Errorf("format drifted in:\n%s", s)
		}
	}
}
