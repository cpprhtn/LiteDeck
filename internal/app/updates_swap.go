package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// The helper that replaces the running copy.
//
// # Why a script and not Go
//
// Whatever does the swap has to still be running after this process is gone,
// and it must not be the thing being replaced. A second Go binary would have to
// be shipped, found and kept in step with the app. `/bin/sh` and `cmd` are
// already on the machine and are not what is being overwritten.
//
// # Why the paths are arguments
//
// Both scripts below are compile-time constants — the §3.2b exception to the
// argv-only rule — and every path they touch arrives as $1..$3 rather than
// being pasted into the text. A home directory with a space in it is ordinary,
// and quoting it into a shell script correctly is the kind of thing that works
// on the machine it was written on.
//
// # What happens when it goes wrong
//
// The old copy is moved aside, not deleted, until the new one is in place. A
// failed move puts it back. The worst case is an app that did not update.

const swapSh = `#!/bin/sh
# $1 pid to wait for, $2 what to replace, $3 the replacement
set -u
i=0
while kill -0 "$1" 2>/dev/null; do
  i=$((i + 1))
  # ~20s. A window that will not close is a reason to stop, not to wait
  # forever with the replacement sitting in /tmp.
  [ "$i" -gt 100 ] && exit 1
  sleep 0.2
done

# Relaunch whatever is at $2 and stop. Reached from every failure after the app
# has already quit: without it "Install update" made the window disappear and
# nothing came back — on a read-only DMG, under macOS App Translocation, from a
# /Applications the user cannot write, or with Defender holding the file open.
# An app that did not update is a small disappointment; an app that is gone is
# not.
relaunch_and_die() {
  case "$(uname -s)" in
    Darwin) open "$2" ;;
    *) "$2" >/dev/null 2>&1 & ;;
  esac
  exit 1
}

old="$2.litedeck-old"
rm -rf "$old"
mv "$2" "$old" || relaunch_and_die
if ! mv "$3" "$2"; then
  mv "$old" "$2" || exit 1
  relaunch_and_die
fi
rm -rf "$old"

case "$(uname -s)" in
  Darwin) open "$2" ;;
  *) "$2" >/dev/null 2>&1 & ;;
esac

rm -rf "$(dirname "$3")"
rm -f "$0"
rmdir "$(dirname "$0")" 2>/dev/null
`

const swapCmd = `@echo off
setlocal
rem %1 pid to wait for, %2 what to replace, %3 the replacement
set /a i=0
:wait
tasklist /FI "PID eq %~1" /NH 2>nul | find "%~1" >nul
if errorlevel 1 goto gone
set /a i+=1
if %i% GTR 100 exit /b 1
timeout /t 1 /nobreak >nul
goto wait

:gone
set "old=%~2.litedeck-old"
if exist "%old%" del /f /q "%old%"
rem Every failure after the app has quit relaunches what is there. Without it
rem "Install update" made the window vanish with nothing coming back — Defender
rem holding the file open is enough to reach this.
move /y "%~2" "%old%" >nul || (start "" "%~2" & exit /b 1)
move /y "%~3" "%~2" >nul || (move /y "%old%" "%~2" >nul & start "" "%~2" & exit /b 1)
del /f /q "%old%" >nul 2>&1

start "" "%~2"
rmdir /s /q "%~dp3" >nul 2>&1
del /f /q "%~f0" >nul 2>&1
`

// writeSwapHelper puts the script somewhere it can run from and returns its
// path. The paths it works on are passed to it as arguments (see swapArgs).
func writeSwapHelper(staged string) (string, error) {
	name, body, mode := "litedeck-swap.sh", swapSh, os.FileMode(0o700)
	if runtime.GOOS == "windows" {
		name, body = "litedeck-swap.cmd", swapCmd
	}
	// Beside the staged copy, not in the app's own directory: that one is about
	// to be moved out from under it.
	path := filepath.Join(filepath.Dir(filepath.Dir(staged)), name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return "", err
	}
	return path, nil
}

// swapArgs is what the helper is invoked with, in order.
func swapArgs(pid int, target, staged string) []string {
	return []string{strconv.Itoa(pid), target, staged}
}
