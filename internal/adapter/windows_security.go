package adapter

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// The security screen on Windows (§4.7).
//
// # Why this is a different screen and not an adapter
//
// The Linux tab is shaped by what Linux has: ufw or firewalld in front,
// netfilter behind, fail2ban watching the journal and writing bans into a set,
// and a packet counter that says whether any of it is doing anything. Windows
// has one of those five. There is a firewall; there is no fail2ban, no ban list,
// no jail, and no drop counter unless somebody switched firewall logging on.
//
// Mapping the missing four onto empty Linux fields would produce a screen of
// blanks that reads as "broken" rather than "not a thing here". So the Windows
// answer is its own shape, and it says the Windows version of the same four
// questions: is the firewall on, what is open, what stops somebody guessing a
// password, and who is trying.
//
// # What replaces fail2ban
//
// Account lockout. It is the only thing on a stock Windows box that reacts to
// repeated failures, and it is weaker than fail2ban in a way worth saying out
// loud: it locks the *account*, not the address, so it does nothing about
// someone working through account names — which is exactly what the measured
// server was getting (712 failures across 39 different names in 77 minutes).
//
// # Where the numbers come from
//
//	Get-NetFirewallProfile      the three profiles, on or off
//	Get-NetConnectionProfile    which of the three the live network is in
//	Get-NetFirewallRule         inbound allow rules, joined to their ports
//	secedit /export             the lockout policy, in locale-independent keys
//	Get-MpComputerStatus        Defender, the other thing watching
//	OpenSSH/Operational         who is knocking, and how often
//
// `net accounts` is the obvious source for the lockout policy and it is not
// usable: its output is localised, so a parser keyed on its labels reads a
// Korean machine as an empty policy. secedit writes `LockoutBadCount = 10`
// whatever the display language is, in 76 ms.

// windowsSecurityRules caps the rules reported. A stock Windows 10 has 487
// firewall rules and 121 of them are enabled inbound allows; the ones with a
// numeric TCP or UDP port are the ones that answer "what is open", and there
// were 72 even before the cap.
const windowsSecurityRules = 60

// WindowsFirewallProfile is one of Domain, Private and Public.
type WindowsFirewallProfile struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// InboundBlocked is the effective default for connections nothing allows.
	InboundBlocked bool `json:"inboundBlocked"`
	// InboundExplicit is false where the profile says "NotConfigured", which is
	// the stock state and means the Windows default — block. Kept apart because
	// a profile explicitly set to allow inbound is a deliberate act and worth
	// pointing at, while NotConfigured is just an untouched machine.
	InboundExplicit bool `json:"inboundExplicit"`
	// LogBlocked is off on a stock install, which is why this screen has no
	// dropped-packet count where the Linux one does.
	LogBlocked bool `json:"logBlocked"`
	// Active marks the profile the live network is actually in. The other two
	// are switched on and not deciding anything right now.
	Active bool `json:"active"`
}

// WindowsFirewallRule is one inbound allow with a port on it.
type WindowsFirewallRule struct {
	Protocol string `json:"protocol"`
	Port     string `json:"port"`
	Profile  string `json:"profile"`
	Name     string `json:"name"`
}

// WindowsLockout is what happens after repeated failures.
type WindowsLockout struct {
	// Threshold is bad attempts before the account locks. Zero means never,
	// which is the Windows default and the thing worth saying.
	Threshold int `json:"threshold"`
	// Duration and Window are minutes.
	Duration int `json:"duration"`
	Window   int `json:"window"`
}

// WindowsDefender is the other thing on the machine that is watching.
type WindowsDefender struct {
	Enabled  bool `json:"enabled"`
	RealTime bool `json:"realTime"`
	// SignatureAge in days. Defender being on with month-old signatures is a
	// different state from Defender being on.
	SignatureAge int `json:"signatureAge"`
}

// WindowsSecurity is the whole screen.
//
// Every section carries whether it was read. Windows has no sudo, so there is
// no lock to turn and no "retry as administrator": a section either answered or
// it did not, and the screen says which. Reporting an unreadable policy as an
// absent one is how a screen tells somebody they are safe.
type WindowsSecurity struct {
	Profiles     []WindowsFirewallProfile `json:"profiles,omitempty"`
	ProfilesRead bool                     `json:"profilesRead"`

	Rules     []WindowsFirewallRule `json:"rules,omitempty"`
	RulesRead bool                  `json:"rulesRead"`
	// RuleTotal is every enabled inbound allow, including the ones with no port
	// and the ones past the cap, so the list can say "60 of 72".
	RuleTotal int `json:"ruleTotal"`
	// BlockedRemote is the addresses inbound block rules name. Empty on a stock
	// machine, and the nearest thing Windows has to a ban list.
	BlockedRemote []string `json:"blockedRemote,omitempty"`

	Lockout  *WindowsLockout  `json:"lockout,omitempty"`
	Defender *WindowsDefender `json:"defender,omitempty"`

	Failures  []FailureBucket `json:"failures,omitempty"`
	Attackers []Attacker      `json:"attackers,omitempty"`
	Clusters  []SubnetCluster `json:"clusters,omitempty"`
	Failed    int             `json:"failed"`
	// LogSince is the oldest record the OpenSSH log still holds, for the same
	// reason the login history carries one: it is circular and 1 MB, and on a
	// box under attack it covered 77 minutes. A failure chart labelled "24
	// hours" over an hour of data is a lie about how long this has been going on.
	LogSince *time.Time `json:"logSince,omitempty"`
	HasLog   bool       `json:"hasLog"`
}

