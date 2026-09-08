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
	// Rules is the firewall's own account of itself, and Bans fail2ban's.
	// Empty unless Unlocked.
	Rules string `json:"rules,omitempty"`
	Bans  string `json:"bans,omitempty"`
	// RulesError says why the elevated read did not happen, when it did not.
	RulesError string `json:"rulesError,omitempty"`
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
	units, ufwConf, jails := adapter.SplitSecurityOutput(string(res.Stdout))
	view.Units = adapter.ParseSecurityUnits(units)
	view.UfwEnabled, view.UfwConfFound = adapter.ParseUfwConf(ufwConf)
	if got := adapter.ParseFail2banJails(jails); got != nil {
		view.Jails = got
	}

	if !elevate {
		return view, nil
	}
	if !info.HasSudo {
		view.RulesError = i18n.T("이 계정에는 sudo 가 없습니다")
		return view, nil
	}
	rules, bans, err := a.securityRules(ctx, conn, hostID, info)
	if err != nil {
		// A refused password is a normal answer. The free half stands.
		view.RulesError = err.Error()
		return view, nil
	}
	view.Unlocked = true
	view.Rules = rules
	view.Bans = bans
	return view, nil
}

// securityRules reads the parts that need root, in one elevated round trip.
func (a *App) securityRules(
	ctx context.Context, conn *sshcore.Conn, hostID string, info ServerInfoView,
) (rules, bans string, err error) {
	// Compile-time constant, like the free script. `2>&1` because these tools
	// explain a refusal on stderr and that explanation is the useful part.
	const script = `echo '#rules'
ufw status verbose 2>&1 || nft list ruleset 2>&1 || iptables -S 2>&1
echo '#bans'
fail2ban-client status 2>&1
echo '#end'
:`
	var res *sshcore.Result
	if info.SudoNoPasswd {
		res, err = conn.Exec(ctx, "sudo", "-n", "--", "sh", "-c", script)
	} else {
		password, ok := a.unlocked.get(hostID, a.mgr.Generation(hostID))
		if !ok {
			return "", "", i18n.Errorf("잠겨 있습니다")
		}
		res, err = conn.ExecOpts(ctx,
			sshcore.ExecOptions{Stdin: strings.NewReader(password + "\n")},
			"sudo", "-S", "-p", "", "--", "sh", "-c", script)
	}
	if err != nil {
		return "", "", err
	}
	out := string(res.Stdout)
	rulesPart, bansPart, _ := strings.Cut(out, "#bans")
	rules = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rulesPart), "#rules"))
	bans = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(bansPart), "#end"))
	return rules, bans, nil
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
	password, err := a.prompts.secretFunc(hostID, secret.KindSudo, i18n.S("sudo 비밀번호"))()
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
