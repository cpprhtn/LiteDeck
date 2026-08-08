package adapter

// Windows interfaces and listening sockets, mapped onto the shapes the network
// view already renders.
//
// Two sources per half, and the direction matters. The interface list is built
// from Get-NetIPAddress and enriched from Get-NetAdapter, not the other way round:
// the loopback pseudo-interface has addresses but is not an adapter, so starting
// from the adapter table drops the one interface that is always present.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/adapter/windowspowershell"
)

// WindowsNetworkScript collects everything the tab needs in one round trip.
//
// UDP as well as TCP, because `ss -tulnp` on the Linux side lists both and a UDP
// service bound to 0.0.0.0 is exposed in exactly the same sense.
func WindowsNetworkScript() string {
	return strings.Join([]string{
		`Write-Output '#addr'`,
		windowspowershell.JSON(`Get-NetIPAddress | Select-Object `+
			`InterfaceAlias,IPAddress,PrefixLength,AddressFamily,InterfaceIndex`, 2),
		`Write-Output '#adapter'`,
		windowspowershell.JSON(`Get-NetAdapter | Select-Object `+
			`InterfaceAlias,InterfaceIndex,MacAddress,Status,MtuSize,InterfaceType`, 2),
		`Write-Output '#tcp'`,
		windowspowershell.JSON(`Get-NetTCPConnection -State Listen | `+
			`Select-Object LocalAddress,LocalPort,OwningProcess`, 2),
		`Write-Output '#udp'`,
		windowspowershell.JSON(`Get-NetUDPEndpoint | `+
			`Select-Object LocalAddress,LocalPort,OwningProcess`, 2),
		`Write-Output '#proc'`,
		windowspowershell.JSON(`Get-Process | Select-Object Id,ProcessName`, 2),
	}, "; ")
}

type winAddrRow struct {
	InterfaceAlias string `json:"InterfaceAlias"`
	IPAddress      string `json:"IPAddress"`
	PrefixLength   int    `json:"PrefixLength"`
	// AddressFamily is an enum integer, not a string: 2 is IPv4 and 23 is IPv6.
	AddressFamily  int `json:"AddressFamily"`
	InterfaceIndex int `json:"InterfaceIndex"`
}

type winAdapterRow struct {
	InterfaceAlias string `json:"InterfaceAlias"`
	InterfaceIndex int    `json:"InterfaceIndex"`
	MacAddress     string `json:"MacAddress"`
	Status         string `json:"Status"` // Up, Disconnected, Disabled…
	MtuSize        int    `json:"MtuSize"`
	// InterfaceType is the IANA ifType: 6 is Ethernet, 24 software loopback,
	// 71 wireless.
	InterfaceType int `json:"InterfaceType"`
}

type winEndpointRow struct {
	LocalAddress  string `json:"LocalAddress"`
	LocalPort     int    `json:"LocalPort"`
	OwningProcess int    `json:"OwningProcess"`
}

type winProcNameRow struct {
	ID          int    `json:"Id"`
	ProcessName string `json:"ProcessName"`
}

const (
	winAFInet  = 2
	winAFInet6 = 23
	// ifTypeSoftwareLoopback is the IANA value. Matching on the alias text
	// instead would work only on an English install.
	ifTypeSoftwareLoopback = 24
)

