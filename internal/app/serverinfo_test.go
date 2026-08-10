package app

import (
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
)

// TestServerInfoViewSupportedMatchesPlatform guards a drift that shipped once.
//
// Whether a host is supported was decided in two places: Go's Platform.Supported()
// and, separately, `platform !== 'linux'` in the frontend. When the Windows
// adapter landed only the Go side learned about it, so the backend served a
// working service list while the UI drew "this server is not supported" on top of
// it — and nothing failed, because both halves were internally consistent.
//
// The fix was to send the answer instead of deriving it. This test is what stops
// a third copy of the question appearing.
func TestServerInfoViewSupportedMatchesPlatform(t *testing.T) {
	for _, p := range []adapter.Platform{
		adapter.PlatformLinux,
		adapter.PlatformWindows,
		adapter.PlatformDarwin,
		adapter.PlatformBSD,
		adapter.PlatformUnknown,
	} {
		v := newServerInfoView(adapter.ServerInfo{Platform: p})
		if v.Supported != p.Supported() {
			t.Errorf("platform %q: view says supported=%v, adapter says %v",
				p, v.Supported, p.Supported())
		}
	}
}

// TestSupportedPlatformsHaveCapabilities is the other half: a platform can be
// "supported" and still have every tab switched off, which on screen is the same
// blank window with no reason given.
func TestSupportedPlatformsHaveCapabilities(t *testing.T) {
	cases := []struct {
		name string
		info adapter.ServerInfo
		want []adapter.Capability
	}{
		{
			name: "linux with systemd",
			info: adapter.ServerInfo{Platform: adapter.PlatformLinux, HasSystemd: true},
			want: []adapter.Capability{
				adapter.CapServices, adapter.CapProcesses,
				adapter.CapMetrics, adapter.CapNetwork,
			},
		},
		{
			name: "windows",
			info: adapter.ServerInfo{Platform: adapter.PlatformWindows},
			want: []adapter.Capability{
				adapter.CapServices, adapter.CapProcesses,
				adapter.CapMetrics, adapter.CapNetwork,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newServerInfoView(tc.info)
			if !v.Supported {
				t.Fatalf("%s reported unsupported", tc.name)
			}
			for _, c := range tc.want {
				if !v.Capabilities[c] {
					t.Errorf("capability %q off on a supported platform", c)
				}
			}
		})
	}
}

// TestUnsupportedPlatformsHaveNoCapabilities keeps the polling views from
// starting against a host that cannot answer — the failure that this whole gate
// exists to prevent.
func TestUnsupportedPlatformsHaveNoCapabilities(t *testing.T) {
	for _, p := range []adapter.Platform{
		adapter.PlatformDarwin, adapter.PlatformBSD, adapter.PlatformUnknown,
	} {
		v := newServerInfoView(adapter.ServerInfo{Platform: p})
		if v.Supported {
			t.Errorf("platform %q reported supported without an adapter", p)
		}
		for c, on := range v.Capabilities {
			if on {
				t.Errorf("platform %q has capability %q enabled", p, c)
			}
		}
	}
}
