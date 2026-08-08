#!/usr/bin/env bash
# Capture golden files from a real Windows server, over SSH, the way LiteDeck
# will actually talk to it.
#
#     ./capture.sh <ssh-host> [outdir]
#
# Run this from the client machine rather than on the Windows box itself. The
# point is to record what arrives at the other end of the SSH channel — encoding,
# line endings, and the exact JSON shape — not what the commands print locally.
# Capturing on the box would hide precisely the transport problems this adapter
# has to survive.
#
# Every command goes through -EncodedCommand: the script is base64'd UTF-16LE, so
# no shell metacharacter can escape it and it behaves identically whether the
# configured SSH shell is cmd.exe or PowerShell. That is also how the adapter
# will issue commands, which is why the capture uses it too — a golden file taken
# through a different path is a golden file for a different program.
set -uo pipefail

HOST="${1:-}"
OUT="${2:-$(dirname "$0")/golden}"
if [ -z "$HOST" ]; then
	echo "usage: $0 <ssh-host> [outdir]" >&2
	exit 2
fi
mkdir -p "$OUT"

# ps_encode turns a UTF-8 script into what -EncodedCommand expects.
ps_encode() { iconv -f UTF-8 -t UTF-16LE | base64 | tr -d '\n'; }

# capture <name> <powershell-script>
#
# Records stdout, stderr and the exit status separately. stderr matters as much
# as stdout here: a cmdlet that is missing on Server Core, or a counter that
# needs administrator, fails in a way the adapter has to recognise.
capture() {
	local name="$1" script="$2" enc status
	enc=$(printf '%s' "$script" | ps_encode)
	printf '  %-28s' "$name"
	ssh -o BatchMode=no "$HOST" \
		"powershell -NoProfile -NonInteractive -EncodedCommand $enc" \
		>"$OUT/$name.out" 2>"$OUT/$name.err.raw"
	status=$?
	echo "$status" >"$OUT/$name.exit"
	# The ssh client writes its own advisories to the same stream as the remote
	# command's stderr. Left in, every single capture appeared to have 222 bytes
	# of stderr, which makes "did this command actually fail?" unanswerable at a
	# glance and puts a client-side warning into a server golden file.
	grep -v '^\*\* ' "$OUT/$name.err.raw" >"$OUT/$name.err" || true
	rm -f "$OUT/$name.err.raw"
	if [ -s "$OUT/$name.err" ]; then
		echo "exit=$status  (stderr $(wc -c <"$OUT/$name.err" | tr -d ' ') bytes)"
	else
		echo "exit=$status  $(wc -c <"$OUT/$name.out" | tr -d ' ') bytes"
	fi
}

# Force UTF-8 on the way out. Without this the console uses the machine's OEM
# codepage — 949 on a Korean install — and every non-ASCII service description
# arrives as mojibake. This prelude is prepended to every JSON capture, and the
# adapter will carry the same one for the same reason.
PRELUDE='$OutputEncoding=[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $ProgressPreference="SilentlyContinue"; '

echo "capturing from $HOST -> $OUT"