// ParseWindowsNetwork reads the output of WindowsNetworkScript.
func ParseWindowsNetwork(data []byte) ([]Interface, []Listener, []string, error) {
	blocks := splitTaggedBlocks(string(data))
	warnings := []string{}

	addrs, err := decodeJSONArray[winAddrRow]([]byte(blocks["addr"]))
	if err != nil {
		return nil, nil, warnings, fmt.Errorf("adapter: parse Get-NetIPAddress: %w", err)
	}
	adapters, err := decodeJSONArray[winAdapterRow]([]byte(blocks["adapter"]))
	if err != nil {
		// Get-NetAdapter is the enrichment, not the source. Without it the
		// addresses still render; only MAC, MTU and link state go missing.
		warnings = append(warnings, "어댑터 정보를 읽지 못했습니다 — MAC·MTU·링크 상태가 비어 있습니다")
		adapters = nil
	}

	byIndex := make(map[int]winAdapterRow, len(adapters))
	for _, a := range adapters {
		byIndex[a.InterfaceIndex] = a
	}

	// Grouped by interface index rather than by name: an alias is localised and
	// two adapters can share one, while the index is what the address rows and
	// the adapter rows actually join on.
	type group struct {
		name  string
		index int
		addrs []Address
	}
	groups := map[int]*group{}
	var order []int
	for _, a := range addrs {
		g, ok := groups[a.InterfaceIndex]
		if !ok {
			g = &group{name: a.InterfaceAlias, index: a.InterfaceIndex}
			groups[a.InterfaceIndex] = g
			order = append(order, a.InterfaceIndex)
		}
		g.addrs = append(g.addrs, Address{
			Address: a.IPAddress,
			Prefix:  a.PrefixLength,
			Family:  winAddressFamily(a.AddressFamily),
		})
	}

	ifaces := make([]Interface, 0, len(groups))
	for _, idx := range order {
		g := groups[idx]
		a, hasAdapter := byIndex[idx]
		iface := Interface{
			Name:      g.name,
			Addresses: g.addrs,
			// No adapter row means a pseudo-interface — loopback, or a tunnel the
			// adapter table does not list. Its addresses are real either way.
			Loopback: !hasAdapter && isWindowsLoopbackAddrs(g.addrs),
			State:    "UNKNOWN",
		}
		if hasAdapter {
			iface.MAC = strings.ToLower(strings.ReplaceAll(a.MacAddress, "-", ":"))
			iface.MTU = a.MtuSize
			iface.State = winLinkState(a.Status)
			iface.Loopback = a.InterfaceType == ifTypeSoftwareLoopback
		}
		ifaces = append(ifaces, iface)
	}

	procNames := map[int]string{}
	if rows, err := decodeJSONArray[winProcNameRow]([]byte(blocks["proc"])); err == nil {
		for _, p := range rows {
			procNames[p.ID] = p.ProcessName
		}
	}

	listeners := []Listener{}
	for _, b := range []struct{ tag, proto string }{{"tcp", "tcp"}, {"udp", "udp"}} {
		rows, err := decodeJSONArray[winEndpointRow]([]byte(blocks[b.tag]))
		if err != nil {
			warnings = append(warnings,
				strings.ToUpper(b.proto)+" 소켓 목록을 읽지 못했습니다")
			continue
		}
		for _, r := range rows {
			ipv6 := strings.Contains(r.LocalAddress, ":")
			listeners = append(listeners, Listener{
				Protocol: b.proto,
				Address:  r.LocalAddress,
				Port:     strconv.Itoa(r.LocalPort),
				PID:      r.OwningProcess,
				Process:  procNames[r.OwningProcess],
				IPv6:     ipv6,
				Exposed:  winExposed(r.LocalAddress),
			})
		}
	}

	// Sorted by port so the table is stable between refreshes; Get-NetTCPConnection
	// returns them in an order that changes.
	sort.Slice(listeners, func(i, j int) bool {
		if listeners[i].Port != listeners[j].Port {
			pi, _ := strconv.Atoi(listeners[i].Port)
			pj, _ := strconv.Atoi(listeners[j].Port)
			return pi < pj
		}
		return listeners[i].Protocol < listeners[j].Protocol
	})

	return ifaces, listeners, warnings, nil
}

func winAddressFamily(af int) string {
	switch af {
	case winAFInet:
		return "inet"
	case winAFInet6:
		return "inet6"
	default:
		return ""
	}
}

// winLinkState maps the adapter status to the ip(8) vocabulary the view uses.
func winLinkState(status string) string {
	switch strings.ToLower(status) {
	case "up":
		return "UP"
	case "disconnected", "disabled", "not present":
		return "DOWN"
	default:
		return "UNKNOWN"
	}
}

// isWindowsLoopbackAddrs decides loopback for an interface with no adapter row.
func isWindowsLoopbackAddrs(addrs []Address) bool {
	for _, a := range addrs {
		if a.Address == "127.0.0.1" || a.Address == "::1" {
			return true
		}
	}
	return false
}

// winExposed marks a socket reachable from another machine.
//
// The same test as the Linux side, and the reason the tab exists: a service on
// 0.0.0.0 or [::] answers anyone who can route to the box, where one on loopback
// answers only the box itself.
func winExposed(addr string) bool {
	switch addr {
	case "0.0.0.0", "::", "*":
		return true
	}
	return false
}
