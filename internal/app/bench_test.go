package app

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The half of M0 risk ④ that can be measured without a window: how many bytes
// each poll pushes across the boundary, and what Go spends producing them.
// Webview IPC and React commit cost are measured in the app itself.

var sizes = []int{500, 2000, 5000, 10000}

func TestPayloadSize(t *testing.T) {
	t.Log("행 수    스냅샷        diff          비율")
	for _, n := range sizes {
		a := New()
		a.BenchResize(n)

		// Warm up so the diff reflects steady-state churn rather than a table
		// that has never ticked.
		a.BenchDiff()

		snap, err := json.Marshal(a.BenchSnapshot())
		if err != nil {
			t.Fatal(err)
		}
		diff, err := json.Marshal(a.BenchDiff())
		if err != nil {
			t.Fatal(err)
		}

		ratio := float64(len(diff)) / float64(len(snap)) * 100
		t.Logf("%6d  %8.1f KB  %8.1f KB  %5.1f%%",
			n, float64(len(snap))/1024, float64(len(diff))/1024, ratio)

		if len(diff) >= len(snap) {
			t.Errorf("n=%d: diff (%d B) is not smaller than snapshot (%d B)", n, len(diff), len(snap))
		}
	}
}

func BenchmarkSnapshot(b *testing.B) {
	for _, n := range sizes {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			a := New()
			a.BenchResize(n)
			b.ReportAllocs()
			for range b.N {
				out, err := json.Marshal(a.BenchSnapshot())
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(out)))
			}
		})
	}
}

func BenchmarkDiff(b *testing.B) {
	for _, n := range sizes {
		b.Run(fmt.Sprintf("rows=%d", n), func(b *testing.B) {
			a := New()
			a.BenchResize(n)
			b.ReportAllocs()
			for range b.N {
				out, err := json.Marshal(a.BenchDiff())
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(out)))
			}
		})
	}
}
