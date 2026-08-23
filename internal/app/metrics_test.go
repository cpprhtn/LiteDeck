package app

import (
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// The counters are cumulative, so a rate is a difference divided by the time
// between samples — not by the interval anyone intended. Idle backoff stretches
// that interval sixfold (A-45), and dividing by a constant would make a quiet
// machine look six times busier the moment nobody was watching.
func TestRatesDivideByElapsedTime(t *testing.T) {
	h := newCPUHistory()
	m1 := adapter.Metrics{
		Net:    []adapter.NetIface{{Name: "eth0", RxBytes: 1000, TxBytes: 500, RxRate: -1, TxRate: -1}},
		DiskIO: []adapter.DiskIO{{Name: "sda", ReadBytes: 2000, WriteBytes: 0, ReadRate: -1, WriteRate: -1}},
	}
	h.rates("h", &m1)
	// Nothing to difference against yet.
	if m1.Net[0].RxRate != -1 {
		t.Errorf("first sample produced a rate: %v", m1.Net[0].RxRate)
	}

	// Backdate the stored sample by a known interval rather than sleeping.
	h.mu.Lock()
	c := h.counters["h"]
	c.at = c.at.Add(-4 * time.Second)
	h.counters["h"] = c
	h.mu.Unlock()

	m2 := adapter.Metrics{
		Net:    []adapter.NetIface{{Name: "eth0", RxBytes: 5000, TxBytes: 500, RxRate: -1, TxRate: -1}},
		DiskIO: []adapter.DiskIO{{Name: "sda", ReadBytes: 2000, WriteBytes: 0, ReadRate: -1, WriteRate: -1}},
	}
	h.rates("h", &m2)

	// 4000 bytes over 4 seconds.
	if got := m2.Net[0].RxRate; got < 990 || got > 1010 {
		t.Errorf("rx rate %v, want ~1000/s", got)
	}
	// An unchanged counter is genuinely zero, not unknown.
	if got := m2.Net[0].TxRate; got != 0 {
		t.Errorf("tx rate %v for an unchanged counter, want 0", got)
	}
	if got := m2.DiskIO[0].ReadRate; got != 0 {
		t.Errorf("read rate %v, want 0", got)
	}
}

// A counter that went backwards is a reboot, or an interface torn down and
// recreated. Reporting the difference would draw a spike of the entire counter.
func TestRatesRejectACounterThatWentBackwards(t *testing.T) {
	h := newCPUHistory()
	m1 := adapter.Metrics{Net: []adapter.NetIface{{Name: "eth0", RxBytes: 9_000_000, RxRate: -1}}}
	h.rates("h", &m1)

	h.mu.Lock()
	c := h.counters["h"]
	c.at = c.at.Add(-2 * time.Second)
	h.counters["h"] = c
	h.mu.Unlock()

	m2 := adapter.Metrics{Net: []adapter.NetIface{{Name: "eth0", RxBytes: 12, RxRate: -1}}}
	h.rates("h", &m2)
	if got := m2.Net[0].RxRate; got != -1 {
		t.Errorf("rate %v after a counter reset, want unknown", got)
	}
}

// An interface that appears between samples has nothing to difference against
// and must not inherit another one's history.
func TestRatesIgnoreAnInterfaceItHasNotSeen(t *testing.T) {
	h := newCPUHistory()
	m1 := adapter.Metrics{Net: []adapter.NetIface{{Name: "eth0", RxBytes: 100, RxRate: -1}}}
	h.rates("h", &m1)

	h.mu.Lock()
	c := h.counters["h"]
	c.at = c.at.Add(-2 * time.Second)
	h.counters["h"] = c
	h.mu.Unlock()

	m2 := adapter.Metrics{Net: []adapter.NetIface{
		{Name: "eth0", RxBytes: 300, RxRate: -1},
		{Name: "wg0", RxBytes: 99999, RxRate: -1},
	}}
	h.rates("h", &m2)
	if m2.Net[0].RxRate <= 0 {
		t.Errorf("eth0 rate %v", m2.Net[0].RxRate)
	}
	if got := m2.Net[1].RxRate; got != -1 {
		t.Errorf("wg0 rate %v on its first sighting, want unknown", got)
	}
}
