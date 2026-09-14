package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Captured from a real Rocky Linux 9.8 and a real CentOS Stream 9, both running
// firewalld, with http, https and two port rules added so the fixture has
// something to read. See testdata/golden/firewalld/provenance.txt.
func firewalldGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "firewalld", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	// The captures hold `firewall-cmd --state`, a "--" line and then the zone.
	// Only the zone reaches the parser; the state line went away when the read
	// was simplified, because a zone only arrives from a firewalld that is up.
	_, zone, ok := strings.Cut(string(b), "\n--\n")
	if !ok {
		t.Fatalf("%s is not in the captured shape", name)
	}
	return zone
}

func TestParseFirewalldRockyZone(t *testing.T) {
	got, ok := ParseFirewalld(firewalldGolden(t, "rocky9-public.txt"))
	if !ok {
		t.Fatal("a running firewalld's own zone was not recognised")
	}
	if !got.Active {
		t.Error("the zone came back, so firewalld answered, so it is running")
	}
	// `target: default` is firewalld for "refuse what no rule allows".
	if got.Incoming != "deny" || got.Outgoing != "allow" {
		t.Errorf("defaults = in %q / out %q, want deny / allow", got.Incoming, got.Outgoing)
	}

	by := map[string]FirewallRule{}
	for _, r := range got.Rules {
		by[r.To] = r
	}
	// Services by name. Resolving ssh to 22/tcp would cost a round trip per
	// service and lose the word the administrator actually typed.
	for _, name := range []string{"ssh", "http", "https", "cockpit", "dhcpv6-client"} {
		r, ok := by[name]
		if !ok {
			t.Errorf("service %s is missing from %v", name, ruleNames(by))
			continue
		}
		if r.Action != "ALLOW IN" {
			t.Errorf("%s action = %q", name, r.Action)
		}
	}
	// Ports as they were written.
	if r, ok := by["8443/tcp"]; !ok {
		t.Errorf("8443/tcp is missing from %v", ruleNames(by))
	} else if len(r.Ports) != 1 || r.Ports[0] != "8443" {
		t.Errorf("8443/tcp ports = %v, want [8443]", r.Ports)
	}
	// A range keeps its endpoints. 1-65535 is not a slice worth building.
	if r, ok := by["9090-9095/udp"]; !ok {
		t.Errorf("9090-9095/udp is missing from %v", ruleNames(by))
	} else if len(r.Ports) != 2 || r.Ports[0] != "9090" || r.Ports[1] != "9095" {
		t.Errorf("range ports = %v, want [9090 9095]", r.Ports)
	}
}

func TestParseFirewalldCentOSZone(t *testing.T) {
	got, ok := ParseFirewalld(firewalldGolden(t, "centos9-public.txt"))
	if !ok || !got.Active {
		t.Fatalf("CentOS Stream 9's zone was not read: ok=%v %+v", ok, got)
	}
	if len(got.Rules) == 0 {
		t.Error("the zone lists services and none of them arrived")
	}
}

// Everything that is not a zone has to come back as "nothing", because the
// caller turns "something" into a rules table and an empty table reads as
// "nothing is allowed in".
//
// The Debian and Ubuntu case is the one that matters here: those hosts have no
// firewall-cmd at all, this section of the read is always empty, and the screen
// they show today must not change.
func TestParseFirewalldRefusesWhatIsNotAZone(t *testing.T) {
	for name, in := range map[string]string{
		"empty":         "",
		"whitespace":    "\n  \n",
		"stopped":       firewalldGolden(t, "rocky9-stopped.txt"),
		"shell error":   "sh: 1: firewall-cmd: not found",
		"colon but not": "Status: active\nLogging: on (low)\n",
	} {
		got, ok := ParseFirewalld(in)
		if ok {
			t.Errorf("%s was taken for a firewall zone: %+v", name, got)
		}
		if len(got.Rules) != 0 || got.Active {
			t.Errorf("%s produced a status anyway: %+v", name, got)
		}
	}
}

func ruleNames(m map[string]FirewallRule) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
