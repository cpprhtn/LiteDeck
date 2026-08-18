package app

import (
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// A wedged nvidia-smi must not take the rest of the metrics with it.
//
// This covers the inline reading, which is what runs whenever the GPU feed is
// not answering — so it is the path a card can still hang on.
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
		"PATH=/tmp/fakebin:$PATH; "+adapter.MetricsScriptWithGPU)
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

// A server with no NVIDIA card must not look like a failing command.
//
// The shell reports the last command's status, and the GPU line is last — so on
// the ordinary server this poll exited 127, every two seconds, as a red row in
// the Command Log with a failure count climbing behind it (v1.3.0). The numbers
// on screen were right the whole time; the log was the thing that lied, and the
// log is the one place this app asks to be believed.
func TestMetricsExitZeroWithoutNvidiaSmi(t *testing.T) {
	a := connectedApp(t)
	conn, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}

	// The fixture has no nvidia-smi, which is the case under test. Exec reports
	// a non-zero exit through the result, not through err, so the status is what
	// has to be read here.
	if probe, err := conn.Exec(testCtx(t), "sh", "-c", "command -v nvidia-smi"); err == nil && probe.OK() {
		t.Skip("this fixture has nvidia-smi, so it cannot show the missing case")
	}

	res, err := conn.Exec(testCtx(t), "sh", "-c", adapter.MetricsScriptWithGPU)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !res.OK() {
		t.Errorf("exit %d on a host with no GPU — the Command Log shows this as a "+
			"failed command on every poll", res.ExitCode)
	}
	// And the rest of the snapshot still arrived.
	m, err := adapter.ParseMetrics(res.Stdout, adapter.CPUTimes{})
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}
	if m.MemTotal == 0 {
		t.Error("memory is missing")
	}
	if len(m.GPUs) != 0 {
		t.Errorf("got %d GPUs on a host with no nvidia-smi", len(m.GPUs))
	}
}

// The feed's own script has to end quietly on a server with no card.
//
// It runs as a stream, and a stream that dies on nvidia-smi's exit 127 would
// put the v1.3.0 Command Log flood back — a red row per host per reconnect —
// and leave the app unable to tell "no such program" from "the driver broke".
// The `command -v` guard turns both of those into an exit 0 with no output,
// which the feed reads as the plain fact that this machine has no card.
func TestGPUStreamScriptIsSilentWithoutNvidiaSmi(t *testing.T) {
	a := connectedApp(t)
	conn, err := a.mgr.Conn("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if probe, err := conn.Exec(testCtx(t), "sh", "-c", "command -v nvidia-smi"); err == nil && probe.OK() {
		t.Skip("this fixture has nvidia-smi, so it cannot show the missing case")
	}

	res, err := conn.Exec(testCtx(t), "sh", "-c", adapter.GPUStreamScript)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !res.OK() {
		t.Errorf("exit %d with no nvidia-smi — every connection to an ordinary "+
			"server would log a failed command", res.ExitCode)
	}
	if len(res.Stdout) != 0 {
		t.Errorf("stdout %q, want nothing", res.Stdout)
	}
}
