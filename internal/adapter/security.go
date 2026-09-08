package adapter

import (
	"strconv"
	"strings"
)

// Reading what is guarding the server (T-35).
//
// # What this can see without root, and what it cannot
//
// Almost every firewall answer needs root: `ufw status`, `nft list ruleset`,
// `iptables -L` and `fail2ban-client status` all refuse a normal user. A screen
// built on those would ask for a password before showing anything, and the sshd
// config reader already learned where that leads — a view nobody opens.
//
// So the split is: **whether a thing is on** is free, **what its rules are** is
// not. The free half is the half that matters. "This box has no firewall" is
// the finding people need; the forty accept rules are detail.
//
// # Why the unit state is not the answer
//
// Measured on a real server, 2026-09-08:
//
//	ufw.service          enabled · active (exited)
//	/etc/ufw/ufw.conf    ENABLED=no
//
// The unit was up and the firewall was off. ufw's unit is a oneshot that loads
// rules at boot and exits; with `ENABLED=no` it starts, does nothing and
// finishes, which systemd reports as a healthy `active/exited`. A screen
// reading `systemctl is-active` alone would have put a green light on an
// unprotected machine facing the internet.
//
// That is why both are read, and why a disagreement between them is shown
// rather than resolved. The file wins — it is what `ufw enable` writes — but
// the point is to say so out loud rather than quietly pick one.

// SecurityUnits are the tools asked about, in the order the screen shows them.
//
// A fixed list on purpose. `systemctl show` takes them all in one invocation,
// so the cost is one round trip whatever the length — but a list built from
// something the user typed would be a name reaching a command, and this file
// stays on the safe side of that line (§3.2b).
var SecurityUnits = []string{
	"ufw.service",
	"nftables.service",
	"iptables.service",
	"firewalld.service",
	"fail2ban.service",
}

// SecurityScript reads everything the free half needs, in one round trip.
//
// A compile-time constant with nothing interpolated, which is what lets it go
// to `sh -c` without breaking the argv-only rule (§3.2b) — the unit names come
// from SecurityUnits above, not from a caller.
//
// The markers matter: a missing file produces no output at all, and without
// something to separate the sections an absent ufw.conf would look like an
// empty jail.local. `2>/dev/null` because "no such file" is an answer here, not
// an error, and `:` so a missing file does not make the whole read fail.
const SecurityScript = `echo '#units'
systemctl show --no-pager -p Id -p LoadState -p UnitFileState -p ActiveState -p SubState ` +
	"ufw.service nftables.service iptables.service firewalld.service fail2ban.service" + ` 2>/dev/null
echo '#ufw'
cat /etc/ufw/ufw.conf 2>/dev/null
echo '#jails'
cat /etc/fail2ban/jail.local 2>/dev/null
echo '#modules'
grep -E '^(nf_tables|ip_tables) ' /proc/modules 2>/dev/null
echo '#end'
:`

// SplitSecurityOutput cuts the script's output into its sections.
//
// The modules section is grepped on the server rather than read whole:
// /proc/modules is 14KB on an ordinary desktop kernel and two lines of it are
// wanted. Filtering there costs nothing — the command is already running — and
// the round trip stays one.
func SplitSecurityOutput(out string) (units, ufwConf, jails, modules string) {
	section := ""
	var u, f, j, m strings.Builder
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case "#units", "#ufw", "#jails", "#modules", "#end":
			section = strings.TrimSpace(line)
			continue
		}
		switch section {
		case "#units":
			u.WriteString(line + "\n")
		case "#ufw":
			f.WriteString(line + "\n")
		case "#jails":
			j.WriteString(line + "\n")
		case "#modules":
			m.WriteString(line + "\n")
		}
	}
	return u.String(), f.String(), j.String(), m.String()
}

// SecurityUnit is one tool's systemd state.
type SecurityUnit struct {
	Name string `json:"name"`
	// Installed is false where systemd has no such unit — a different answer
	// from "installed and switched off", and the one that means "there is no
	// switch here to find".
	Installed bool `json:"installed"`
	// Enabled is the boot setting, Active is right now.
	Enabled bool `json:"enabled"`
	Active  bool `json:"active"`
	// SubState is systemd's own word: "running", "exited", "dead". Kept because
	// a firewall that is `active (exited)` is normal and looks alarming.
	SubState string `json:"subState,omitempty"`
}

