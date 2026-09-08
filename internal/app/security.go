package app

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// The security tab (T-35).
//
// One question: is this box exposed. The parts that answer it were already
// being collected and were sitting in three different tabs — listening ports in
// the network view, failed logins in the sessions view, pending security
// updates in monitoring — and none of them could say the thing that matters,
// which is all of it at once. What was missing is what is actually guarding the
// machine, and that is what this file reads.
//
// # The lock
//
// Rules need root. Whether a thing is switched on does not. So the tab opens
// and shows the free half, and the half that needs a password sits behind a
// lock the user turns themselves — never automatically (§7.2).
//
// The unlock lives for one connection. There is no timer: the thing that ends
// it is the connection going away, which is also what makes the answer stale.

// SecurityView is what the tab renders.
type SecurityView struct {
	Units []adapter.SecurityUnit `json:"units"`
	// UfwEnabled is what /etc/ufw/ufw.conf says, and UfwConfFound whether the
	// file was there to say it. Kept apart from the unit state above because
	// the two disagree on real servers and the screen has to show that rather
	// than pick a winner — see adapter/security.go.
	UfwEnabled   bool `json:"ufwEnabled"`
	UfwConfFound bool `json:"ufwConfFound"`
	// Jails are the fail2ban jails the config file declares. The effective set
	// needs fail2ban-client and root; this is the file's word.
	Jails []string `json:"jails"`
	// Kernel is what the packet filter is doing, which the unit states do not
	// say — see adapter.KernelFirewall. nftables.service is a oneshot that
	// Ubuntu ships disabled, so its `dead` means nothing either way.
	Kernel adapter.KernelFirewall `json:"kernel"`
	// Verdict is the one-line answer, and "none" is only said where it can be
	// said: no front end on, and nothing holding a reference on netfilter.
	// Where something is using it but no front end is switched on, this is
	// "unknown" — Docker alone puts hundreds of references on nf_tables, and
	// calling that a firewall would be the opposite mistake.
	Verdict string `json:"verdict"`

	// CanElevate is false where this account has no sudo at all. The lock is
	// then drawn as one that will not open, rather than one that has not been
	// tried.
	CanElevate bool `json:"canElevate"`
	// FreeElevation is true where `sudo -n` works, so unlocking costs no
	// password. Asking for one the server does not want teaches people to type
	// it at any dialog that appears.
	FreeElevation bool `json:"freeElevation"`
	// Unlocked reports whether the fields below were actually read.
	Unlocked bool `json:"unlocked"`
	// Firewall is the rule list, parsed where the tool was ufw. Rules keeps the
	// raw text — for nft and iptables it is all there is, and even for ufw it
	// is what somebody checks the parse against.
	Firewall *adapter.FirewallStatus `json:"firewall,omitempty"`
	Rules    string                  `json:"rules,omitempty"`
	Bans     string                  `json:"bans,omitempty"`
	// Jail is the sshd jail as fail2ban is running it, and Mismatches what the
	// file asked for and did not get. The second one is the reason the first is
	// read: a screen that only reads the file reports an intention as a state,
	// and goes on doing it forever.
	Jail       *adapter.JailStatus    `json:"jail,omitempty"`
	Mismatches []adapter.JailMismatch `json:"mismatches,omitempty"`
	// Sets and Counters are what the ruleset is blocking with, and whether the
	// blocking is doing anything. The counter is the only number on this screen
	// that says a thing is working rather than configured.
	Sets     []adapter.NftSet     `json:"sets,omitempty"`
	Counters []adapter.NftCounter `json:"counters,omitempty"`
	// Attackers is who is still getting through, with everything already
	// blocked removed — a list including handled addresses is one nobody can
	// act on. Clusters groups them by /24, a pattern that only exists when they
	// are put together.
	Attackers []adapter.Attacker      `json:"attackers,omitempty"`
	Clusters  []adapter.SubnetCluster `json:"clusters,omitempty"`
	// AttackersAccess is about the journal alone. "Could not read" and "nobody
	// is knocking" must never arrive as the same screen.
	AttackersAccess EventAccess `json:"attackersAccess"`
	// RulesError says why the elevated read did not happen, when it did not.
	RulesError string `json:"rulesError,omitempty"`

	// rawAttackers is the unfiltered count, kept so the list can be filtered
	// again once the lock reveals what is already blocked. Not sent: a screen
	// showing addresses the firewall already handles is what this avoids.
	rawAttackers map[string]int
}

