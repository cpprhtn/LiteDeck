package app

// Windows support at the binding layer: one helper for running a PowerShell
// script, and the branches that send a request to the Windows adapter instead of
// the Linux one.
//
// The split is here rather than inside each parser because the two platforms need
// different *commands*, not different post-processing. Everything below produces
// the shapes the frontend already renders, so no view knows which OS answered.

import (
	"context"
	"fmt"

	"github.com/cpprhtn/LiteDeck/internal/adapter/windowspowershell"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// runPowerShell executes a script and returns stdout.
//
// Errors carry the decoded CLIXML rather than the envelope. PowerShell 5.1 writes
// error records as XML whenever stdout is not a console, which over SSH it never
// is, so the raw form is a schema URL and _x000D__x000A_ escapes wrapped around
// the one sentence worth reading.
func (a *App) runPowerShell(ctx context.Context, conn *sshcore.Conn, kind sshcore.CommandKind, script string) ([]byte, error) {
	args := windowspowershell.Args(script)
	res, err := conn.ExecOpts(ctx, sshcore.ExecOptions{Kind: kind}, windowspowershell.Executable, args...)
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		if text := windowspowershell.ErrorText(res.Stderr); text != "" {
			return nil, fmt.Errorf("%s", text)
		}
		return nil, fmt.Errorf("powershell exited %d", res.ExitCode)
	}
	return res.Stdout, nil
}

// windowsActionResult turns a PowerShell failure into the shape the UI expects.
//
// NeedsElevation is never set. Windows has no sudo: an operation that needs
// administrator needs a different login, so offering "retry as administrator"
// would be a button that cannot work. Saying so in the error is more useful than
// a retry that fails identically.
func windowsActionResult(res *sshcore.Result, err error) ActionResult {
	if err != nil {
		return failResult(err)
	}
	if res.OK() {
		return okResult()
	}

	text := windowspowershell.ErrorText(res.Stderr)
	if text == "" {
		text = fmt.Sprintf("powershell exited %d", res.ExitCode)
	}
	if windowspowershell.IsAccessDenied(res.Stderr) {
		text = "권한이 없습니다 — 이 계정은 관리자 그룹이 아닙니다. " +
			"Windows에는 sudo가 없어 같은 세션에서 권한을 올릴 수 없습니다: " +
			"관리자 계정으로 다시 접속해야 합니다.\n\n" + text
	}
	return ActionResult{OK: false, Error: text, Stderr: text}
}