# --- identity -------------------------------------------------------------
# What the platform gate keys off, plus the version facts that decide which
# cmdlets exist. Get-CimInstance replaced Get-WmiObject in PS 3.0 and is gone
# from PowerShell 7 on some editions; Server Core lacks others entirely.
capture ver 'cmd /c ver'
capture psversion "${PRELUDE}\$PSVersionTable | ConvertTo-Json -Compress"
capture whoami "${PRELUDE}[PSCustomObject]@{
  user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
  admin=([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  groups=@([Security.Principal.WindowsIdentity]::GetCurrent().Groups | ForEach-Object { \$_.Translate([Security.Principal.NTAccount]).Value })
} | ConvertTo-Json -Compress"
capture osinfo "${PRELUDE}Get-CimInstance Win32_OperatingSystem | Select-Object Caption,Version,BuildNumber,OSArchitecture,LastBootUpTime,TotalVisibleMemorySize,FreePhysicalMemory | ConvertTo-Json -Compress"

# --- services -------------------------------------------------------------
# Win32_Service rather than Get-Service: it carries StartMode (the enabled/
# disabled equivalent), the binary path, the owning account and the PID, none of
# which Get-Service exposes. Both are captured so the trade-off is documented in
# real output rather than from memory.
capture get-service "${PRELUDE}Get-Service | Select-Object Name,DisplayName,Status,StartType | ConvertTo-Json -Compress -Depth 3"
# DelayedAutoStart is the field that makes the failed filter usable. Without it,
# StartMode=Auto + State=Stopped looks like a service that should be running, and
# on a stock Windows box that is four false positives — edgeupdate, MapsBroker and
# sppsvc are all Automatic (Delayed Start) and are meant to be stopped. ExitCode
# catches a service that tried to start and could not.
capture win32-service "${PRELUDE}Get-CimInstance Win32_Service | Select-Object Name,DisplayName,State,StartMode,Status,ProcessId,PathName,StartName,Description,AcceptStop,AcceptPause,DelayedAutoStart,ExitCode,ServiceSpecificExitCode | ConvertTo-Json -Compress -Depth 3"

# --- processes ------------------------------------------------------------
# Two sources again: Win32_Process has the command line and parent PID (the tree
# view needs both), Get-Process has the working set and CPU seconds.
capture win32-process "${PRELUDE}Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine,WorkingSetSize,CreationDate | ConvertTo-Json -Compress -Depth 3"
capture get-process "${PRELUDE}Get-Process | Select-Object Id,ProcessName,WorkingSet64,CPU,StartTime,Responding | ConvertTo-Json -Compress -Depth 3"
# Process owner needs a second call and often fails for system processes when
# not administrator — capture the failure, it is part of the contract.
capture process-owner "${PRELUDE}Get-CimInstance Win32_Process | ForEach-Object { \$o=Invoke-CimMethod -InputObject \$_ -MethodName GetOwner -ErrorAction SilentlyContinue; [PSCustomObject]@{pid=\$_.ProcessId; user=\$(if(\$o.User){\"\$(\$o.Domain)\\\\\$(\$o.User)\"}else{\$null})} } | ConvertTo-Json -Compress"

# --- metrics --------------------------------------------------------------
# Get-Counter is the accurate source but takes a full sample interval and needs
# privileges; the PerfFormattedData class is instant. Capture both and let the
# timings in the .exit files decide which the adapter uses.
capture counter-cpu "${PRELUDE}(Get-Counter '\\Processor(_Total)\\% Processor Time' -ErrorAction Stop).CounterSamples | Select-Object CookedValue | ConvertTo-Json -Compress"
capture perf-cpu "${PRELUDE}Get-CimInstance Win32_PerfFormattedData_PerfOS_Processor | Where-Object Name -eq '_Total' | Select-Object PercentProcessorTime | ConvertTo-Json -Compress"
# Per-process CPU. Get-Process reports cumulative CPU seconds since start, which
# is not a percentage and is meaningless for a process that has been up for a
# week; this class gives the instantaneous figure the table column wants.
capture perf-proc "${PRELUDE}Get-CimInstance Win32_PerfFormattedData_PerfProc_Process | Select-Object IDProcess,PercentProcessorTime,Name | ConvertTo-Json -Compress"
capture pagefile "${PRELUDE}Get-CimInstance Win32_PageFileUsage | Select-Object Name,AllocatedBaseSize,CurrentUsage,PeakUsage | ConvertTo-Json -Compress"
capture logicaldisk "${PRELUDE}Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3' | Select-Object DeviceID,VolumeName,Size,FreeSpace,FileSystem | ConvertTo-Json -Compress"

# --- network --------------------------------------------------------------
capture netip "${PRELUDE}Get-NetIPAddress | Select-Object InterfaceAlias,IPAddress,PrefixLength,AddressFamily,InterfaceIndex | ConvertTo-Json -Compress"
capture nettcp "${PRELUDE}Get-NetTCPConnection -State Listen | Select-Object LocalAddress,LocalPort,OwningProcess | ConvertTo-Json -Compress"

# --- containers -----------------------------------------------------------
# Docker Desktop on Windows speaks the same CLI, so the existing container
# parser may work unchanged. That is worth knowing before writing a second one.
capture docker-version 'docker version --format "{{json .}}"'
capture docker-ps 'docker ps -a --no-trunc --format "{{json .}}"'

# --- shape of failure -----------------------------------------------------
# What a missing cmdlet looks like. The adapter has to tell "this capability is
# absent" from "this broke", and guessing at the text is how parsers end up
# wrong. PowerShell 5.1 wraps error records in CLIXML when stdout is not a
# console, which is only visible in a real capture.
capture missing-cmdlet "${PRELUDE}Get-NoSuchCmdlet"

# A permission failure, without reading anything privileged. Get-WinEvent on the
# Security log was tried first and is not usable as a fixture: on an
# administrator account it succeeds, so it records an audit-log entry from
# someone's machine instead of the error shape it was meant to capture.
capture access-denied "${PRELUDE}[IO.File]::ReadAllText('C:\\Windows\\System32\\config\\SAM')"

# --- single-element JSON --------------------------------------------------
# ConvertTo-Json emits a bare object, not a one-element array, when the pipeline
# yields exactly one item. Every PowerShell JSON parser gets this wrong once; the
# capture makes it impossible to forget.
capture single-service "${PRELUDE}Get-Service | Select-Object -First 1 Name,Status | ConvertTo-Json -Compress"
# The same shape from the source the service adapter actually reads. The
# Get-Service capture above is not interchangeable with it: its Status is an enum
# integer, so feeding it to the Win32_Service parser tests nothing but the type
# mismatch between two different projections.
capture single-win32-service "${PRELUDE}Get-CimInstance Win32_Service | Select-Object -First 1 Name,DisplayName,State,StartMode,Status,ProcessId,PathName,StartName,Description,AcceptStop,AcceptPause,DelayedAutoStart,ExitCode,ServiceSpecificExitCode | ConvertTo-Json -Compress -Depth 3"

# --- anonymise -------------------------------------------------------------
# These files are committed to a public repository, and a capture of a real
# machine carries its computer name, its account names and its LAN addressing.
# None of that is what a golden file is for: the parser never looks at the
# hostname, it looks at whether Status is the integer 1 and State is the string
# "Stopped" and whether a Korean description survived the encoding. So the
# identities are replaced and everything structural is left exactly as it
# arrived — same key order, same enum values, same /Date(ms)/ serialisation, same
# CRLF line endings.
#
# Done here rather than by hand so the next person capturing from their own box
# does not have to remember, or notice.
echo
echo "anonymising"
python3 - "$OUT" <<'PY'
import json, pathlib, re, sys

out = pathlib.Path(sys.argv[1])

machine = user = None
try:
    who = json.loads((out / "whoami.out").read_text(encoding="utf-8"))
    full = who.get("user", "")           # DESKTOP-ABC123\SOMEUSER
    if "\\" in full:
        machine, user = full.split("\\", 1)
except Exception:
    pass

# Exact-string substitutions first, so a username that happens to be a substring
# of something else cannot be partially rewritten.
subs = []
if machine:
    subs.append((machine, "DESKTOP-EXAMPLE"))
if user:
    subs.append((user, "TESTUSER"))

# RFC 5737 documentation range for the routable/LAN v4 address. Loopback and
# link-local are kept: they are the same on every machine and the exposure logic
# is tested against them.
#
# Only quoted values, and only in .out files. A bare dotted-quad regex also
# matches things that are not addresses: the first version of this rewrote the
# CLIXML schema version in every stderr capture, turning
# <Objs Version="1.1.0.1"> into <Objs Version="192.0.2.14"> and corrupting the
# very fixtures the decoder is written against.
def redact_ipv4(text):
    def repl(m):
        ip = m.group(1)
        if ip.startswith(("127.", "169.254.", "0.")):
            return m.group(0)
        return '"192.0.2.14"'
    return re.sub(r'"((?:\d{1,3}\.){3}\d{1,3})"', repl, text)

# The interface identifier of a link-local address is derived from, or randomised
# per, the adapter — either way it identifies the machine. The fe80::/64 prefix
# and the %zone suffix are what the parser cares about, so only the middle goes.
def redact_ipv6_iid(text):
    return re.sub(r"fe80::[0-9a-f:]+", "fe80::1111:2222:3333:4444", text, flags=re.I)

count = 0
for p in sorted(out.iterdir()):
    if p.suffix not in (".out", ".err", ".txt"):
        continue
    try:
        s = original = p.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        # Not UTF-8. Left byte-for-byte on purpose: docker-*.err is captured
        # without the encoding prelude and arrives in the OEM codepage, which is
        # the evidence that the prelude is load-bearing. Rewriting it would
        # destroy the only mojibake fixture in the tree.
        continue
    for old, new in subs:
        s = s.replace(old, new)
    # Addresses only where addresses belong. stderr carries no host addressing,
    # and it does carry version strings that look like one.
    if p.suffix == ".out":
        s = redact_ipv4(s)
    s = redact_ipv6_iid(s)
    if s != original:
        p.write_text(s, encoding="utf-8")
        count += 1

print(f"  rewrote {count} file(s); machine={machine!r} user={user!r}")
PY

{
	echo "host:      (anonymised)"
	echo "captured:  $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	echo "client:    $(uname -sr)"
	echo "transport: ssh + powershell -NoProfile -NonInteractive -EncodedCommand"
	echo
	echo "Output as it arrived over SSH. Structure is verbatim — key order, enum"
	echo "values, /Date(ms)/ serialisation, CRLF line endings and UTF-8 non-ASCII"
	echo "text are all exactly what the server sent."
	echo
	echo "Identities are not verbatim. The capture script replaces the computer"
	echo "name with DESKTOP-EXAMPLE, the account with TESTUSER, routable IPv4 with"
	echo "192.0.2.14 and link-local interface identifiers with a fixed value,"
	echo "because these files are public and none of it is what a parser reads."
	echo "Loopback and 169.254 addresses are kept: they are identical on every"
	echo "machine and the exposed-binding logic is tested against them."
} >"$OUT/provenance.txt"

echo
echo "done. review $OUT/provenance.txt and the .out/.err/.exit files."
