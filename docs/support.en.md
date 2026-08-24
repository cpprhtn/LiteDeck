# What is verified, and what this is not

[← README](../README.en.md)

What was checked and where, when this is the wrong tool, and what it deliberately does not do.

This section separates **what has actually been run** from what merely ought to work. "Not verified" below does not mean the code is missing. It means nobody has tried it.

## Verified

| | Where | What |
|---|---|---|
| **Client** | macOS 26.5.2 (arm64) | Development, daily use, every feature. Release binary launch confirmed |
| **Client** | Windows | Release `litedeck.exe` launch confirmed |
| **Client** | **Ubuntu 24.04.3 (Proxmox VM)** | Release `litedeck-linux-amd64.tar.gz` launch confirmed: the window opens, connects to a real server, and shows monitoring and a file listing. No keychain there, so the **credential-storage fallback** was exercised too |
| **Server** | **Windows 10 Pro / PowerShell 5.1 (a real SSH server)** | Services, processes, network, monitoring. Not a container |
| **Server** | **Ubuntu 24.04.4 (a real SSH server)** | The whole read side (metrics, services and failed detection, containers, exposed ports, journal) plus reading, writing and deleting files, including the permission-denied path |
| **Server** | Ubuntu 22.04.5 / systemd 249 | Full flow: services (JSON path), files, processes, timers, log tailing, privilege escalation |
| **Server** | Ubuntu 20.04.6 / systemd 245 | Service **table-parsing fallback** (the version with no JSON output) |
| **Server** | Alpine 3.20 + OpenSSH | Transport: SFTP, reconnect, injection defence, concurrent sessions |
| **Jumping** | Two Alpine containers (bastion + target) | ProxyJump, with the target publishing no port so it is **genuinely unreachable directly** |
| **Containers** | docker 28 (dind) | Containers, images, volumes |
| **Compose** | docker 28 (dind) + Compose v2.40.3 | Reading the project labels, and starting/stopping/restarting per service and per project — including after the compose file was deleted |

**The one real Linux SSH server is the row above.** An Ubuntu 24.04.4 server was driven through the
MCP integration, covering the read side and reading, writing and deleting files. **The paths the
GUI writes through** — file transfers, completing a sudo escalation, the terminal PTY, live log
tailing — **are still container fixtures only** (`testdata/`).

The Linux client has been **opened, connected and read from**. Transfers, the terminal and the GTK
file chooser have not.

> [!IMPORTANT]
> **The released Linux binary links against `libwebkit2gtk-4.1`**, because the CI runner is Ubuntu
> 24.04 and that is the package it has. **It will not start on 22.04** — on a distribution that
> only ships 4.0, [build from source](building.en.md).

## Not verified

| | Status |
|---|---|
| **Write paths on real Linux hardware** | Transfers (whole folders and resuming included), completing a sudo escalation, the terminal PTY and log tailing were only exercised in containers |
| **The rest of the Linux client** | Window, connecting and the read side are confirmed. Transfers, the terminal, the GTK file chooser and keychain storage are not |
| **Debian, RHEL/Rocky 8** | Share the systemd 245 table-parsing path, so they should work, but untested |
| **Podman** | Docker-compatible CLI, so the parser is shared, but never run |
| **Windows containers** | The test machine had no Docker |
| **Directory download on the Windows client** | **The vulnerability was reproduced on real hardware (the 1.5.0 release `litedeck.exe`).** A backslash is an ordinary character in a Linux filename and a path separator on Windows, so a name the server sends can point outside the download folder. Downloading a folder into `Downloads\demo`, a file named `..\..\PWNED.txt` **landed in the home folder (`C:\Users\<user>`)** — two levels above the chosen folder. Same class as scp's CVE-2019-6111. **The guard (`safeLocalName`) is in and is tested platform-independently.** 1.5.2 has the identical code and is equally affected. **Still not done**: running the fixed build on a real Windows client to confirm the guard blocks it there too — the reproduction was on the vulnerable version. Transfers, the terminal and keychain storage on the Windows client are also still only launch-confirmed |
| **OOM and core-dump events** | Classification goes by systemd's `MESSAGE_ID` and those IDs were checked against the catalogue, but **no real OOM or core dump has ever been seen through this.** A container cannot produce one — the kernel's OOM message goes to the host's journal |
| **The monitoring tab on Windows** | CPU percentage, memory, disk space and uptime, and no further. The **CPU split, per-core view, memory breakdown, network counters, disk I/O, PSI, runnable and blocked counts, open descriptors and inodes** all read `/proc` or `df -i`, which Windows does not have. The system panel's **hostname, CPU model, kernel release and timezone** are not filled in yet either. The events pane does not appear at all — the Windows event log sits where journald does, but nothing reads it |
| **More than one NVIDIA card** | Confirmed on real hardware with a single card. The multi-card path is covered by tests for the parser and the sample boundary, but has not run against a machine with several |
| **Compose v1 and `podman compose`** | Only the Compose v2 plugin was tried. The standalone `docker-compose` (v1) cannot address a project without its file and is not supported. `podman compose` takes the same code path but was never run |
| **ProxyJump against a real bastion** | Verified against two containers (bastion + target). Never tried against an actual bastion. Multi-hop is not implemented and is refused |
| **macOS and BSD servers** | No adapter. Connecting works and **files and terminal do too**; the other tabs explain themselves |
| **Remote → local drag** | Wails v2 has no drag-out API. Use the download button |

If you run a combination that is not listed, please [open an issue](https://github.com/cpprhtn/LiteDeck/issues).

## How the server OS is decided

`uname -s` first. If nothing answers, PowerShell is asked for `Win32_OperatingSystem`. A reply is itself proof of Windows, and it carries the edition name (`Windows 10 Pro`) in the same round trip.

systemd below 246 (Ubuntu 20.04, RHEL 8) has no JSON output, so LiteDeck **falls back to parsing the table** automatically. It detects the version and picks the format; there is nothing for you to configure.

Connecting to an OS with no adapter still gives you **files and a terminal**: SFTP and PTY come from SSH itself. The remaining tabs say which OS was detected and why they cannot help.

## When it is not

Stated plainly. If any of these is what you need, something else is better:

| What you need | The right tool |
|---|---|
| Dozens or hundreds of servers at once | Ansible, Salt, other configuration management |
| Declarative state | Terraform, Ansible |
| A real remote development environment (LSP, debugger, refactoring) | VS Code Remote-SSH |
| The screen of a GUI application | RDP, VNC |
| Protocols other than SSH (RDP, VNC, Kubernetes, Proxmox) | A multi-protocol connection manager |
| File transfer and nothing else | WinSCP, an SFTP client, `rsync` |

LiteDeck is for **a few servers reachable over SSH, with nothing installed on them, and with what
it runs visible while it runs**. The tools above do the rest better.

## Non-goals

- **Screen streaming.** For anything that cannot expose structured state (GUI apps, designers, debuggers) RDP is the right answer, and LiteDeck does not try to replace it
- **Configuration management.** Declarative state belongs to Ansible and Terraform. LiteDeck is an imperative tool
- **Agent-based watching.** Real-time watching (inotify and friends) needs something resident on the server, which breaks principle 1
- **Fleet management.** This is for two to five servers. Treating dozens as a set is a different product
