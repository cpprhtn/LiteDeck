package adapter

// The network view (v1.x): interfaces and listening sockets.
//
// Answers the two questions people actually open a network tab for — "what
// address is this box on" and "what is listening on that port" — and stops
// there. Traffic graphs and packet capture belong to other tools.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Interface is one network interface.
type Interface struct {
	Name      string    `json:"name"`
	MAC       string    `json:"mac,omitempty"`
	State     string    `json:"state"` // UP, DOWN, UNKNOWN
	MTU       int       `json:"mtu"`
	Loopback  bool      `json:"loopback"`
	Addresses []Address `json:"addresses"`
}

// Up reports whether the interface is carrying traffic.
//
// Loopback is reported as UNKNOWN by the kernel rather than UP, which would
// otherwise show the one interface that is always working as down.
func (i Interface) Up() bool { return i.State == "UP" || (i.Loopback && i.State == "UNKNOWN") }

// Address is one address on an interface.
type Address struct {
	Address string `json:"address"`
	Prefix  int    `json:"prefix"`
	Family  string `json:"family"` // inet, inet6
	Scope   string `json:"scope,omitempty"`
}

func (a Address) CIDR() string { return fmt.Sprintf("%s/%d", a.Address, a.Prefix) }

// Listener is one listening socket.
type Listener struct {
	Protocol string `json:"protocol"` // tcp, udp
	Address  string `json:"address"`
	Port     string `json:"port"`
	Process  string `json:"process,omitempty"`
	PID      int    `json:"pid,omitempty"`
	IPv6     bool   `json:"ipv6"`
	// Exposed marks a socket bound to all interfaces rather than loopback —
	// the difference between "reachable from the internet" and "local only",
	// which is the whole reason to look at this list.
	Exposed bool `json:"exposed"`
}

// IPAddrArgs returns the argv for listing interfaces.
func IPAddrArgs() []string { return []string{"-j", "addr"} }

// SSArgs returns the argv for listing listening sockets.
//
// -p (process names) needs privileges for other users' sockets, but the command
// still succeeds and simply omits what it cannot see — so it is always asked
// for, and a missing process name is not an error.
func SSArgs() []string { return []string{"-tulnp"} }

// ipAddrRow mirrors one element of `ip -j addr`.
type ipAddrRow struct {
	IfName    string   `json:"ifname"`
	Address   string   `json:"address"`
	OperState string   `json:"operstate"`
	MTU       int      `json:"mtu"`
	Flags     []string `json:"flags"`
	AddrInfo  []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
		Scope     string `json:"scope"`
	} `json:"addr_info"`
}

// ParseInterfaces parses `ip -j addr`.
func ParseInterfaces(data []byte) ([]Interface, error) {
	out := []Interface{}
	if len(bytes.TrimSpace(data)) == 0 {
		return out, nil
	}

	var rows []ipAddrRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("adapter: parse ip addr: %w", err)
	}

	for _, r := range rows {
		iface := Interface{
			Name:      r.IfName,
			MAC:       r.Address,
			State:     r.OperState,
			MTU:       r.MTU,
			Addresses: []Address{},
		}
		for _, f := range r.Flags {
			if f == "LOOPBACK" {
				iface.Loopback = true
			}
		}
		for _, a := range r.AddrInfo {
			iface.Addresses = append(iface.Addresses, Address{
				Address: a.Local, Prefix: a.PrefixLen,
				Family: a.Family, Scope: a.Scope,
			})
		}
		out = append(out, iface)
	}

	// Real interfaces first, loopback last: the address someone came to find is
	// never 127.0.0.1.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Loopback != out[j].Loopback {
			return !out[i].Loopback
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ParseListeners parses `ss -tulnp`.
//
// ss has no JSON output in the versions this project supports, so this is a
// priority-4 human-output parser — quarantined here and golden-file tested.
// The columns are:
//
//	Netid State Recv-Q Send-Q Local:Port Peer:Port Process
func ParseListeners(data []byte) ([]Listener, error) {
	out := []Listener{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "Netid") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		proto := strings.ToLower(f[0])
		if proto != "tcp" && proto != "udp" {
			continue
		}
		// UDP has no LISTEN state — it shows UNCONN — so state is not filtered
		// on; -l already restricted the output to listening sockets.
		local := f[4]

		addr, port, ok := splitHostPort(local)
		if !ok {
			continue
		}

		l := Listener{
			Protocol: proto,
			Address:  addr,
			Port:     port,
			IPv6:     strings.Contains(addr, ":") || strings.HasPrefix(local, "["),
		}
		// "0.0.0.0" and "*" and "[::]" all mean every interface.
		switch addr {
		case "0.0.0.0", "*", "::", "[::]":
			l.Exposed = true
		}
		l.Process, l.PID = parseSSProcess(strings.Join(f[5:], " "))
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("adapter: read ss output: %w", err)
	}

	// Exposed sockets first, then by port: what is reachable from outside is
	// what a person opening this view is looking for.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Exposed != out[j].Exposed {
			return out[i].Exposed
		}
		pi, _ := strconv.Atoi(out[i].Port)
		pj, _ := strconv.Atoi(out[j].Port)
		return pi < pj
	})
	return out, nil
}

// splitHostPort splits ss's "addr:port", where addr may be a bracketed IPv6
// literal, a bare IPv6 literal, or "*".
func splitHostPort(s string) (host, port string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	host = strings.Trim(s[:i], "[]")
	port = s[i+1:]
	if host == "" {
		host = "*"
	}
	return host, port, port != ""
}

// parseSSProcess reads `users:(("sshd",pid=34,fd=3))`.
//
// Absent when the socket belongs to another user and ss was run without
// privileges — that is normal, not a failure, so the row is kept without it.
func parseSSProcess(s string) (name string, pid int) {
	i := strings.Index(s, `users:((`)
	if i < 0 {
		return "", 0
	}
	rest := s[i+len(`users:((`):]

	if q := strings.Index(rest, `"`); q >= 0 {
		if end := strings.Index(rest[q+1:], `"`); end >= 0 {
			name = rest[q+1 : q+1+end]
		}
	}
	if p := strings.Index(rest, "pid="); p >= 0 {
		numStr := rest[p+4:]
		end := strings.IndexAny(numStr, ",)")
		if end > 0 {
			pid, _ = strconv.Atoi(numStr[:end])
		}
	}
	return name, pid
}
