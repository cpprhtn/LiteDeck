package adapter

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
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

// ParseJailDeclared reads the settings a config file asks for, merging the
// DEFAULT block with the named jail's own — which is how fail2ban reads it.
//
// Only what the file says. What the daemon is running comes from
// `fail2ban-client get`, and the gap between the two is the point (JailStatus).
func ParseJailDeclared(text, jail string) map[string]string {
	out := map[string]string{}
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
		// DEFAULT first, then the jail's own block overriding it — the order
		// the file is read in, so a later value wins by being written later.
		if !strings.EqualFold(section, "DEFAULT") && !strings.EqualFold(section, jail) {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return out
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

// JailStatus is a fail2ban jail as the daemon is actually running it.
//
// # Why the file is not enough
//
// `apt install fail2ban` starts the service, so a later `systemctl enable --now`
// changes nothing that is already running. The daemon goes on with whatever it
// read at install time, and the file somebody edited afterwards describes an
// intention rather than a state. Measured on a real server: jail.local said
// twenty retries and the jail was doing five.
//
// A screen that reads only the file reports that intention as fact, and reports
// it forever — there is no later moment at which it notices. This is the same
// shape as the unit-versus-ufw.conf disagreement, one layer up.
type JailStatus struct {
	// Effective settings, as `fail2ban-client get` reports them. Seconds for
	// the two durations, which is the only unit that command uses.
	MaxRetry int `json:"maxRetry"`
	FindTime int `json:"findTime"`
	BanTime  int `json:"banTime"`

	CurrentlyFailed int `json:"currentlyFailed"`
	TotalFailed     int `json:"totalFailed"`
	CurrentlyBanned int `json:"currentlyBanned"`
	TotalBanned     int `json:"totalBanned"`
	// Banned is who is in the jail right now.
	Banned []string `json:"banned,omitempty"`
}

// Repeats is bans divided by the *distinct addresses ever banned*.
//
// Not the number banned right now — that divisor turned 287 bans into a repeat
// rate of 143 on a server whose real figure was about four. The count it wants
// comes from the ban log, and until a caller has that this is better left
// uncalled than called with whatever is to hand.
//
// A high number means the ban expires before whoever it landed on gives up, so
// they come back and are banned again — which is a `bantime` that is too short,
// said in a way the screen can act on. Measured across three servers it ran at
// about 4.6, and raising bantime from one minute to thirty was the answer.
func (j JailStatus) Repeats(uniqueAddresses int) float64 {
	if uniqueAddresses <= 0 {
		return 0
	}
	return float64(j.TotalBanned) / float64(uniqueAddresses)
}

// ParseJailStatus reads the marked output of the elevated fail2ban reads.
func ParseJailStatus(out string) JailStatus {
	var j JailStatus
	section := ""
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			section = t
			continue
		}
		if t == "" {
			continue
		}
		switch section {
		case "#get sshd maxretry":
			j.MaxRetry = jailInt(t)
		case "#get sshd findtime":
			j.FindTime = jailInt(t)
		case "#get sshd bantime":
			j.BanTime = jailInt(t)
		case "#status sshd":
			parseJailStatusLine(t, &j)
		}
	}
	return j
}

// parseJailStatusLine reads one row of `fail2ban-client status`, which draws a
// tree with box characters and separates label from value with a tab.
func parseJailStatusLine(line string, j *JailStatus) {
	body := strings.TrimLeft(line, "|`- \t")
	label, value, ok := strings.Cut(body, ":")
	if !ok {
		return
	}
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	switch label {
	case "Currently failed":
		j.CurrentlyFailed = jailInt(value)
	case "Total failed":
		j.TotalFailed = jailInt(value)
	case "Currently banned":
		j.CurrentlyBanned = jailInt(value)
	case "Total banned":
		j.TotalBanned = jailInt(value)
	case "Banned IP list":
		j.Banned = strings.Fields(value)
	}
}

// jailInt reads a number, treating anything unreadable as zero — which callers
// take as "do not compare" rather than as the value nought.
func jailInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// JailMismatch is one setting the file and the daemon disagree about.
type JailMismatch struct {
	Key      string `json:"key"`
	Declared string `json:"declared"`
	Running  string `json:"running"`
}

