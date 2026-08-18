package app

import (
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// A wedged nvidia-smi must not take the rest of the metrics with it.
//
// The GPU line rides on the same script as CPU, memory and disk, so a driver
// fault that leaves nvidia-smi blocked in the kernel would black out the whole
// summary bar until pollTimeout — twenty seconds, at the moment those numbers
// matter most. The timeout caps it; this proves the cap by putting a hanging
// nvidia-smi on PATH.
func TestMetricsSurviveAHangingNvidiaSmi(t *testing.T) {
	a := connectedApp(t)
	conn, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}

	// A fake that never returns, earlier on PATH than anything real.
	if _, err := conn.Exec(testCtx(t), "sh", "-c",
		"mkdir -p /tmp/fakebin && printf '#!/bin/sh\\nsleep 300\\n' > /tmp/fakebin/nvidia-smi "+
			"&& chmod +x /tmp/fakebin/nvidia-smi"); err != nil {
		t.Skipf("could not plant the fake: %v", err)
	}
	if _, err := conn.Exec(testCtx(t), "sh", "-c", "command -v timeout"); err != nil {
		t.Skip("this fixture has no coreutils timeout, which is the case the fallback covers")
	}

	start := time.Now()
	res, err := conn.Exec(testCtx(t), "sh", "-c",
		"PATH=/tmp/fakebin:$PATH; "+adapter.MetricsScript)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	took := time.Since(start)

	// Five seconds for the cap, plus room for a slow container.
	if took > 12*time.Second {
		t.Errorf("the poll took %s; the timeout did not cap the hang", took)
	}

	// And the numbers that were never in doubt are still there.
	m, err := adapter.ParseMetrics(res.Stdout, adapter.CPUTimes{})
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}
	if m.MemTotal == 0 {
		t.Error("memory went dark because the GPU line hung")
	}
	if len(m.GPUs) != 0 {
		t.Errorf("got %d GPUs from a command that never answered", len(m.GPUs))
	}
	if !strings.Contains(string(res.Stdout), "#gpu") {
		t.Error("the gpu section marker is missing, so the sections may have shifted")
	}
}