// Verdicts. Three, not two: "no firewall" is a strong claim and gets said only
// where the evidence supports it.
const (
	// VerdictOn is a front end that says so itself — ufw's own config, or
	// firewalld running.
	VerdictOn = "on"
	// VerdictNone is nothing switched on *and* nothing holding a reference on
	// netfilter. Then there really are no rules.
	VerdictNone = "none"
	// VerdictUnknown is something using netfilter with no front end to name it.
	// Docker does exactly this, and so does a hand-written nft ruleset — the
	// two look identical from here, and the honest move is to say the lock can
	// settle it rather than guess which.
	VerdictUnknown = "unknown"
)

func firewallVerdict(v SecurityView) string {
	// ufw's own file, not its unit: the unit is a oneshot that reports a
	// healthy `active (exited)` with the firewall switched off.
	if v.UfwConfFound && v.UfwEnabled {
		return VerdictOn
	}
	for _, u := range v.Units {
		if u.Name == "firewalld.service" && u.Active {
			return VerdictOn
		}
	}
	if v.Kernel.InUse() {
		return VerdictUnknown
	}
	return VerdictNone
}

// sudoUnlock holds a sudo password for the life of one connection.
//
// Nothing else in this app keeps one in memory: every other elevated command
// asks the keychain and then the user, every time. That is right for a one-off
// action and wrong for a tab, where the same password would be asked for on
// every refresh until somebody ticked "remember" and wrote it to disk — which
// is a bigger commitment than what is wanted here.
//
// So it is held, and held narrowly: in memory only, never written anywhere,
// keyed to the connection it was given for. A reconnect changes the generation
// and the password is gone; so does disconnecting, through forget below.
type sudoUnlock struct {
	mu   sync.Mutex
	byID map[string]sudoUnlockEntry
}

type sudoUnlockEntry struct {
	gen      uint64
	password string
}

func newSudoUnlock() *sudoUnlock { return &sudoUnlock{byID: map[string]sudoUnlockEntry{}} }

func (u *sudoUnlock) get(id string, gen uint64) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.byID[id]
	if !ok || e.gen != gen {
		return "", false
	}
	return e.password, true
}

func (u *sudoUnlock) put(id string, gen uint64, password string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.byID[id] = sudoUnlockEntry{gen: gen, password: password}
}

func (u *sudoUnlock) forget(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.byID, id)
}

// HostSecurity reads what is guarding a host.
//
// elevate is the user turning the lock, never the app deciding to. The free
// half is read either way, so a refused password costs the detail and not the
// screen.
func (a *App) HostSecurity(hostID string, elevate bool) (SecurityView, error) {
	info, err := a.requireCapability(hostID, adapter.CapServices, i18n.S("보안 상태"))
	if err != nil {
		return SecurityView{}, err
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return SecurityView{}, err
	}
	view := SecurityView{
		Units:         []adapter.SecurityUnit{},
		Jails:         []string{},
		CanElevate:    info.HasSudo,
		FreeElevation: info.SudoNoPasswd,
	}

	ctx, cancel := context.WithTimeout(context.Background(), pollTimeout)
	defer cancel()

	// One round trip for the whole free half. Not polled: a firewall does not
	// change between two ticks of a timer, and the audit that came before this
	// feature was about exactly that kind of repeat.
	res, err := conn.Exec(ctx, "sh", "-c", adapter.SecurityScript)
	if err != nil {
		return SecurityView{}, err
	}
	units, ufwConf, jails, modules := adapter.SplitSecurityOutput(string(res.Stdout))
	view.Units = adapter.ParseSecurityUnits(units)
	view.UfwEnabled, view.UfwConfFound = adapter.ParseUfwConf(ufwConf)
	if got := adapter.ParseFail2banJails(jails); got != nil {
		view.Jails = got
	}
	view.Kernel = adapter.ParseFirewallModules(modules)
	view.Verdict = firewallVerdict(view)
	// Kept for the comparison below, which only happens when the lock is open.
	// The file is free to read; what it was supposed to configure is not.
	jailDeclared := adapter.ParseJailDeclared(jails, "sshd")

	// Free, and read whether or not the lock is open: it comes from the
	// journal, which needs the same permission the event timeline needs and
	// not root. Access is reported separately for the same reason it is there —
	// "could not read" and "nobody is knocking" are opposite answers.
	a.readAttackers(ctx, conn, hostID, &view, info, elevate)

	if !elevate {
		return view, nil
	}
	if !info.HasSudo {
		view.RulesError = i18n.T("이 계정에는 sudo 가 없습니다")
		return view, nil
	}
	rules, ruleset, bans, jailOut, err := a.securityRules(ctx, conn, hostID, info)
	if err != nil {
		// A refused password is a normal answer. The free half stands.
		view.RulesError = err.Error()
		return view, nil
	}
	view.Unlocked = true
	view.Rules = rules
	view.Bans = bans
	// Blocked addresses only become known here, so the attacker list is
	// filtered again now that there is something to filter with.
	// From the kernel ruleset, not from ufw's summary: ufw prints its own rules
	// and knows nothing about the hand-made table or fail2ban's.
	view.Sets = adapter.ParseNftSets(ruleset)
	view.Counters = adapter.ParseNftCounters(ruleset)
	view.Attackers = adapter.TopAttackers(view.rawAttackers, blockedFrom(view), 12)
	view.Clusters = adapter.SubnetClusters(view.Attackers, 3)
	// Parsed only for ufw. nft and iptables print something else entirely, and
	// a half-understood ruleset is worse than a plain one because it looks like
	// it was understood.
	if strings.Contains(rules, "Status:") {
		parsed := adapter.ParseUfwStatus(rules)
		view.Firewall = &parsed
	}
	if rules == "" {
		// No ufw on this host. The ruleset is all there is to show, and showing
		// it is better than an empty pane.
		view.Rules = ruleset
	}
	if strings.Contains(jailOut, "Status for the jail") {
		jail := adapter.ParseJailStatus(jailOut)
		view.Jail = &jail
		view.Mismatches = adapter.JailMismatches(jailDeclared, jail)
	}
	return view, nil
}

