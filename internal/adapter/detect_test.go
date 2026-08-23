package adapter

import "testing"

// The processor's own name, from a file whose key is not the same everywhere.
func TestCPUModel(t *testing.T) {
	x86 := "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz\n" +
		"processor\t: 1\nmodel name\t: Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz\n"
	if got := cpuModel(x86); got != "Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz" {
		t.Errorf("x86: %q", got)
	}

	// arm64 names nothing a person would recognise under "model name"; the
	// useful line is the board or the SoC.
	arm := "processor\t: 0\nBogoMIPS\t: 108.00\nFeatures\t: fp asimd\n" +
		"CPU implementer\t: 0x41\nCPU part\t: 0xd0b\n\nModel\t\t: Raspberry Pi 5 Model B Rev 1.0\n"
	if got := cpuModel(arm); got != "Raspberry Pi 5 Model B Rev 1.0" {
		t.Errorf("arm: %q", got)
	}

	// A machine that names itself in none of them gets no name, not a wrong one.
	if got := cpuModel("processor\t: 0\nflags\t: fpu vme\n"); got != "" {
		t.Errorf("unnamed: %q, want empty", got)
	}
	if got := cpuModel(""); got != "" {
		t.Errorf("empty input: %q", got)
	}
}

// /proc/cpuinfo repeats the whole block once per core. A 64-core box must not
// produce a name 64 times, and the answer must not depend on which core the
// loop happened to end on.
func TestCPUModelTakesTheFirstBlock(t *testing.T) {
	in := "model name\t: First CPU\nprocessor\t: 1\nmodel name\t: Second CPU\n"
	if got := cpuModel(in); got != "First CPU" {
		t.Errorf("%q, want the first", got)
	}
}
