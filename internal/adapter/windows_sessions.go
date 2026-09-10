package adapter

import (
	"strconv"
	"strings"
	"time"
)

// Who is logged in, on Windows (§4.5).
//
// # Why ps does not work here
//
// The POSIX path reads `ps` for the "sshd: user@pts/0" process that OpenSSH
// creates per login. Windows OpenSSH does not create it: the command line of
// every one of its processes is just the executable path. So the capability was
// off and the tab said the server could not answer, over the top of a server
// that knows perfectly well who is on it.
//
// # What a session is here
//
// One connection makes three processes. The listener runs as SYSTEM; each
// connection gets a `-R` child, also SYSTEM; and under that a `-z` child owned
// by **the account that logged in**. Measured on Windows 10 19045:
//
//	pid=2908 ppid=724  user=SYSTEM     sshd.exe            ← the listener
//	pid=4628 ppid=2908 user=SYSTEM     sshd.exe -R         ← one connection
//	pid=6460 ppid=4628 user=KTJ        sshd.exe -z         ← the session
//	pid=4652 ppid=3876 user=sshd_3876  sshd.exe -y         ← not yet logged in
//
// So a session is an sshd process owned by a real account. The `sshd_NNNN`
// owner is the privilege-separation account used before authentication
// succeeds — a stranger knocking, not somebody who is in.
//
// # Where the client address comes from
//
// Not from the socket table: Get-NetTCPConnection attributes every established
// connection on port 22 to the listener, so it cannot say which one belongs to
// which session. The OpenSSH event log can — "Accepted publickey for ktj from
// 121.151.244.121 port 52025" — and it is timestamped, so a login is matched to
// the process that started at the same moment.

// windowsSessionAge is how far back the log is read for the address of a login.
// Sessions older than this keep their user and their uptime and lose only the
// address, which is the right way round.
const windowsSessionLogHours = 24

// WindowsSessionsScript collects the process table, our own session and the
// authentication log in one round trip.
func WindowsSessionsScript() string {
	return strings.Join([]string{
		`$ErrorActionPreference = 'SilentlyContinue'`,
		// Every sshd process with its owner. GetOwner is a separate call per
		// process, which is why this is not a one-liner.
		`foreach ($p in (Get-CimInstance Win32_Process -Filter "Name='sshd.exe'")) {`,
		`  $o = Invoke-CimMethod -InputObject $p -MethodName GetOwner`,
		`  $t = [int64]($p.CreationDate.ToUniversalTime() - (Get-Date '1970-01-01Z').ToUniversalTime()).TotalSeconds`,
		`  Write-Output ("#proc|" + $p.ProcessId + "|" + $p.ParentProcessId + "|" + $o.User + "|" + $t)`,
		`}`,
		// Our own session, by walking up from this process. Bounded: the parent
		// of process 0 is process 0, and an unbounded walk there never returns.
		`$me = Get-CimInstance Win32_Process -Filter "ProcessId=$PID"`,
		`for ($i = 0; $i -lt 8 -and $me; $i++) {`,
		`  if ($me.Name -eq 'sshd.exe') { Write-Output ("#self|" + $me.ProcessId); break }`,
		`  if ($me.ParentProcessId -le 4) { break }`,
		`  $me = Get-CimInstance Win32_Process -Filter "ProcessId=$($me.ParentProcessId)"`,
		`}`,
		// Successful logins, newest first.
		//
		// The whole window rather than a first-N cap. Measured on Windows 10
		// 19045: the log open dominates and the record count does not — 400
		// records and 2,343 records both took 688 ms. A cap bought nothing and
		// cost the addresses of any session older than the last few minutes,
		// because on a box under a password attack 400 records is 13 minutes.
		`$since = (Get-Date).AddHours(-` + strconv.Itoa(windowsSessionLogHours) + `)`,
		`foreach ($e in (Get-WinEvent -FilterHashtable @{LogName='OpenSSH/Operational'; StartTime=$since} -EA SilentlyContinue)) {`,
		`  $m = ($e.Message -replace "\s+", " ")`,
		`  if ($m -match 'Accepted \w+ for (\S+) from (\S+) port') {`,
		`    $t = [int64]($e.TimeCreated.ToUniversalTime() - (Get-Date '1970-01-01Z').ToUniversalTime()).TotalSeconds`,
		`    Write-Output ("#auth|" + $t + "|" + $matches[1] + "|" + $matches[2])`,
		`  }`,
		`}`,
	}, "\n")
}

// authMatchWindow is how far apart a login record and its process may be.
//
// The event is written when authentication succeeds and the process appears at
// the same moment; a few seconds covers a loaded machine without letting one
// login claim the next one's address.
const authMatchWindow = 5 * time.Second

type winProc struct {
	pid, ppid int
	user      string
	started   int64
}

type winAuth struct {
	at   int64
	user string
	from string
}

