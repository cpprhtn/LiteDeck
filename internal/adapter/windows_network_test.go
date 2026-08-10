package adapter

import (
	"strings"
	"testing"
)

func winNetworkCapture(t *testing.T) []byte {
	t.Helper()
	var sb strings.Builder
	for _, b := range []struct{ tag, file string }{
		{"addr", "netip.out"},
		{"adapter", "netadapter.out"},
		{"tcp", "nettcp.out"},
		{"udp", "netudp.out"},
		{"proc", "get-process.out"},
	} {
		sb.WriteString("#" + b.tag + "\n")
		sb.Write(winGolden(t, b.file))
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

func TestParseWindowsNetwork(t *testing.T) {
	ifaces, listeners, warnings, err := ParseWindowsNetwork(winNetworkCapture(t))
	if err != nil {
		t.Fatalf("ParseWindowsNetwork: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings on a complete capture: %v", warnings)
	}
	// Nil slices become JSON null and unmount the view.
	if ifaces == nil || listeners == nil || warnings == nil {
		t.Fatal("nil slice returned")
	}

	byName := map[string]Interface{}
	for _, i := range ifaces {
		byName[i.Name] = i
	}

	// Loopback has addresses but no adapter row. Building the list from
	// Get-NetAdapter instead would drop the one interface that is always there.
	lo, ok := byName["Loopback Pseudo-Interface 1"]
	if !ok {
		t.Fatalf("loopback missing; interfaces: %v", keysOf(byName))
	}
	if !lo.Loopback {
		t.Error("loopback not flagged")
	}
	if !lo.Up() {
		t.Error("loopback reported down; it is the interface that always works")
	}

	// A real adapter is enriched from the adapter table.
	eth, ok := byName["이더넷"]
	if !ok {
		t.Fatalf("ethernet interface missing; interfaces: %v", keysOf(byName))
	}
	if eth.State != "UP" || !eth.Up() {
		t.Errorf("ethernet state = %q", eth.State)
	}
	if eth.MTU != 1500 {
		t.Errorf("ethernet MTU = %d, want 1500", eth.MTU)
	}
	// Windows writes MACs with dashes and uppercase; the view expects the ip(8)
	// form.
	if !strings.Contains(eth.MAC, ":") || strings.Contains(eth.MAC, "-") {
		t.Errorf("MAC not normalised: %q", eth.MAC)
	}
	if eth.Loopback {
		t.Error("ethernet flagged as loopback")
	}

	// A disconnected adapter must read as DOWN, not UNKNOWN — the whole point of
	// the column is telling a live interface from a dead one.
	if bt, ok := byName["Bluetooth 네트워크 연결"]; ok {
		if bt.State != "DOWN" {
			t.Errorf("disconnected adapter state = %q, want DOWN", bt.State)
		}
		if bt.Up() {
			t.Error("disconnected adapter reported up")
		}
	}

	// AddressFamily arrives as an enum integer: 2 is IPv4, 23 is IPv6.
	var sawV4, sawV6 bool
	for _, i := range ifaces {
		for _, a := range i.Addresses {
			switch a.Family {
			case "inet":
				sawV4 = true
			case "inet6":
				sawV6 = true
			default:
				t.Errorf("address %q has family %q", a.Address, a.Family)
			}
		}
	}
	if !sawV4 || !sawV6 {
		t.Errorf("families seen: v4=%v v6=%v — the enum mapping is wrong", sawV4, sawV6)
	}

	// Listeners: both protocols, sorted by port, process names joined on PID.
	var tcp, udp, exposed, named int
	for _, l := range listeners {
		switch l.Protocol {
		case "tcp":
			tcp++
		case "udp":
			udp++
		default:
			t.Errorf("unexpected protocol %q", l.Protocol)
		}
		if l.Exposed {
			exposed++
		}
		if l.Process != "" {
			named++
		}
		if l.Port == "" || l.Port == "0" {
			t.Errorf("listener with no port: %+v", l)
		}
	}
	if tcp == 0 || udp == 0 {
		t.Errorf("tcp=%d udp=%d — ss -tulnp lists both and so must this", tcp, udp)
	}
	if exposed == 0 {
		t.Error("nothing marked exposed; the capture has sockets on :: and 0.0.0.0")
	}
	if named == 0 {
		t.Error("no process names; the PID join did not happen")
	}
	for i := 1; i < len(listeners); i++ {
		a, _ := atoiSafe(listeners[i-1].Port)
		b, _ := atoiSafe(listeners[i].Port)
		if a > b {
			t.Fatalf("listeners not sorted by port at %d: %s then %s",
				i, listeners[i-1].Port, listeners[i].Port)
		}
	}
}

// TestParseWindowsNetworkMissingAdapters covers a host that withholds
// Get-NetAdapter. Addresses still render; only the enrichment goes.
func TestParseWindowsNetworkMissingAdapters(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("#addr\n")
	sb.Write(winGolden(t, "netip.out"))
	sb.WriteString("\n#adapter\n")
	sb.Write(winGolden(t, "missing-cmdlet.err")) // not JSON
	sb.WriteString("\n#tcp\n")
	sb.Write(winGolden(t, "nettcp.out"))
	sb.WriteString("\n")

	ifaces, listeners, warnings, err := ParseWindowsNetwork([]byte(sb.String()))
	if err != nil {
		t.Fatalf("should degrade, not fail: %v", err)
	}
	if len(ifaces) == 0 {
		t.Fatal("no interfaces; the address block alone is enough to render")
	}
	if len(listeners) == 0 {
		t.Error("no listeners; TCP was present")
	}
	if len(warnings) == 0 {
		t.Error("no warning about the missing adapter table — a silent gap")
	}
	for _, i := range ifaces {
		if i.MAC != "" || i.MTU != 0 {
			t.Errorf("%s has MAC/MTU without an adapter table", i.Name)
		}
	}
}

func TestWinExposed(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"0.0.0.0", true},
		{"::", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"192.0.2.14", false},
		{"fe80::1111:2222:3333:4444%10", false},
	} {
		if got := winExposed(tc.addr); got != tc.want {
			t.Errorf("winExposed(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestWindowsNetworkScriptShape(t *testing.T) {
	s := WindowsNetworkScript()
	for _, tag := range []string{"#addr", "#adapter", "#tcp", "#udp", "#proc"} {
		if !strings.Contains(s, tag) {
			t.Errorf("script missing the %s block", tag)
		}
	}
	if !strings.Contains(s, "Get-NetUDPEndpoint") {
		t.Error("UDP not collected; ss -tulnp lists it on the Linux side")
	}
	if strings.Count(s, "@(") < 5 {
		t.Error("not every block forces an array")
	}
}

func keysOf(m map[string]Interface) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func atoiSafe(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, nil
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
