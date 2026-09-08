package adapter

import (
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
echo '#end'
:`

// SplitSecurityOutput cuts the script's output into its three sections.
func SplitSecurityOutput(out string) (units, ufwConf, jails string) {
	section := ""
	var u, f, j strings.Builder
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case "#units", "#ufw", "#jails", "#end":
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
		}
	}
	return u.String(), f.String(), j.String()
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
