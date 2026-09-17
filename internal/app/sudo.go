package app

import (
	"context"
	"errors"
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

// execFailure turns an exec error into a result.
//
// A shut lock is not a failure to report, it is the same question the UI
// already knows how to ask, so it comes back as "needs root" rather than as a
// Go error string that only makes sense to whoever wrote it.
func execFailure(err error) ActionResult {
	if errors.Is(err, ErrSudoLocked) {
		return ActionResult{
			NeedsElevation: true,
			Error:          i18n.S("관리자 권한이 필요합니다 — LiteDeck 에서 잠금을 열어 주세요"),
		}
	}
	return failResult(err)
}

func failResult(err error) ActionResult {
	return ActionResult{Error: err.Error()}
}

// ErrSudoLocked reports that elevation was asked for where no dialog may be
// raised and the lock is not open.
//
// Its own error because the MCP layer turns it into a sentence for the model:
// the person has to open the lock in LiteDeck, and no tool call can do it for
// them.
var ErrSudoLocked = errors.New("app: sudo lock is not open")

// execMaybeElevated runs a command, optionally through sudo.
func (a *App) execMaybeElevated(
	ctx context.Context, conn *sshcore.Conn, hostID string, elevate bool,
	cmd string, args ...string,
) (*sshcore.Result, error) {
	return a.execElevated(ctx, conn, hostID, elevate, true, cmd, args...)
}

// execUnlockedOnly runs a command through sudo using a lock that is already
// open, and fails rather than asking for a password.
//
// For callers that are not a person: an MCP tool call must never make a
// password dialog appear. Nobody is looking at the screen when the model
// decides to restart a unit, and a dialog that arrives unbidden is one that
// gets answered for the wrong reason — or trains its reader to answer any
// dialog at all.
func (a *App) execUnlockedOnly(
	ctx context.Context, conn *sshcore.Conn, hostID string, elevate bool,
	cmd string, args ...string,
) (*sshcore.Result, error) {
	return a.execElevated(ctx, conn, hostID, elevate, false, cmd, args...)
}

func (a *App) execElevated(
	ctx context.Context, conn *sshcore.Conn, hostID string, elevate, mayAsk bool,
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
	if password, ok := a.unlocked.get(hostID, a.connGeneration(hostID)); ok {
		return conn.ExecOpts(ctx,
			sshcore.ExecOptions{Stdin: strings.NewReader(password + "\n")},
			"sudo", append([]string{"-S", "-p", "", "--", cmd}, args...)...)
	}

	// The only way out that is left is asking, and some callers may not.
	if !mayAsk {
		return nil, ErrSudoLocked
	}

	password, err := a.prompts.secretFunc(hostID, secret.KindSudo, i18n.S("sudo 비밀번호"))()
	if err != nil {
		return nil, err
	}

	// -S reads the password from stdin; -p '' suppresses the prompt text so it
	// does not end up interleaved in stdout. The reader yields EOF after the
	// password, so a wrong password fails instead of hanging on a retry prompt.
	sudoArgs := append([]string{"-S", "-p", "", "--", cmd}, args...)
	res, err := conn.ExecOpts(ctx,
		sshcore.ExecOptions{Stdin: strings.NewReader(password + "\n")},
		"sudo", sudoArgs...,
	)
	// A password that worked turns the lock for this connection.
	//
	// The lock used to be filled only by the security and network tabs' own
	// button, so elevating one action left nothing behind: restart a unit with
	// the password, then ask to read its log, and the dialog came back for the
	// same password on the same connection a second later. The commit that
	// promised "one lock per connection" reached the button and not the
	// actions. Only on success, so a typo is not remembered.
	if err == nil && res.OK() {
		// Remembered, not turned: see sudoUnlockEntry. The next elevated action
		// on this connection goes through without asking; the security tab's
		// own lock still belongs to its button.
		a.unlocked.remember(hostID, a.connGeneration(hostID), password)
	}
	return res, err
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
	if password, ok := a.unlocked.get(hostID, a.connGeneration(hostID)); ok {
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