// WindowsSecurityScript reads the whole screen in one round trip.
func WindowsSecurityScript() string {
	return strings.Join([]string{
		`$ErrorActionPreference = 'SilentlyContinue'`,
		`$epoch = (Get-Date '1970-01-01Z').ToUniversalTime()`,

		// The three profiles, and which one the live network is in.
		`$active = @(Get-NetConnectionProfile | ForEach-Object { $_.NetworkCategory })`,
		`$profs = @(Get-NetFirewallProfile)`,
		`if ($profs.Count -eq 0) { Write-Output '#fwdenied' }`,
		`foreach ($p in $profs) {`,
		`  $on = if ($active -contains $p.Name) { '1' } else { '0' }`,
		`  Write-Output ("#profile|" + $p.Name + "|" + $p.Enabled + "|" + $p.DefaultInboundAction + "|" + $p.LogBlocked + "|" + $on)`,
		`}`,

		// Inbound allows, joined to their ports through a hashtable. The
		// per-rule pipe (`$r | Get-NetFirewallPortFilter`) is the obvious
		// spelling and costs a round trip through the policy store per rule;
		// one bulk read plus a lookup was measured at 2.2 s against 4.6 s.
		`$ports = @{}`,
		`foreach ($f in (Get-NetFirewallPortFilter)) { $ports[$f.InstanceID] = $f }`,
		`$rules = @(Get-NetFirewallRule -Enabled True -Direction Inbound -Action Allow)`,
		`if ($rules.Count -eq 0) { Write-Output '#ruledenied' } else {`,
		`  $kept = 0; $total = 0`,
		`  foreach ($r in $rules) {`,
		`    $pf = $ports[$r.InstanceID]`,
		`    if (-not $pf) { continue }`,
		`    $lp = ($pf.LocalPort -join ',')`,
		// Numeric ports only. Windows uses "RPC", "RPC-EPMap" and "IPHTTPS" as
		// symbolic LocalPort values, and every ICMPv6 rule carries one; they
		// are not ports and nobody is looking for them on this screen.
		`    if ($lp -notmatch '^[0-9][0-9,\-]*$') { continue }`,
		`    $total++`,
		`    if ($kept -lt ` + strconv.Itoa(windowsSecurityRules) + `) {`,
		`      Write-Output ("#rule|" + $pf.Protocol + "|" + $lp + "|" + $r.Profile + "|" + $r.DisplayName)`,
		`      $kept++`,
		`    }`,
		`  }`,
		`  Write-Output ("#rulecount|" + $total)`,
		`}`,

		// The nearest thing to a ban list.
		`foreach ($r in (Get-NetFirewallRule -Enabled True -Direction Inbound -Action Block)) {`,
		`  foreach ($a in ($r | Get-NetFirewallAddressFilter).RemoteAddress) {`,
		`    if ($a -and $a -ne 'Any') { Write-Output ("#blocked|" + $a) }`,
		`  }`,
		`}`,

		// The lockout policy, in keys that do not change with the display
		// language. Written to a temp file because that is secedit's only
		// output channel.
		`$tmp = [IO.Path]::GetTempFileName()`,
		`& secedit.exe /export /cfg $tmp /areas SECURITYPOLICY /quiet 2>&1 | Out-Null`,
		`$cfg = Get-Content $tmp`,
		`Remove-Item $tmp -Force`,
		`if ($cfg) {`,
		`  $bad = 0; $dur = 0; $win = 0`,
		`  foreach ($l in $cfg) {`,
		`    if ($l -match '^\s*LockoutBadCount\s*=\s*(\d+)') { $bad = $matches[1] }`,
		`    if ($l -match '^\s*LockoutDuration\s*=\s*(\d+)') { $dur = $matches[1] }`,
		`    if ($l -match '^\s*ResetLockoutCount\s*=\s*(\d+)') { $win = $matches[1] }`,
		`  }`,
		`  Write-Output ("#lockout|" + $bad + "|" + $dur + "|" + $win)`,
		`}`,

		`$mp = Get-MpComputerStatus`,
		`if ($mp) { Write-Output ("#defender|" + $mp.AMServiceEnabled + "|" + $mp.RealTimeProtectionEnabled + "|" + [int]$mp.AntivirusSignatureAge) }`,

		// Who is knocking, bucketed by the hour on the server.
		`$since = (Get-Date).AddHours(-24)`,
		`$es = @(Get-WinEvent -FilterHashtable @{LogName='OpenSSH/Operational'; StartTime=$since} -EA SilentlyContinue)`,
		`if ($es.Count -eq 0) { Write-Output '#nolog' } else {`,
		`  Write-Output ("#logspan|" + [int64]($es[$es.Count-1].TimeCreated.ToUniversalTime() - $epoch).TotalSeconds)`,
		`  $ips = @{}; $hours = @{}`,
		`  foreach ($e in $es) {`,
		`    $m = ($e.Message -replace "\s+", " ")`,
		// Not 'Invalid user ...' as well: sshd writes one attempt as two
		// records, and counting both doubles every number on the screen.
		`    if ($m -match 'Failed \S+ for (?:invalid user )?\S+ from (\S+) port') {`,
		`      $ips[$matches[1]] = 1 + $ips[$matches[1]]`,
		`      $h = $e.TimeCreated.Date.AddHours($e.TimeCreated.Hour)`,
		`      $k = [int64]($h.ToUniversalTime() - $epoch).TotalSeconds`,
		`      $hours[$k] = 1 + $hours[$k]`,
		`    }`,
		`  }`,
		`  foreach ($k in $hours.Keys) { Write-Output ("#fail|" + $k + "|" + $hours[$k]) }`,
		`  foreach ($k in $ips.Keys) { Write-Output ("#atk|" + $k + "|" + $ips[$k]) }`,
		`}`,
	}, "\n")
}