// JailMismatches compares what the file asked for against what is running.
//
// Values are compared after being reduced to the same unit, because `30m` and
// `1800` are the same instruction — reporting those as a disagreement would
// teach people to ignore this line, which is the one line here worth reading.
func JailMismatches(declared map[string]string, running JailStatus) []JailMismatch {
	var out []JailMismatch
	for _, c := range []struct {
		key     string
		running int
		seconds bool
	}{
		{"maxretry", running.MaxRetry, false},
		{"findtime", running.FindTime, true},
		{"bantime", running.BanTime, true},
	} {
		want, ok := declared[c.key]
		if !ok || strings.TrimSpace(want) == "" {
			// Not declared: the daemon's default applies and there is nothing
			// to disagree with.
			continue
		}
		var wantN int
		if c.seconds {
			wantN = ParseFail2banDuration(want)
		} else {
			wantN = jailInt(want)
		}
		if wantN == 0 || wantN == c.running {
			continue
		}
		out = append(out, JailMismatch{
			Key: c.key, Declared: strings.TrimSpace(want), Running: strconv.Itoa(c.running),
		})
	}
	return out
}

// ParseFail2banDuration reads fail2ban's time syntax into seconds.
//
// It accepts bare numbers as seconds and a suffix otherwise. Returns 0 for
// anything it cannot read, which callers treat as "do not compare" rather than
// as zero seconds — guessing would report a disagreement that is really a
// parser gap.
func ParseFail2banDuration(v string) int {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return 0
	}
	unit := map[byte]int{'s': 1, 'm': 60, 'h': 3600, 'd': 86400, 'w': 604800, 'y': 31536000}
	last := v[len(v)-1]
	mult, suffixed := unit[last]
	if !suffixed {
		return jailInt(v)
	}
	n := jailInt(v[:len(v)-1])
	if n == 0 {
		return 0
	}
	return n * mult
}

// Reading `nft list ruleset` for what is being blocked and whether it works.
//
// Two things are wanted out of it and neither is the ruleset itself. Who is
// blocked — the sets — and whether the blocking is doing anything — the drop
// counters. The rest is syntax.
//
// Deliberately not a full nft parser. The grammar is large and nobody here
// needs it: a set with elements and a rule with a counter are the two shapes
// that answer the question, and anything more elaborate is shown as text.

// NftSet is one set a ruleset is blocking with.
type NftSet struct {
	Table string `json:"table"`
	Name  string `json:"name"`
	// Fail2ban marks a table fail2ban wrote. Its entries come and go on their
	// own as bans expire; a hand-made table stays until somebody removes it.
	// Mixing the two loses the only question worth asking about the list —
	// which of these did I put there.
	Fail2ban bool     `json:"fail2ban"`
	Elements []string `json:"elements"`
	// Ranges counts the entries that are a network rather than one address. A
	// /24 and a single host are one line each and very different decisions.
	Ranges int `json:"ranges"`
}

// NftCounter is a rule that counts what it acted on.
//
// The most useful number on the security screen. Everything else says a thing
// is configured; this says it is doing something, and to how many packets.
type NftCounter struct {
	Table    string `json:"table"`
	Chain    string `json:"chain"`
	Fail2ban bool   `json:"fail2ban"`
	Packets  int64  `json:"packets"`
	Bytes    int64  `json:"bytes"`
	// Verdict is what the rule does — drop, reject, accept.
	Verdict string `json:"verdict"`
}

// fail2banTable reports whether a table name is one fail2ban made.
func fail2banTable(name string) bool { return strings.HasPrefix(name, "f2b-") }

// ParseNftSets pulls the sets and their elements out of a ruleset.
//
// Elements wrap across lines inside `{ ... }` and are comma separated, so the
// body is gathered first and split afterwards — reading line by line would cut
// an address list in half wherever nft chose to wrap it.
func ParseNftSets(out string) []NftSet {
	var sets []NftSet
	table, setName := "", ""
	inElements := false
	var body strings.Builder

	finish := func() {
		if setName == "" {
			return
		}
		s := NftSet{Table: table, Name: setName, Fail2ban: fail2banTable(table)}
		for _, e := range strings.Split(body.String(), ",") {
			e = strings.Trim(strings.TrimSpace(e), "{} ")
			if e == "" {
				continue
			}
			s.Elements = append(s.Elements, e)
			if strings.Contains(e, "/") {
				s.Ranges++
			}
		}
		if len(s.Elements) > 0 {
			sets = append(sets, s)
		}
		setName, inElements = "", false
		body.Reset()
	}

	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "table "):
			finish()
			f := strings.Fields(t)
			// `table inet blackhole {` — the name is the last word before the brace.
			if len(f) >= 3 {
				table = strings.TrimSuffix(f[2], "{")
				table = strings.TrimSpace(table)
			}
		case strings.HasPrefix(t, "set "):
			finish()
			setName = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "set "), "{"))
		case strings.HasPrefix(t, "elements = "):
			inElements = true
			body.WriteString(strings.TrimPrefix(t, "elements = "))
			if strings.Contains(t, "}") {
				finish()
			}
		case inElements:
			body.WriteString(" " + t)
			if strings.Contains(t, "}") {
				finish()
			}
		}
	}
	finish()
	return sets
}

