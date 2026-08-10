package adapter

import "testing"

func TestParseInterfacesGolden(t *testing.T) {
	ifaces, err := ParseInterfaces(loadGolden(t, "network", "ip-addr.json"))
	if err != nil {
		t.Fatalf("ParseInterfaces: %v", err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("got %d interfaces, want 2", len(ifaces))
	}

	// Real interfaces sort ahead of loopback: the address someone came to find
	// is never 127.0.0.1.
	if ifaces[0].Name != "eth0" || !ifaces[1].Loopback {
		t.Errorf("order = %s then %s", ifaces[0].Name, ifaces[1].Name)
	}

	eth := ifaces[0]
	if eth.State != "UP" || !eth.Up() {
		t.Errorf("eth0 = %+v", eth)
	}
	if eth.MTU == 0 || eth.MAC == "" {
		t.Errorf("eth0 missing mtu/mac: %+v", eth)
	}
	if len(eth.Addresses) != 1 || eth.Addresses[0].Address != "192.168.215.2" {
		t.Errorf("eth0 addresses = %+v", eth.Addresses)
	}
	if got := eth.Addresses[0].CIDR(); got != "192.168.215.2/24" {
		t.Errorf("CIDR = %q", got)
	}

	// The kernel reports loopback as UNKNOWN rather than UP. Taking that at
	// face value would show the one interface that always works as down.
	lo := ifaces[1]
	if lo.State != "UNKNOWN" {
		t.Errorf("lo state = %q", lo.State)
	}
	if !lo.Up() {
		t.Error("loopback reported as down")
	}
	if len(lo.Addresses) != 2 {
		t.Errorf("lo should have v4 and v6: %+v", lo.Addresses)
	}
}

func TestParseListenersGolden(t *testing.T) {
	ls, err := ParseListeners(loadGolden(t, "network", "ss.txt"))
	if err != nil {
		t.Fatalf("ParseListeners: %v", err)
	}
	if len(ls) != 2 {
		t.Fatalf("got %d listeners, want 2", len(ls))
	}
	for _, l := range ls {
		if l.Protocol != "tcp" || l.Port != "22" {
			t.Errorf("listener = %+v", l)
		}
		if l.Process != "sshd" || l.PID != 34 {
			t.Errorf("process not parsed: %+v", l)
		}
		if !l.Exposed {
			t.Errorf("0.0.0.0/[::] binding not marked exposed: %+v", l)
		}
	}
	// One v4 and one v6 entry.
	if ls[0].IPv6 == ls[1].IPv6 {
		t.Errorf("expected one v4 and one v6: %+v", ls)
	}
}

// TestListenerExposure is the distinction the view exists for: bound to
// loopback means local only, bound to 0.0.0.0 means reachable from outside.
func TestListenerExposure(t *testing.T) {
	const in = `Netid State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
tcp   LISTEN 0      128        127.0.0.1:5432      0.0.0.0:*     users:(("postgres",pid=91,fd=5))
tcp   LISTEN 0      511          0.0.0.0:80        0.0.0.0:*     users:(("nginx",pid=12,fd=6))
udp   UNCONN 0      0          0.0.0.0:53        0.0.0.0:*     users:(("dnsmasq",pid=7,fd=4))
tcp   LISTEN 0      128             [::]:443          [::]:*
`
	ls, err := ParseListeners([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 4 {
		t.Fatalf("got %d listeners, want 4: %+v", len(ls), ls)
	}

	byPort := map[string]Listener{}
	for _, l := range ls {
		byPort[l.Port] = l
	}

	if byPort["5432"].Exposed {
		t.Error("a loopback binding was marked exposed")
	}
	for _, p := range []string{"80", "53", "443"} {
		if !byPort[p].Exposed {
			t.Errorf("port %s should be exposed: %+v", p, byPort[p])
		}
	}
	// UDP has no LISTEN state; filtering on it would drop every UDP socket.
	if byPort["53"].Protocol != "udp" {
		t.Errorf("udp listener lost: %+v", byPort["53"])
	}
	// A socket whose owner ss could not see is still a socket.
	if byPort["443"].Process != "" {
		t.Errorf("process invented for a row that had none: %+v", byPort["443"])
	}
	if byPort["80"].Process != "nginx" || byPort["80"].PID != 12 {
		t.Errorf("process = %+v", byPort["80"])
	}

	// Exposed first, then by port number.
	if ls[len(ls)-1].Port != "5432" {
		t.Errorf("loopback-only socket should sort last: %+v", ls)
	}
}

func TestNetworkParsersReturnArrays(t *testing.T) {
	ifaces, err := ParseInterfaces([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseInterfaces(empty)", ifaces)

	ls, err := ParseListeners([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseListeners(empty)", ls)
}