// ParseSecurityUnits reads `systemctl show` run over several units at once.
//
// One invocation for all of them rather than one `is-active` each: the tools
// asked about are a fixed list, and six round trips to learn six booleans is
// six forked shells on a machine this app is meant to go easy on.
func ParseSecurityUnits(out string) []SecurityUnit {
	var units []SecurityUnit
	cur := map[string]string{}

	flush := func() {
		id := cur["Id"]
		if id == "" {
			return
		}
		load := cur["LoadState"]
		units = append(units, SecurityUnit{
			Name: id,
			// "not-found" is systemd's word for a unit that does not exist.
			// Anything else — loaded, masked, error — means something is there.
			Installed: load != "" && load != "not-found",
			Enabled:   cur["UnitFileState"] == "enabled",
			// ActiveState, not SubState. A oneshot that has done its work
			// reports active/exited, and that is a firewall doing its job.
			Active:   cur["ActiveState"] == "active",
			SubState: cur["SubState"],
		})
		cur = map[string]string{}
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// Id starts a new unit. systemd separates them with a blank line, but
		// not every version does it in every mode, so the key does it too.
		if key == "Id" && cur["Id"] != "" {
			flush()
		}
		cur[key] = value
	}
	flush()
	return units
}

// ParseUfwConf reads ENABLED out of /etc/ufw/ufw.conf.
//
// This is the file `ufw enable` and `ufw disable` write, so it is what the tool
// itself considers the answer. Returns whether it is on, and whether the line
// was there at all — a missing file and a disabled firewall are different
// things to say.
func ParseUfwConf(text string) (on bool, found bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// The shipped file carries the setting in its own comments as an
		// example. A commented line is documentation, not configuration.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "ENABLED" {
			continue
		}
		// Last one wins, the way the shell that sources this file would.
		on = strings.EqualFold(strings.TrimSpace(value), "yes")
		found = true
	}
	return on, found
}

// ParseFail2banJails names the jails a config file switches on.
//
// What the file declares, not what fail2ban is running — the effective set
// merges jail.conf, jail.local and jail.d, and only `fail2ban-client status`
// knows it. The screen says which of the two it is showing.
func ParseFail2banJails(text string) []string {
	var jails []string
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		// DEFAULT is the block every jail inherits from, not a jail. An
		// `enabled = true` there does not turn anything on by itself.
		if section == "" || strings.EqualFold(section, "DEFAULT") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "enabled") {
			continue
		}
		if isTruthy(strings.TrimSpace(value)) {
			jails = append(jails, section)
		}
	}
	return jails
}

// isTruthy accepts what fail2ban accepts. Its parser is Python's configparser,
// which takes more than "true".
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// FirewallRule is one line of `ufw status`, with the v4 and v6 copies folded
// into one.
//
// ufw prints every rule twice — once for each address family — so a list of
// seven rules arrives as fourteen lines. Showing them as fourteen makes the
// reader do the folding, and the fold is not interesting: nobody opens a port
// for v4 and means to leave it shut for v6.
type FirewallRule struct {
	To     string `json:"to"`
	Action string `json:"action"`
	From   string `json:"from"`
	// Ports are the numbers in To, split out. A rule can open several at once
	// (`80,443/tcp`), and the screen cross-references each against what is
	// actually listening.
	Ports []string `json:"ports,omitempty"`
	// Comment is ufw's application profile, where one named the rule.
	Comment string `json:"comment,omitempty"`
	V4      bool   `json:"v4"`
	V6      bool   `json:"v6"`
}

// FirewallStatus is what `ufw status verbose` says.
type FirewallStatus struct {
	Active bool `json:"active"`
	// Incoming is the default policy, which is the sentence that actually
	// decides whether this firewall does anything. A rule list under
	// `Default: allow (incoming)` is decoration.
	Incoming string         `json:"incoming,omitempty"`
	Outgoing string         `json:"outgoing,omitempty"`
	Routed   string         `json:"routed,omitempty"`
	Rules    []FirewallRule `json:"rules"`
}

// ParseUfwStatus reads `ufw status verbose`.
//
// Only ufw's format. nft and iptables print something else entirely and their
// output is shown as it came — a half-parsed ruleset is worse than a plain one,
// because it looks like it was understood.
func ParseUfwStatus(out string) FirewallStatus {
	s := FirewallStatus{Rules: []FirewallRule{}}
	// Folded by the rule's identity rather than its printed line: the v6 copy
	// carries "(v6)" in two of its three columns.
	seen := map[string]int{}
	inTable := false

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Status:"); ok {
			s.Active = strings.TrimSpace(rest) == "active"
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Default:"); ok {
			s.Incoming, s.Outgoing, s.Routed = parseUfwDefaults(rest)
			continue
		}
		if strings.HasPrefix(trimmed, "To ") || strings.HasPrefix(trimmed, "--") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		r, ok := parseUfwRule(trimmed)
		if !ok {
			continue
		}
		key := r.To + "|" + r.Action + "|" + r.From
		if at, dup := seen[key]; dup {
			s.Rules[at].V4 = s.Rules[at].V4 || r.V4
			s.Rules[at].V6 = s.Rules[at].V6 || r.V6
			continue
		}
		seen[key] = len(s.Rules)
		s.Rules = append(s.Rules, r)
	}
	return s
}