// ParseNftCounters pulls out the rules that carry a counter.
func ParseNftCounters(out string) []NftCounter {
	var counters []NftCounter
	table, chain := "", ""

	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "table "):
			if f := strings.Fields(t); len(f) >= 3 {
				table = strings.TrimSuffix(f[2], "{")
			}
			chain = ""
		case strings.HasPrefix(t, "chain "):
			chain = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "chain "), "{"))
		case strings.Contains(t, "counter packets "):
			c := NftCounter{Table: table, Chain: chain, Fail2ban: fail2banTable(table)}
			f := strings.Fields(t)
			for i, w := range f {
				switch w {
				case "packets":
					if i+1 < len(f) {
						c.Packets = int64(jailInt(f[i+1]))
					}
				case "bytes":
					if i+1 < len(f) {
						c.Bytes = int64(jailInt(f[i+1]))
					}
				}
			}
			// The verdict is the last word, which is what the rule does with
			// what it counted.
			if len(f) > 0 {
				c.Verdict = strings.TrimSuffix(f[len(f)-1], ";")
			}
			counters = append(counters, c)
		}
	}
	return counters
}

// AttackersScript counts failed passwords per address over a short window.
//
// Short on purpose. A wide window includes the traffic from *before* a block
// went on, so an address that has been silent for an hour still tops the list
// and reads as "not handled yet" — which happened twice while this was being
// worked out. Fifteen minutes is long enough to have numbers and short enough
// that what it shows is still happening.
//
// journald filters by identifier so awk only sees sshd, and the counting is
// done on the server: the point is a dozen rows, not twenty thousand lines.
const AttackersScript = `journalctl -t sshd -t sshd-session --since '-15 min' --no-pager -q -o cat 2>/dev/null | awk '
/Failed password|Invalid user/ {
  for (i = 1; i <= NF; i++) if ($i == "from") { print $(i+1); break }
}' | sort | uniq -c | sort -rn | head -40
:`

// ParseAttackerCounts reads `uniq -c` output into address → count.
func ParseAttackerCounts(out string) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		n := jailInt(f[0])
		if n == 0 || net.ParseIP(f[1]) == nil {
			continue
		}
		counts[f[1]] = n
	}
	return counts
}

// Attacker is one address and how many failures came from it.
type Attacker struct {
	Address string `json:"address"`
	Count   int    `json:"count"`
}