// ParseWindowsSessions turns the probe's output into the session list.
//
// now is passed in rather than read so the elapsed times are testable.
func ParseWindowsSessions(raw string, now time.Time) []SSHSession {
	var procs []winProc
	var auths []winAuth
	self := 0

	for _, line := range strings.Split(raw, "\n") {
		f := strings.Split(strings.TrimRight(strings.TrimSpace(line), "\r"), "|")
		switch {
		case len(f) == 2 && f[0] == "#self":
			self, _ = strconv.Atoi(f[1])
		case len(f) == 5 && f[0] == "#proc":
			pid, err1 := strconv.Atoi(f[1])
			ppid, _ := strconv.Atoi(f[2])
			started, err2 := strconv.ParseInt(f[4], 10, 64)
			if err1 == nil && err2 == nil {
				procs = append(procs, winProc{pid: pid, ppid: ppid, user: f[3], started: started})
			}
		case len(f) == 4 && f[0] == "#auth":
			at, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil {
				auths = append(auths, winAuth{at: at, user: f[2], from: f[3]})
			}
		}
	}

	// The listener and the per-connection helpers are not sessions. What is left
	// is one process per account that is actually logged in.
	out := []SSHSession{}
	for _, p := range procs {
		if !realAccount(p.user) {
			continue
		}
		s := SSHSession{
			PID:     p.pid,
			PPID:    p.ppid,
			User:    p.user,
			Elapsed: now.Unix() - p.started,
			Self:    p.pid == self,
		}
		if s.Elapsed < 0 {
			s.Elapsed = 0
		}
		if from, ok := matchAuth(auths, p, s.User); ok {
			s.From = from
		}
		out = append(out, s)
	}
	return out
}

// realAccount rejects the owners that are not somebody logging in.
//
// SYSTEM owns the listener and the per-connection parent. sshd_NNNN is the
// privilege-separation account OpenSSH uses *before* authentication succeeds —
// counting it would put every failed knock on the screen as a session.
func realAccount(user string) bool {
	switch {
	case user == "":
		return false
	case strings.EqualFold(user, "SYSTEM"), strings.EqualFold(user, "LOCAL SERVICE"),
		strings.EqualFold(user, "NETWORK SERVICE"):
		return false
	case strings.HasPrefix(strings.ToLower(user), "sshd_"):
		return false
	}
	return true
}

// matchAuth finds the login record that belongs to this process.
//
// By time and by user, nearest first. Matching on user alone would give every
// session of one account the same address, which is wrong the moment somebody
// is logged in twice from two places — the case the tab exists for.
func matchAuth(auths []winAuth, p winProc, user string) (string, bool) {
	best, bestGap := "", authMatchWindow
	for _, a := range auths {
		if !strings.EqualFold(a.user, user) {
			continue
		}
		gap := time.Duration(abs64(a.at-p.started)) * time.Second
		if gap <= bestGap {
			best, bestGap = a.from, gap
		}
	}
	return best, best != ""
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// WindowsEndSessionScript ends one login and reports what is left.
//
// # Why taskkill /T and not Stop-Process
//
// Stop-Process ends the one process it is given, and on this platform that is
// not the session. Measured on Windows 10 19045 against a live login: killing
// the session's sshd left cmd.exe and the powershell.exe under it running, and
// the client stayed connected — the socket is held by the SYSTEM-owned sshd one
// level up, which had no reason to notice. The person clicking "끊기" got a
// server that said yes and a session that carried on.
//
// `taskkill /F /T` ends the tree. The same login, killed that way: powershell,
// conhost, cmd and the session's sshd all went, the SYSTEM parent exited on its
// own once its child was gone, and the client dropped.
//
// # Why the answer is read from the process table
//
// Not from the exit code. taskkill exits 128 for "no such process", which is
// failure by its reckoning and success by ours, and its stderr is localised —
// on the measured machine it is Korean. Whether the process is still there
// afterwards is the same question in every locale.
func WindowsEndSessionScript(pid int) string {
	id := strconv.Itoa(pid)
	return strings.Join([]string{
		`$ErrorActionPreference = 'SilentlyContinue'`,
		`& taskkill.exe /PID ` + id + ` /F /T 2>&1 | Out-Null`,
		`Start-Sleep -Milliseconds 400`,
		`if (Get-CimInstance Win32_Process -Filter "ProcessId=` + id + `") {`,
		`  Write-Output 'END=alive'`,
		`} else {`,
		`  Write-Output 'END=gone'`,
		`}`,
	}, "\n")
}

// ParseWindowsEndSession reports whether the session is gone.
//
// Anything that is not a clear "gone" is a failure. A probe that produced no
// output at all did not get far enough to look, and reporting that as success
// is how the caller learns nothing happened only when somebody complains.
func ParseWindowsEndSession(raw string) bool {
	return strings.Contains(raw, "END=gone")
}
