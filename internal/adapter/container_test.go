package adapter

import (
	"testing"
)

func containersByName(cs []Container) map[string]Container {
	m := make(map[string]Container, len(cs))
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestParseContainersGolden(t *testing.T) {
	cs, err := ParseContainers(loadGolden(t, "docker", "ps.jsonl"))
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	if len(cs) != 5 {
		t.Fatalf("got %d containers, want 5", len(cs))
	}
	m := containersByName(cs)

	running := m["ld-c1"]
	if !running.Running() || running.State != "running" {
		t.Errorf("ld-c1 = %+v", running)
	}
	if running.Image != "alpine:3.20" {
		t.Errorf("image = %q", running.Image)
	}
	// The Command column arrives wrapped in quotes; the view should not show them.
	if running.Command == "" || running.Command[0] == '"' {
		t.Errorf("command still quoted: %q", running.Command)
	}

	// A container that was created but never started.
	if got := m["ld-c4"].State; got != "created" {
		t.Errorf("ld-c4 state = %q, want created", got)
	}
	if len(m["ld-c4"].Ports) != 0 {
		t.Errorf("ld-c4 has ports: %+v", m["ld-c4"].Ports)
	}

	// Exit codes matter: "exited" alone does not distinguish a clean shutdown
	// from a crash, and that is the first thing anyone wants to know.
	c2 := m["ld-c2"]
	if c2.State != "exited" {
		t.Errorf("ld-c2 state = %q", c2.State)
	}
	if c2.ExitCode != 3 {
		t.Errorf("ld-c2 exit code = %d, want 3", c2.ExitCode)
	}
	if m["ld-c1"].ExitCode != -1 {
		t.Errorf("running container reported exit code %d, want -1", m["ld-c1"].ExitCode)
	}

	// Running containers sort ahead of stopped ones.
	stoppedSeen := false
	for _, c := range cs {
		if !c.Running() {
			stoppedSeen = true
		} else if stoppedSeen {
			t.Error("a running container appears after a stopped one")
			break
		}
	}
}

// TestParsePortsDeduplicatesAddressFamilies: docker lists IPv4 and IPv6 for the
// same published port as separate entries. The user published one port; showing
// two is noise that makes a busy container unreadable.
func TestParsePortsDeduplicatesAddressFamilies(t *testing.T) {
	got := ParsePorts("0.0.0.0:18080->80/tcp, [::]:18080->80/tcp")
	if len(got) != 1 {
		t.Fatalf("got %d ports, want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.HostPort != "18080" || p.Container != "80" || p.Protocol != "tcp" {
		t.Errorf("port = %+v", p)
	}
	if p.HostIP != "0.0.0.0" {
		t.Errorf("host ip = %q, want the v4 entry", p.HostIP)
	}
}

func TestParsePorts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Port
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "bound to a specific interface",
			in:   "127.0.0.1:19090->9090/tcp",
			want: []Port{{HostIP: "127.0.0.1", HostPort: "19090", Container: "9090", Protocol: "tcp"}},
		},
		{
			name: "udp",
			in:   "0.0.0.0:19091->9091/udp",
			want: []Port{{HostIP: "0.0.0.0", HostPort: "19091", Container: "9091", Protocol: "udp"}},
		},
		{
			name: "exposed but not published",
			in:   "9090/tcp",
			want: []Port{{Container: "9090", Protocol: "tcp"}},
		},
		{
			name: "mixed",
			in:   "0.0.0.0:18080->80/tcp, [::]:18080->80/tcp, 443/tcp",
			want: []Port{
				{HostIP: "0.0.0.0", HostPort: "18080", Container: "80", Protocol: "tcp"},
				{Container: "443", Protocol: "tcp"},
			},
		},
		{
			name: "ipv6 only",
			in:   "[::]:5000->5000/tcp",
			want: []Port{{HostIP: "[::]", HostPort: "5000", Container: "5000", Protocol: "tcp"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParsePorts(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d ports, want %d: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("port %d = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestParseExitCode(t *testing.T) {
	cases := map[string]int{
		"Exited (0) 5 minutes ago":  0,
		"Exited (3) 3 seconds ago":  3,
		"Exited (137) 1 hour ago":   137,
		"Up 2 hours":                -1,
		"Created":                   -1,
		"Restarting (1) 2 secs ago": -1, // not an exit
		"":                          -1,
	}
	for status, want := range cases {
		if got := parseExitCode(status); got != want {
			t.Errorf("parseExitCode(%q) = %d, want %d", status, got, want)
		}
	}
}

func TestParseContainersIgnoresMalformedLines(t *testing.T) {
	input := `not json at all
{"ID":"abc123","Names":"good","Image":"alpine","State":"running","Status":"Up 1 second"}
{"ID":"","Names":"no id"}

`
	cs, err := ParseContainers([]byte(input))
	if err != nil {
		t.Fatalf("ParseContainers: %v", err)
	}
	// One bad line must not lose the rest of the list.
	if len(cs) != 1 || cs[0].Name != "good" {
		t.Errorf("got %+v, want just the valid row", cs)
	}
}

func TestPSArgsContainers(t *testing.T) {
	args := PSArgsContainers()
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	// --no-trunc matters: without it the command column arrives with an
	// ellipsis and the ID is shortened, which breaks copy-paste.
	if !containsAll(joined, "ps", "-a", "--no-trunc", "{{json .}}") {
		t.Errorf("PSArgsContainers() = %q", args)
	}
}

// The running-only listing exists to be cheap, and the one word that decides
// that is the one it must not have. `-a` makes the daemon walk every container
// the host has ever kept; putting it back here would make this listing exactly
// as slow as the one it is meant to precede, while looking correct.
func TestPSArgsRunningContainersOmitsAll(t *testing.T) {
	args := PSArgsRunningContainers()
	for _, a := range args {
		if a == "-a" || a == "--all" {
			t.Fatalf("PSArgsRunningContainers() = %q — the point is to not walk every container", args)
		}
	}
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	// The rest has to match the full listing: same columns, same labels, same
	// parser. Compose grouping reads those labels, so a leaner format here
	// would ungroup the fast pass and regroup it a beat later.
	if !containsAll(joined, "ps", "--no-trunc", "{{json .}}") {
		t.Errorf("PSArgsRunningContainers() = %q", args)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
