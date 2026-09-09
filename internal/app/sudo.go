package app

import (
	"context"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// Privilege escalation (§7.2).
//
// The rule is that LiteDeck never reaches for root on its own. A command runs
// as the logged-in user; if the server refuses, the UI offers to retry as
// administrator and the user decides. Silently prefixing sudo would mean the
// Command Log stopped matching what the user believes they asked for, which is
// exactly the trust the log exists to establish (§4.6).
//
// The password travels on stdin, never in argv. Anything in argv is visible in
// the remote process table to every other user on that machine, and would show
// up verbatim in the Command Log.

// ActionResult is what an action binding returns.
//
// A typed result rather than a bare error, because "you need root" is not a
// failure to report but a question to ask, and the frontend needs to tell the
// two apart without matching on error text.
type ActionResult struct {
	OK bool `json:"ok"`
	// NeedsElevation means the command failed only for want of privileges.
	// The UI shows a "retry as administrator" button.
	NeedsElevation bool   `json:"needsElevation"`
	Error          string `json:"error,omitempty"`
	// Stderr is kept verbatim: §8 requires the original text to stay available.
	Stderr string `json:"stderr,omitempty"`
}

func okResult() ActionResult { return ActionResult{OK: true} }

func failResult(err error) ActionResult {
	return ActionResult{Error: err.Error()}
}

// execMaybeElevated runs a command, optionally through sudo.
func (a *App) execMaybeElevated(
	ctx context.Context, conn *sshcore.Conn, hostID string, elevate bool,
	cmd string, args ...string,
) (*sshcore.Result, error) {
	if !elevate {
		return conn.Exec(ctx, cmd, args...)
	}

	info, err := a.DetectHost(hostID)
	if err != nil {
		return nil, err
	}

	// Where sudo is already authorised, -n runs without ever prompting. Asking
	// for a password the server does not want is worse than useless — it trains
	// the user to type their password at any dialog that appears.
	if info.SudoNoPasswd {
		return conn.Exec(ctx, "sudo", append([]string{"-n", "--", cmd}, args...)...)
	}

	// A lock already turned on this connection answers for every elevated
	// command on it. Without this the security tab asked for the password a
	// second time to read the journal, having just been handed it — and the
	// network tab could unlock and then watch the security tab ask again for
	// the same permission on the same connection.
	if password, ok := a.unlocked.get(hostID, a.mgr.Generation(hostID)); ok {
		return conn.ExecOpts(ctx,
			sshcore.ExecOptions{Stdin: strings.NewReader(password + "\n")},
			"sudo", append([]string{"-S", "-p", "", "--", cmd}, args...)...)
	}

	password, err := a.prompts.secretFunc(hostID, secret.KindSudo, i18n.S("sudo 비밀번호"))()
	if err != nil {
		return nil, err
	}

	// -S reads the password from stdin; -p '' suppresses the prompt text so it
	// does not end up interleaved in stdout. The reader yields EOF after the
	// password, so a wrong password fails instead of hanging on a retry prompt.
	sudoArgs := append([]string{"-S", "-p", "", "--", cmd}, args...)
	return conn.ExecOpts(ctx,
		sshcore.ExecOptions{Stdin: strings.NewReader(password + "\n")},
		"sudo", sudoArgs...,
	)
}

// classify turns a finished command into the result the frontend acts on.
func (a *App) classify(hostID string, res *sshcore.Result, elevated bool) ActionResult {
	if res.OK() {
		return okResult()
	}
	out := ActionResult{
		Error:  res.Err().Error(),
		Stderr: strings.TrimSpace(string(res.Stderr)),
	}

	// Offering elevation after an elevated attempt already failed would loop.
	if !elevated && isPermissionDenied(res) {
		out.NeedsElevation = true
		out.Error = i18n.S("권한이 없습니다 — 관리자 권한으로 다시 시도할 수 있습니다")
		return out
	}

	if elevated && isSudoAuthFailure(res) {
		// A stored sudo password that stopped working would otherwise fail on
		// every attempt, with no way for the user to correct it: the keychain
		// keeps answering before the dialog ever appears. Drop it so the next
		// try asks.
		_ = a.secrets.Delete(hostID, secret.KindSudo)
		out.Error = i18n.S("sudo 인증에 실패했습니다 — 저장된 비밀번호를 지웠습니다. 다시 시도하세요")
	}
	return out
}

func isSudoAuthFailure(res *sshcore.Result) bool {
	s := strings.ToLower(string(res.Stderr))
	for _, marker := range []string{
		"incorrect password", "sorry, try again",
		"no password was provided", "a password is required",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// execUnlocked runs a read with sudo when this connection is already unlocked,
// and plainly when it is not.
//
// It never prompts, and that is the difference from execMaybeElevated. The lock
// is where the asking happens; a view that polls on a timer must not be able to
// put a password dialog on screen every few seconds, and a read that quietly
// degrades — process names missing, everything else there — is the right
// behaviour for one that has not been unlocked.
//
// Reports whether it actually ran elevated, so the view can say which of the
// two answers it is showing.
func (a *App) execUnlocked(
	ctx context.Context, conn *sshcore.Conn, hostID, cmd string, args ...string,
) (*sshcore.Result, bool, error) {
	if info, err := a.DetectHost(hostID); err == nil && info.SudoNoPasswd {
		res, err := conn.ExecOpts(ctx, sshcore.ExecOptions{Kind: sshcore.CommandPoll},
			"sudo", append([]string{"-n", "--", cmd}, args...)...)
		return res, err == nil, err
	}
	if password, ok := a.unlocked.get(hostID, a.mgr.Generation(hostID)); ok {
		// The password goes on stdin, never in argv — argv is visible in the
		// remote process table and in the Command Log (§7.2).
		res, err := conn.ExecOpts(ctx, sshcore.ExecOptions{
			Kind:  sshcore.CommandPoll,
			Stdin: strings.NewReader(password + "\n"),
		}, "sudo", append([]string{"-S", "-p", "", "--", cmd}, args...)...)
		return res, err == nil, err
	}
	res, err := conn.Poll(ctx, cmd, args...)
	return res, false, err
}
