package app

import (
	"strings"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/webrpc"
)

// The web transport reflects over the App to expose its bindings, so the danger
// is a method becoming remotely reachable that never should be. A full pin of
// the exposed set (there are ~86) would break on every ordinary new binding and
// train people to update it without thinking — the opposite of a review gate.
// The bindings are, after all, exactly what the desktop UI already does; a new
// one is not a new risk. What IS a risk is narrow and structural, so that is
// what this floor guards:
//
//   - lifecycle methods (Startup/Shutdown), caught by the "takes a
//     context.Context" rule, must stay unreachable — /rpc/Shutdown would tear
//     down every connection;
//   - the Bench* render-benchmark spike must stay unreachable.
//
// Run against the real App, not a stand-in, so a real method that slips the net
// is caught here rather than in production.
func TestWebRPCExposureFloor(t *testing.T) {
	exposed := map[string]bool{}
	for _, m := range webrpc.New(New()).Methods() {
		exposed[m] = true
	}

	// Nothing that tears the process down, or that is dev-only spike infra.
	for _, banned := range []string{"Startup", "Shutdown"} {
		if exposed[banned] {
			t.Errorf("%s is reachable over /rpc — it must never be", banned)
		}
	}
	for m := range exposed {
		if strings.HasPrefix(m, "Bench") {
			t.Errorf("%s (benchmark spike) is reachable over /rpc", m)
		}
	}

	// The bindings the whole UI depends on must be reachable, or the web app
	// cannot boot or connect. A representative floor, not an exhaustive list.
	for _, needed := range []string{
		"Bootstrap", "Platform", "ListHosts", "ConnectHost", "DisconnectHost",
		"AnswerHostKey", "AnswerSecret", "CommandLog", "HostMetrics",
		"OpenTerminal", "WriteTerminal", "SaveTextFile", "DeletePaths",
	} {
		if !exposed[needed] {
			t.Errorf("%s is not reachable over /rpc — the web UI needs it", needed)
		}
	}

	// A catastrophic drop (the reflection walk breaks, exposing nothing or
	// almost nothing) should fail loudly rather than silently shipping a UI
	// that cannot call anything.
	if n := len(exposed); n < 60 {
		t.Errorf("only %d methods exposed — the binding surface looks broken", n)
	}
}