// securityRulesScript is the elevated read. A compile-time constant, like the
// free one, so passing it to `sh -c` stays inside the argv-only rule (§3.2b).
const securityRulesScript = `echo '#rules'
ufw status verbose 2>/dev/null
echo '#ruleset'
nft list ruleset 2>/dev/null
echo '#iptables'
iptables -S 2>/dev/null
echo '#bans'
fail2ban-client status 2>/dev/null
echo '#get sshd maxretry'
fail2ban-client get sshd maxretry 2>/dev/null
echo '#get sshd findtime'
fail2ban-client get sshd findtime 2>/dev/null
echo '#get sshd bantime'
fail2ban-client get sshd bantime 2>/dev/null
echo '#status sshd'
fail2ban-client status sshd 2>/dev/null
echo '#end'
:`

// securityRules reads the parts that need root, in one elevated round trip.
func (a *App) securityRules(
	ctx context.Context, conn *sshcore.Conn, hostID string, info ServerInfoView,
) (rules, ruleset, bans, jail string, err error) {
	// Compile-time constant, like the free script. `2>&1` because these tools
	// explain a refusal on stderr and that explanation is the useful part.
	// ufw's own summary and the kernel ruleset are read separately, not chained.
	//
	// They answer different questions and a host can want both. `ufw status`
	// lists what ufw was told; `nft list ruleset` is everything the kernel is
	// actually doing, including a hand-made table and fail2ban's — which is
	// where the blocked sets and the drop counters are. Chained with `||`, the
	// ruleset never ran on a host that had ufw, so the tab went half blank on
	// exactly the machines with a firewall to describe.
	//
	// stderr is dropped rather than shown. A missing tool is an answer, and
	// `sh: 2: ufw: not found` in the middle of a rules pane is noise that reads
	// as a fault.
	const script = securityRulesScript
	var res *sshcore.Result
	if info.SudoNoPasswd {
		res, err = conn.Exec(ctx, "sudo", "-n", "--", "sh", "-c", script)
	} else {
		password, ok := a.unlocked.get(hostID, a.mgr.Generation(hostID))
		if !ok {
			return "", "", "", "", i18n.Errorf("잠겨 있습니다")
		}
		res, err = conn.ExecOpts(ctx,
			sshcore.ExecOptions{Stdin: strings.NewReader(password + "\n")},
			"sudo", "-S", "-p", "", "--", "sh", "-c", script)
	}
	if err != nil {
		return "", "", "", "", err
	}
	out := string(res.Stdout)
	head, rest, _ := strings.Cut(out, "#bans")
	bansPart, jailPart, _ := strings.Cut(rest, "#get sshd maxretry")
	rulesPart, kernelPart, _ := strings.Cut(head, "#ruleset")
	rules = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rulesPart), "#rules"))
	// The kernel side is nft where it answered and iptables where it did not.
	// Whichever spoke is what the sets and counters are read from.
	nftPart, iptPart, _ := strings.Cut(kernelPart, "#iptables")
	ruleset = strings.TrimSpace(nftPart)
	if ruleset == "" {
		ruleset = strings.TrimSpace(iptPart)
	}
	bans = strings.TrimSpace(bansPart)
	// Put the marker back: ParseJailStatus keys off it, and cutting on it is
	// what found the boundary.
	jail = strings.TrimSuffix(strings.TrimSpace("#get sshd maxretry"+jailPart), "#end")
	return rules, ruleset, bans, strings.TrimSpace(jail), nil
}

