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
		>"$OUT/$name.out" 2>"$OUT/$name.err"
	status=$?
	echo "$status" >"$OUT/$name.exit"
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
capture win32-service "${PRELUDE}Get-CimInstance Win32_Service | Select-Object Name,DisplayName,State,StartMode,Status,ProcessId,PathName,StartName,Description,AcceptStop,AcceptPause | ConvertTo-Json -Compress -Depth 3"

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
# What a missing cmdlet and a permission denial actually look like. The adapter
# has to tell them apart, and guessing at the text is how parsers end up wrong.
capture missing-cmdlet "${PRELUDE}Get-NoSuchCmdlet"
capture eventlog-denied "${PRELUDE}Get-WinEvent -LogName Security -MaxEvents 1 | ConvertTo-Json -Compress"

# --- single-element JSON --------------------------------------------------
# ConvertTo-Json emits a bare object, not a one-element array, when the pipeline
# yields exactly one item. Every PowerShell JSON parser gets this wrong once; the
# capture makes it impossible to forget.
capture single-service "${PRELUDE}Get-Service | Select-Object -First 1 Name,Status | ConvertTo-Json -Compress"

{
	echo "host:      $HOST"
	echo "captured:  $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	echo "client:    $(uname -sr)"
	echo "transport: ssh + powershell -NoProfile -NonInteractive -EncodedCommand"
	echo
	echo "Raw output as it arrived over SSH. Not reformatted — the encoding and"
	echo "line endings are part of what is being recorded."
} >"$OUT/provenance.txt"

echo
echo "done. review $OUT/provenance.txt and the .out/.err/.exit files."