// ParseWindowsSecurity reads what the script printed.
func ParseWindowsSecurity(raw string) WindowsSecurity {
	out := WindowsSecurity{ProfilesRead: true, RulesRead: true, HasLog: true}
	counts := map[string]int{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if !strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "|")
		switch f[0] {
		case "#fwdenied":
			out.ProfilesRead = false
		case "#ruledenied":
			out.RulesRead = false
		case "#nolog":
			out.HasLog = false

		case "#profile":
			if len(f) != 6 {
				continue
			}
			// "NotConfigured" is the stock state and means the Windows default,
			// which is block. Two sources agreed on the measured machine: all
			// three profiles read NotConfigured while `netsh advfirewall show
			// allprofiles firewallpolicy` reported BlockInbound.
			explicit := !strings.EqualFold(f[3], "NotConfigured")
			out.Profiles = append(out.Profiles, WindowsFirewallProfile{
				Name:            f[1],
				Enabled:         psBool(f[2]),
				InboundBlocked:  !explicit || strings.EqualFold(f[3], "Block"),
				InboundExplicit: explicit,
				LogBlocked:      psBool(f[4]),
				Active:          f[5] == "1",
			})

		case "#rule":
			if len(f) < 5 {
				continue
			}
			out.Rules = append(out.Rules, WindowsFirewallRule{
				Protocol: f[1],
				Port:     f[2],
				Profile:  f[3],
				// Everything after the fourth pipe: a rule's display name is
				// free text and can hold one.
				Name: strings.Join(f[4:], "|"),
			})

		case "#rulecount":
			if len(f) == 2 {
				out.RuleTotal, _ = strconv.Atoi(f[1])
			}

		case "#blocked":
			if len(f) == 2 && f[1] != "" {
				out.BlockedRemote = append(out.BlockedRemote, f[1])
			}

		case "#lockout":
			if len(f) != 4 {
				continue
			}
			bad, err := strconv.Atoi(f[1])
			if err != nil {
				continue
			}
			dur, _ := strconv.Atoi(f[2])
			win, _ := strconv.Atoi(f[3])
			out.Lockout = &WindowsLockout{Threshold: bad, Duration: dur, Window: win}

		case "#defender":
			if len(f) != 4 {
				continue
			}
			age, _ := strconv.Atoi(f[3])
			out.Defender = &WindowsDefender{
				Enabled:      psBool(f[1]),
				RealTime:     psBool(f[2]),
				SignatureAge: age,
			}

		case "#logspan":
			if len(f) == 2 {
				if at, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					t := time.Unix(at, 0)
					out.LogSince = &t
				}
			}

		case "#fail":
			if len(f) != 3 {
				continue
			}
			at, err1 := strconv.ParseInt(f[1], 10, 64)
			n, err2 := strconv.Atoi(f[2])
			if err1 == nil && err2 == nil {
				out.Failures = append(out.Failures, FailureBucket{At: time.Unix(at, 0), Count: n})
				out.Failed += n
			}

		case "#atk":
			if len(f) != 3 {
				continue
			}
			if n, err := strconv.Atoi(f[2]); err == nil {
				counts[f[1]] = n
			}
		}
	}

	// Oldest first, the order a chart is drawn in. PowerShell hashtable keys
	// come back in whatever order they were hashed into.
	sort.Slice(out.Failures, func(i, j int) bool { return out.Failures[i].At.Before(out.Failures[j].At) })

	// The same ranking the Linux screen uses, with the same removal of what is
	// already blocked — a list including handled addresses is one nobody can
	// act on.
	// The same twelve and the same three the Linux screen uses, so two servers
	// side by side are comparable.
	out.Attackers = TopAttackers(counts, out.BlockedRemote, 12)
	out.Clusters = SubnetClusters(out.Attackers, 3)
	return out
}

// psBool reads what PowerShell prints for a boolean.
//
// "True"/"False", and a policy value that never got read comes through as the
// empty string rather than as false — which is why this is not a comparison
// against "False".
func psBool(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "True") }
