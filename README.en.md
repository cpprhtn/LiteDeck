<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
    <img src="docs/logo.png" width="360" alt="LiteDeck">
  </picture>
</p>

<h1 align="center">LiteDeck</h1>

<p align="center">
  <b>Manage a remote server from a local native GUI over SSH alone — with nothing installed on the server.</b>
</p>

<p align="center">
  <a href="https://github.com/cpprhtn/LiteDeck/actions/workflows/ci.yml"><img src="https://github.com/cpprhtn/LiteDeck/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/cpprhtn/LiteDeck/releases"><img src="https://img.shields.io/github/v/release/cpprhtn/LiteDeck?include_prereleases&label=release&color=orange" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey" alt="Platform">
</p>

<p align="center">
  <a href="README.md">한국어</a> · <b>English</b>
</p>

<p align="center">
  <a href="#install">Download</a> ·
  <a href="#features">Features</a> ·
  <a href="#what-is-and-is-not-verified">What's verified</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

> [!NOTE]
> **v0.1.1-beta** — verified on macOS and Windows clients, against a real Windows machine and Linux containers.
> It has not yet been run against real Linux hardware. Exactly what has and has not been checked is written down in
> [What is and is not verified](#what-is-and-is-not-verified).
> If you point this at a production server, try the irreversible actions — deleting files, killing processes — on a
> throwaway box first.
>
> The documentation is primarily maintained in Korean; this file is kept in step with it.

## It does not stream your screen

RDP, VNC and TeamViewer **stream the server's screen as video**. LiteDeck does not.

```
[GUI action]              [LiteDeck]                  [server — nothing installed]
double-click a folder ──→  SFTP ReadDir          ───→  sftp-server subsystem
restart a service     ──→  systemctl restart     ───→  systemd runs it
remove a container    ──→  docker rm -f <id>     ───→  dockerd runs it
                      ←──  structured text/JSON  ←───
rendering: 100% on your machine
```

All the server does is **run commands it already had and hand back text**. Which means:

- **The UI responds at local speed** — latency only affects how fresh the data is
- **No server load** except during the instant a command runs
- **Nothing installed, no extra ports, no relay** — port 22 and nothing else

## Five principles

1. **Zero server install** — no agent, no daemon, no package, and no files left behind
2. **SSH only** — one port. No web server, no relay
3. **Beyond files** — processes, services, containers and health, not just a file browser
4. **No account, no telemetry, open source** — nothing to sign up for, nothing collected, all source public
5. **Lightweight** — not Electron. Under 10 MB, cold start under a second

## Features

| | |
|---|---|
| **Files** | Browse, upload, download (with progress and cancel), rename, delete, permissions (checkboxes or `chmod 755` typed directly), text editing |
| **Services** | systemd units / Windows services — list, filter, start/stop/restart, set start-at-boot, **live log tailing** (Linux) |
| **Processes** | A task-manager table — sort, search, tree view, terminate (TERM then KILL), change priority |
| **Containers** | Docker and Podman cards — start/stop/restart/remove, **live log tailing**, image and volume cleanup |
| **Network** | Interfaces and listening ports — **flags which ones are reachable from outside** |
| **Scheduled jobs** | systemd timers — next and last run |
| **Terminal** | xterm.js PTY, multiple tabs |
| **Monitoring** | CPU, memory, disk summary bar with sparklines |
| **Command Log** | **Every command the GUI runs, live.** Click to copy |

### Command Log — learn the CLI from the GUI

A GUI that touches a production server is asking you to trust it. LiteDeck earns that by **showing exactly what it just ran**.

```
$ systemctl list-units --type=service --all --output=json               120ms
$ journalctl -u myapp.service -n 200 -f --no-pager -q
$ sudo -S -p '' -- systemctl restart -- myapp.service                   310ms
$ powershell -EncodedCommand ⟨utf8 prelude⟩ Restart-Service -Name 'Spooler' -Force
```

Passwords go over stdin, so **the command line is safe to display verbatim**. The log stays on your machine and is never sent anywhere.

## How it compares

| | LiteDeck | Termius | Cockpit | SSHFS/WinSCP |
|---|---|---|---|---|
| Server install | **none** | none | **required** | none |
| Extra port | **none** | none | **9090** | none |
| Files | ✅ | ✅ | ✅ | ✅ |
| Services / processes / containers | ✅ | ❌ | ✅ | ❌ |
| Account required | **no** | yes | no | no |
| Telemetry | **none** | yes | none | none |
| Price | **free, open source** | paid tiers | free | free |

## Install

Grab a build from the [releases page](https://github.com/cpprhtn/LiteDeck/releases).

| File | For |
|---|---|
| `litedeck-macos.zip` | macOS — universal, Intel and Apple Silicon |
| `litedeck.exe` | Windows 10/11 (amd64) — portable, no installer |
| `litedeck-linux-amd64.tar.gz` | Linux (amd64) |

> [!WARNING]
> **These builds are not code-signed.** Signing and notarisation both cost money, so early releases ship unsigned, and
> the SHA256 checksums published alongside them **are not a substitute for a signature.** If you need that assurance,
> [build from source](#build-from-source).

### First launch — getting past the warning

**macOS 15 (Sequoia) and later** — you will see `Apple could not verify "litedeck" is free of malware…`, and the dialog offers only "Move to Trash" and "Cancel".

Open **System Settings → Privacy & Security**, scroll down to the Security section, and click **Open Anyway** next to `"litedeck" was blocked`. Once is enough. Or from a terminal:

```bash
xattr -d com.apple.quarantine /Applications/litedeck.app
```

> The commonly cited **right-click → Open** trick was removed by Apple in macOS 15. It still works on 14 and earlier.

**Windows** — SmartScreen shows *Windows protected your PC*. Click **More info → Run anyway**. If WebView2 is missing you will be prompted to install it.

**Linux** — nothing special; unpack and make it executable.

```bash
tar xzf litedeck-linux-amd64.tar.gz && chmod +x litedeck && ./litedeck
```

## Preparing the server

**Linux** — if you can already SSH into it, you are done. There is nothing else to set up.

**Windows** — enable the OpenSSH server. From an elevated PowerShell:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service -Name sshd -StartupType Automatic
```

It does not matter whether the default shell is cmd.exe or PowerShell. LiteDeck sends commands as `-EncodedCommand`, so the shell's quoting rules never come into it.

## What is and is not verified

This section separates **what has actually been run** from what merely ought to work. "Not verified" below does not mean the code is missing — it means nobody has tried it.

### Verified

| | Where | What |
|---|---|---|
| **Client** | macOS 26.5.2 (arm64) | Development, daily use, every feature. Release binary launch confirmed |
| **Client** | Windows | Release `litedeck.exe` launch confirmed |
| **Server** | **Windows 10 Pro / PowerShell 5.1 (real hardware)** | Services, processes, network, monitoring. The only target that is not a container |
| **Server** | Ubuntu 22.04.5 / systemd 249 | Full flow — services (JSON path), files, processes, timers, log tailing, privilege escalation |
| **Server** | Ubuntu 20.04.6 / systemd 245 | Service **table-parsing fallback** (the version with no JSON output) |
| **Server** | Alpine 3.20 + OpenSSH | Transport — SFTP, reconnect, injection defence, concurrent sessions |
| **Containers** | docker 28 (dind) | Containers, images, volumes |

**Every Linux server target is a container fixture** (`testdata/`). Real Linux hardware and cloud instances have not been tried.

The Linux client is **link-verified only** — Ubuntu 22.04 resolves `libwebkit2gtk-4.0.so.37` and 24.04 resolves `libwebkit2gtk-4.1.so.0`. Nobody has opened the window.

### Not verified

| | Status |
|---|---|
| **Real Linux hardware** | Linux testing used containers only |
| **Linux client at runtime** | Links correctly; window never opened |
| **Debian, RHEL/Rocky 8** | Share the systemd 245 table-parsing path, so they should work — untested |
| **Podman** | Docker-compatible CLI, so the parser is shared — never run |
| **Windows containers** | The test machine had no Docker |
| **ProxyJump / multi-hop SSH** | Not implemented |
| **macOS and BSD servers** | No adapter. Connecting works and **files and terminal do too**; the other tabs explain themselves |
| **Remote → local drag** | Wails v2 has no drag-out API. Use the download button |

If you run a combination that is not listed, please [open an issue](https://github.com/cpprhtn/LiteDeck/issues).

### How the server OS is decided

`uname -s` first. If nothing answers, PowerShell is asked for `Win32_OperatingSystem` — a reply is itself proof of Windows, and it carries the edition name (`Windows 10 Pro`) in the same round trip.

systemd below 246 (Ubuntu 20.04, RHEL 8) has no JSON output, so LiteDeck **falls back to parsing the table** automatically. It detects the version and picks the format; there is nothing for you to configure.

Connecting to an OS with no adapter still gives you **files and a terminal** — SFTP and PTY come from SSH itself. The remaining tabs say which OS was detected and why they cannot help.

## Non-goals

- **Screen streaming** — for anything that cannot expose structured state (GUI apps, designers, debuggers) RDP is the right answer, and LiteDeck does not try to replace it
- **Configuration management** — declarative state belongs to Ansible and Terraform. LiteDeck is an imperative tool
- **Agent-based watching** — real-time watching (inotify and friends) needs something resident on the server, which breaks principle 1
- **Fleet management** — this is for two to five servers. Treating dozens as a set is a different product

## Build from source

```bash
# Needs: Go 1.25+, Node 20+, Wails v2
#
# An older Go is fine — with Go 1.21+ the toolchain fetches what it needs
# (GOTOOLCHAIN=auto, the default). Only Ubuntu 22.04's stock Go 1.18 needs
# anything done about it.
go install github.com/wailsapp/wails/v2/cmd/wails@latest

git clone https://github.com/cpprhtn/LiteDeck.git
cd LiteDeck
wails build          # output lands in build/bin/
wails dev            # hot-reload development mode
```

Linux needs the webview development headers and a C compiler, because the webview is linked through cgo:

```bash
sudo apt install build-essential pkg-config libgtk-3-dev

# the webkit package name differs by release
sudo apt install libwebkit2gtk-4.1-dev   # 24.04+  — wails build -tags webkit2_41
sudo apt install libwebkit2gtk-4.0-dev   # 22.04   — no tag needed
```

## Development

```bash
go test ./... -short          # unit tests only (no Docker)
go test ./... -race           # includes integration tests (needs Docker)
```

Integration tests bring up real servers — `testdata/` holds sshd, systemd and Docker-in-Docker fixtures. Without Docker they skip rather than fail, so **if `-race` finishes in a few seconds the integration tests did not run** (with Docker up it takes about a minute).

v0.1.1-beta was built with Go 1.26.5, Node 22.13.1, Wails 2.13.0, Docker 29.4.0 on macOS 26.5.2 arm64.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The design goal is that **supporting a new server OS means writing one adapter** — that is how Windows support landed. OpenRC (Alpine), launchd (macOS) and FreeBSD are all open.

## License

[Apache-2.0](LICENSE)
