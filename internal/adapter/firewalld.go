package adapter

import (
	"strings"
)

// firewalld, which is the firewall on the RHEL side of the world (§7.4).
//
// # Why this exists next to the ufw reader
//
// The rules panel was blank on Rocky Linux 9 and CentOS Stream 9. The tab was
// not lying — it named firewalld and said it was on, because the unit is right
// there in the service list — but the panel that says *what is allowed in* read
// `ufw status verbose`, and there is no ufw on those machines. So the one
// question the tab exists to answer went unanswered on the most common
// enterprise Linux.
//
// ufw is still read first and this never runs on a host that has it. That is
// deliberate: Debian and Ubuntu work today and nothing here is allowed to
// change what they show.
//
// # What firewalld says, and what it does not
//
// `firewall-cmd --list-all` describes the *active zone* — normally "public" —
// as a list of services and ports:
//
//	public
//	  target: default
//	  services: cockpit dhcpv6-client http https ssh
//	  ports: 8443/tcp 9090-9095/udp
//	  rich rules:
//	  	rule family="ipv4" source address="203.0.113.0/24" reject
//
// A service is a name, not a port. Resolving "ssh" to 22/tcp would mean a
// round trip per service into /usr/lib/firewalld/services, and the name is what
// the administrator typed and what they will recognise. So a service row is
// shown by its name and a port row by its port, and neither is dressed up as
// the other.
//
// `target: default` is firewalld's way of saying "reject what no rule allows",
// which is the same posture ufw calls `deny (incoming)`.

// ParseFirewalld reads `firewall-cmd --list-all`.
//
// stderr is dropped by the caller's script, which is what makes this safe to
// run everywhere: a host with no firewall-cmd contributes nothing, and so does
// one where the service is stopped — firewalld writes both "not running" and
// "FirewallD is not running" to stderr, never to stdout. So anything arriving
// here came from a firewalld that answered.
//
// Returns ok=false for anything that is not a zone. The caller then shows
// nothing rather than an empty table, because an empty table reads as "nothing
// is allowed in" — which on a Debian host, where this section is always empty,
// would be a new and false claim on a screen that works today.
func ParseFirewalld(zone string) (FirewallStatus, bool) {
	s := FirewallStatus{Rules: []FirewallRule{}}
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return s, false
	}

	// firewalld filters inbound and leaves outbound alone. "allow" for outgoing
	// is not a guess: a zone has no egress policy at all.
	s.Outgoing = "allow"

	sawTarget := false
	for _, line := range strings.Split(zone, "\n") {
		line = strings.TrimRight(line, "\r")
		key, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value := strings.TrimSpace(rest)
		switch key {
		case "target":
			sawTarget = true
			// "default" and "%%REJECT%%" both refuse; ACCEPT is a zone that
			// lets everything through, which is worth saying plainly.
			if value == "ACCEPT" {
				s.Incoming = "allow"
			} else {
				s.Incoming = "deny"
			}
		case "forward":
			if value == "yes" {
				s.Routed = "allow"
			} else {
				s.Routed = "deny"
			}
		case "services":
			for _, name := range strings.Fields(value) {
				s.Rules = append(s.Rules, FirewallRule{
					To:     name,
					Action: "ALLOW IN",
					From:   "Anywhere",
					V4:     true,
					V6:     true,
				})
			}
		case "ports":
			for _, p := range strings.Fields(value) {
				port, _, _ := strings.Cut(p, "/")
				s.Rules = append(s.Rules, FirewallRule{
					To:     p,
					Action: "ALLOW IN",
					From:   "Anywhere",
					Ports:  firewalldPorts(port),
					V4:     true,
					V6:     true,
				})
			}
		}
	}
	// A zone always prints a target. Requiring it means a stray line that
	// happens to contain a colon cannot be mistaken for a firewall.
	if !sawTarget {
		return FirewallStatus{Rules: []FirewallRule{}}, false
	}
	// It answered, so it is running.
	s.Active = true
	return s, true
}

func firewalldPorts(field string) []string {
	lo, hi, isRange := strings.Cut(field, "-")
	if !isRange {
		return []string{field}
	}
	return []string{lo, hi}
}