// TopAttackers ranks the addresses that are still getting through.
//
// Addresses already blocked are removed, including the ones inside a blocked
// network. Leaving them in gives a list nobody can act on, and it misleads
// twice: a banned address goes on appearing in the log for as long as the
// window reaches back past the ban. One measured example topped an hour's
// window with 931 lines whose newest was fifty minutes old — blocked and quiet
// the whole time since.
func TopAttackers(counts map[string]int, blocked []string, limit int) []Attacker {
	nets, hosts := parseBlocked(blocked)

	var out []Attacker
	for addr, n := range counts {
		if hosts[addr] || inAnyNet(addr, nets) {
			continue
		}
		out = append(out, Attacker{Address: addr, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		// Ties by address, so the list does not reshuffle between reads.
		return out[i].Address < out[j].Address
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func parseBlocked(blocked []string) ([]*net.IPNet, map[string]bool) {
	var nets []*net.IPNet
	hosts := map[string]bool{}
	for _, b := range blocked {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(b); err == nil {
			nets = append(nets, n)
			continue
		}
		hosts[b] = true
	}
	return nets, hosts
}

func inAnyNet(addr string, nets []*net.IPNet) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// SubnetCluster is several addresses from one /24.
type SubnetCluster struct {
	CIDR  string `json:"cidr"`
	Hosts int    `json:"hosts"`
	Count int    `json:"count"`
}

// SubnetClusters groups attackers by /24 and keeps the crowded ones.
//
// A pattern that only exists when they are put together: one address from a
// range is noise, and seven of them is somebody working through it. Measured at
// seven hosts from one network on one server and eight on another, each
// invisible while the list was read one address at a time.
func SubnetClusters(attackers []Attacker, min int) []SubnetCluster {
	type acc struct{ hosts, count int }
	by := map[string]*acc{}
	for _, a := range attackers {
		ip := net.ParseIP(a.Address).To4()
		if ip == nil {
			// v6 has no /24 worth speaking of, and grouping it by the same rule
			// would say nothing true.
			continue
		}
		cidr := fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
		e := by[cidr]
		if e == nil {
			e = &acc{}
			by[cidr] = e
		}
		e.hosts++
		e.count += a.Count
	}
	var out []SubnetCluster
	for cidr, e := range by {
		if e.hosts < min {
			continue
		}
		out = append(out, SubnetCluster{CIDR: cidr, Hosts: e.hosts, Count: e.count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// Ban is one address fail2ban put away, and when.
type Ban struct {
	At      time.Time `json:"at"`
	Address string    `json:"address"`
}

// BanHistory is the recent bans and what they add up to.
type BanHistory struct {
	// Bans is newest first — the screen reads downward from now.
	Bans []Ban `json:"bans"`
	// Unique is how many distinct addresses those bans landed on. It is the
	// number `fail2ban-client status` does not report and the only honest
	// denominator for a repeat rate.
	Unique int `json:"unique"`
}

// Repeats is bans divided by the addresses they landed on.
//
// A high number means the ban lifts before whoever it landed on gives up, so
// they come back and are banned again — a `bantime` that is too short, said in
// a way the screen can act on. Measured across three servers at about 4.6, and
// raising bantime from one minute to thirty was the answer.
//
// This is the calculation that was got wrong once: dividing by the addresses
// banned *right now* turned ten bans on five addresses into a rate of 143.
func (h BanHistory) Repeats() float64 {
	if h.Unique == 0 {
		return 0
	}
	return float64(len(h.Bans)) / float64(h.Unique)
}

// ParseBanLog reads fail2ban's log for its Ban lines.
//
// Unban lines are left out on purpose: this counts what happened, and a ban
// that has since lifted still happened. Counting both would make the busiest
// server look like the quietest.
func ParseBanLog(text string) BanHistory {
	var h BanHistory
	seen := map[string]bool{}

	for _, line := range strings.Split(text, "\n") {
		// "... NOTICE  [sshd] Ban 1.2.3.4". The bracketed jail sits between the
		// timestamp and the verb, so the verb is found rather than positioned.
		idx := strings.Index(line, "] Ban ")
		if idx < 0 {
			continue
		}
		addr := strings.TrimSpace(line[idx+len("] Ban "):])
		if net.ParseIP(addr) == nil {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// fail2ban writes milliseconds after a comma, which Go's layout has no
		// verb for. Cutting it off loses nothing a ban list needs.
		stamp := f[0] + " " + f[1]
		if i := strings.IndexByte(stamp, ','); i >= 0 {
			stamp = stamp[:i]
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.Local)
		if err != nil {
			continue
		}
		h.Bans = append(h.Bans, Ban{At: at, Address: addr})
		if !seen[addr] {
			seen[addr] = true
			h.Unique++
		}
	}
	// Newest first.
	sort.SliceStable(h.Bans, func(i, j int) bool { return h.Bans[i].At.After(h.Bans[j].At) })
	return h
}

// FailureBucket is how many failed logins fell in one hour.
type FailureBucket struct {
	At    time.Time `json:"at"`
	Count int       `json:"count"`
}

// FailuresScript counts failed logins per hour over a day.
//
// Bucketed on the server. The chart wants twenty-four numbers and the journal
// holds tens of thousands of lines — on the box this was measured against, over
// three thousand failures a day. Sending them all to count them here would be
// sending the haystack to report the number of straws.
const FailuresScript = `journalctl -t sshd -t sshd-session --since '-24 hours' --no-pager -q -o short-iso 2>/dev/null | awk '
/Failed password|Invalid user/ {
  split($1, p, "T")
  split(p[2], h, ":")
  key = p[1] " " h[1]
  n[key]++
}
END { for (k in n) print k, n[k] }' | sort
:`

// ParseFailureBuckets reads "2026-09-08 20 931" lines.
func ParseFailureBuckets(out string) []FailureBucket {
	var buckets []FailureBucket
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02 15", f[0]+" "+f[1], time.Local)
		if err != nil {
			continue
		}
		n := jailInt(f[2])
		if n == 0 {
			continue
		}
		buckets = append(buckets, FailureBucket{At: at, Count: n})
	}
	// Oldest first: a chart is read left to right.
	sort.SliceStable(buckets, func(i, j int) bool { return buckets[i].At.Before(buckets[j].At) })
	return buckets
}