// parseUfwDefaults reads "deny (incoming), allow (outgoing), deny (routed)".
func parseUfwDefaults(rest string) (in, out, routed string) {
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		verb, where, ok := strings.Cut(part, " ")
		if !ok {
			continue
		}
		switch strings.Trim(where, "()") {
		case "incoming":
			in = verb
		case "outgoing":
			out = verb
		case "routed":
			routed = verb
		}
	}
	return in, out, routed
}

// parseUfwRule reads one row of the table.
//
// Columns are separated by runs of spaces, and every one of them can contain a
// single space of its own — "ALLOW IN", "Anywhere (v6)", "80,443/tcp (Nginx
// Full)". Splitting on whitespace would cut all three in the wrong place, so
// the split is on two-or-more spaces.
func parseUfwRule(line string) (FirewallRule, bool) {
	cols := splitColumns(line)
	if len(cols) < 2 {
		return FirewallRule{}, false
	}
	to, action := cols[0], cols[1]
	from := ""
	if len(cols) > 2 {
		from = cols[2]
	}
	if !strings.Contains(action, "ALLOW") && !strings.Contains(action, "DENY") &&
		!strings.Contains(action, "REJECT") && !strings.Contains(action, "LIMIT") {
		return FirewallRule{}, false
	}

	r := FirewallRule{Action: action}
	// "(v6)" marks the family and is not part of the rule's identity.
	v6 := strings.Contains(to, "(v6)") || strings.Contains(from, "(v6)")
	to = strings.TrimSpace(strings.ReplaceAll(to, "(v6)", ""))
	r.From = strings.TrimSpace(strings.ReplaceAll(from, "(v6)", ""))
	r.V6, r.V4 = v6, !v6

	// What is left of a parenthesis is ufw's application profile name.
	if open := strings.Index(to, "("); open >= 0 {
		r.Comment = strings.TrimSpace(strings.Trim(to[open:], "() "))
		to = strings.TrimSpace(to[:open])
	}
	r.To = to
	spec, _, _ := strings.Cut(to, "/")
	for _, p := range strings.Split(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			r.Ports = append(r.Ports, p)
		}
	}
	return r, true
}

// splitColumns cuts on two or more spaces, which is how ufw lines up a table
// whose cells contain single spaces.
func splitColumns(line string) []string {
	var cols []string
	for _, part := range strings.Split(line, "  ") {
		if part = strings.TrimSpace(part); part != "" {
			cols = append(cols, part)
		}
	}
	return cols
}

// KernelFirewall is what the kernel's packet filter is actually doing, read
// from /proc/modules.
//
// # Why the unit state is the wrong question
//
// nftables.service is `Type=oneshot`: it loads /etc/nftables.conf at boot and
// exits, so a healthy machine reports it `dead`. Ubuntu ships it *disabled*
// altogether because ufw is the front end. Reading that as "no firewall" lights
// a red lamp on nearly every Ubuntu server, and a panel that cries wolf on a
// healthy machine is worse than no panel — after the third time nobody reads
// the colour.
//
// The rules live in the kernel, not in a unit. A loaded module with references
// against it is the closest thing to "there are rules" that can be had without
// root, and it is what says ufw is running on nftables rather than beside it:
// on the server this was measured, `nft_compat` carried 133 references, which
// is ufw's iptables-nft shim at work.
//
// # What it does not prove
//
// That something is using netfilter, not that something is *protecting* this
// machine. Docker alone puts hundreds of references on nf_tables for its NAT
// rules and forwards nothing away from the host. So this raises "in use", which
// is a reason to go and count rules — never a verdict on its own.
type KernelFirewall struct {
	NFTables     bool `json:"nftables"`
	NFTablesRefs int  `json:"nftablesRefs"`
	IPTables     bool `json:"iptables"`
	IPTablesRefs int  `json:"iptablesRefs"`
}

// InUse reports that something has taken a reference on a packet filter.
func (k KernelFirewall) InUse() bool { return k.NFTablesRefs > 0 || k.IPTablesRefs > 0 }

// ParseFirewallModules reads the netfilter lines out of /proc/modules.
//
// The format is `name size refcount deps state offset`. Only the count matters:
// a module loaded with nothing referencing it came up with the kernel and is
// doing nothing, which is a different answer from being switched off.
func ParseFirewallModules(text string) KernelFirewall {
	var k KernelFirewall
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		refs, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		switch f[0] {
		case "nf_tables":
			k.NFTables, k.NFTablesRefs = true, refs
		case "ip_tables":
			k.IPTables, k.IPTablesRefs = true, refs
		}
	}
	return k
}