// readAttackers counts who is knocking, and filters out what is already
// handled as far as this call can tell.
func (a *App) readAttackers(
	ctx context.Context, conn *sshcore.Conn, hostID string,
	view *SecurityView, info ServerInfoView, elevate bool,
) {
	if !info.HasSystemd {
		view.AttackersAccess = EventAccessNoJournal
		return
	}
	if !info.CanReadJournal && !elevate {
		if info.HasSudo {
			view.AttackersAccess = EventAccessNeedsSudo
		} else {
			view.AttackersAccess = EventAccessDenied
		}
		return
	}
	res, err := a.execMaybeElevated(ctx, conn, hostID, elevate && !info.CanReadJournal,
		"sh", "-c", adapter.AttackersScript)
	if err != nil || (!res.OK() && len(res.Stdout) == 0) {
		view.AttackersAccess = EventAccessDenied
		return
	}
	view.AttackersAccess = EventAccessOK
	view.rawAttackers = adapter.ParseAttackerCounts(string(res.Stdout))
	// Without the lock the only blocked addresses known are fail2ban's, from
	// its own status. That is better than nothing and less than the ruleset.
	view.Attackers = adapter.TopAttackers(view.rawAttackers, blockedFrom(*view), 12)
	view.Clusters = adapter.SubnetClusters(view.Attackers, 3)
}

// blockedFrom gathers every address this view knows to be blocked already.
func blockedFrom(v SecurityView) []string {
	var blocked []string
	for _, s := range v.Sets {
		blocked = append(blocked, s.Elements...)
	}
	if v.Jail != nil {
		blocked = append(blocked, v.Jail.Banned...)
	}
	return blocked
}

// SecurityLogins is the successful logins, with the addresses this person has
// not seen before marked.
//
// The most urgent line on this screen, and the only one that is. Failed
// passwords arrive by the thousand on any box facing the internet — the servers
// this was built against take two to twenty thousand a day — and they mean
// nothing on their own. A success from an address nobody recognises means
// something whatever the failure count says.
//
// Reading is separate from remembering. The mark moves only when the caller
// says the list has been shown, so an address is surprising exactly once and
// not zero times because a background read got there first.
func (a *App) SecurityLogins(hostID string) (LoginsView, []string, error) {
	view, err := a.HostLogins(hostID, false)
	if err != nil {
		return LoginsView{}, nil, err
	}
	if a.settings == nil {
		return view, nil, nil
	}
	known := map[string]bool{}
	for _, addr := range a.settings.KnownLogins(hostID) {
		known[addr] = true
	}
	var fresh []string
	for _, l := range view.Logins {
		// Boot pseudo-records put the kernel version in the address column.
		// Treating that as a login from an unknown host would flag every
		// reboot, which is the fastest way to make this marker meaningless.
		if l.Boot || l.From == "" || known[l.From] {
			continue
		}
		known[l.From] = true
		fresh = append(fresh, l.From)
	}
	return view, fresh, nil
}

// RememberSecurityLogins marks addresses as seen, after the screen has shown
// them. Returns the ones that were new, which is what it just marked.
func (a *App) RememberSecurityLogins(hostID string, addrs []string) []string {
	if a.settings == nil {
		return nil
	}
	return a.settings.RememberLogins(hostID, addrs)
}

// UnlockSecurity takes the sudo password for this connection.
//
// Asked for once, by the user turning the lock. Held in memory until the
// connection ends — see sudoUnlock for why this one is kept when no other
// password is.
//
// Returns whether the lock opened. Closing the dialog is an answer, not a
// failure, and the caller needs to tell it from a real error without matching
// on the text of one — the same reason ActionResult.NeedsElevation exists.
func (a *App) UnlockSecurity(hostID string) (bool, error) {
	info, err := a.DetectHost(hostID)
	if err != nil {
		return false, err
	}
	if !info.HasSudo {
		return false, i18n.Errorf("이 계정에는 sudo 가 없습니다")
	}
	if info.SudoNoPasswd {
		// Nothing to hold. `sudo -n` works and asking would train the user to
		// type their password at any dialog that appears.
		return true, nil
	}
	// Deliberately not secretFunc: that one reads the keychain and offers to
	// write to it. This lock is a session, not a saved credential — asked every
	// connection, kept nowhere.
	password, err := a.prompts.askSessionSecret(hostID, secret.KindSudo, i18n.S("sudo 비밀번호"))
	if errors.Is(err, ErrPromptCancelled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	a.unlocked.put(hostID, a.mgr.Generation(hostID), password)
	return true, nil
}

// LockSecurity drops a held sudo password without waiting for a disconnect.
func (a *App) LockSecurity(hostID string) {
	a.unlocked.forget(hostID)
}
