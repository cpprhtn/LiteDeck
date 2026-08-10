<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
    <img src="docs/logo.png" width="360" alt="LiteDeck">
  </picture>
</p>

<h1 align="center">LiteDeck</h1>

<p align="center">
  <b>Manage a remote server from a local native GUI over SSH alone, with nothing installed on the server.</b>
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

<p align="center">
  <img src="docs/media/01-tour.gif" width="880"
       alt="LiteDeck: files, services, processes, containers and network in one window">
</p>

<p align="center">
  <sub>One connection covers <b>files, services, processes, containers and the network</b>. Nothing was installed on the server.</sub>
</p>

---

> [!NOTE]
> **v1.0.0.** Verified on macOS and Windows clients, against **real Windows and Linux machines**.
> On Linux hardware that covers the read side and file writes; transfers and privilege escalation are
> still container-only. Exactly what has and has not been checked is written down in
> [What is and is not verified](#what-is-and-is-not-verified).
> If you point this at a production server, try the irreversible actions — deleting files, killing processes — on a
> throwaway box first.
>
> The documentation is primarily maintained in Korean; this file is kept in step with it.

## It does not stream your screen

RDP, VNC and TeamViewer **stream the server's screen as video**. LiteDeck does not.

```
[GUI action]              [LiteDeck]                  [server · nothing installed]
double-click a folder ──→  SFTP ReadDir          ───→  sftp-server subsystem
restart a service     ──→  systemctl restart     ───→  systemd runs it
remove a container    ──→  docker rm -f <id>     ───→  dockerd runs it
                      ←──  structured text/JSON  ←───
rendering: 100% on your machine
```

All the server does is **run commands it already had and hand back text**. Which means:

- **The UI responds at local speed.** Latency only affects how fresh the data is
- **No server load** except during the instant a command runs
- **Nothing installed, no extra ports, no relay.** Port 22 and nothing else

## Five principles

1. **Zero server install.** No agent, no daemon, no package. What is left on the server is whatever you asked it to do[^tmp]
2. **SSH only.** One port. No web server, no relay
3. **Beyond files.** Processes, services, containers and health, not just a file browser
4. **No account, no telemetry, open source.** Nothing to sign up for, nothing collected, all source public
5. **Lightweight.** Not Electron. A 5–10 MB download, 13–16 MB installed, cold start under a second

[^tmp]: To be exact: saving from the editor writes a temp file in the same directory and swaps it
    in with `rename`, so an interrupted save cannot leave the original half-written. On success
    nothing is left behind. If the `rename` fails, the temp file's path is shown on screen and the
    file is deliberately not deleted, which beats losing the edit.

## Features

| | |
|---|---|
| **Files** | Browse as a tree, upload, download (with progress and cancel), rename, delete, permissions (checkboxes or `chmod 755` typed directly) |
| **Code editing** | Split view beside the tree, file tabs, syntax highlighting for 24 languages, find/replace, **a diff before every save**, **atomic saves** (temp file + rename) |
| **Services** | systemd units / Windows services. List, filter, start/stop/restart, set start-at-boot, **live log tailing** (Linux) |
| **Processes** | A task-manager table. Sort, search, tree view, terminate (TERM then KILL), change priority |
| **Containers** | Docker and Podman cards. Start/stop/restart/remove, **live log tailing**, image and volume cleanup |
| **Network** | Interfaces and listening ports. **Flags which ones are reachable from outside** |
| **Scheduled jobs** | systemd timers. Next and last run |
| **Terminal** | xterm.js PTY, multiple tabs. `code .` and `vi foo.conf` are **caught by the app** and open in the file tab. They are never sent to the server, so neither VS Code nor vi needs to exist there |
| **Monitoring** | CPU, memory, disk summary bar with sparklines |
| **Command Log** | **Every command the GUI runs, live.** Click to copy |
| **MCP** | Claude Code and Claude Desktop read and change your servers through this app. Per-server opt-in, changes are approved, **and can be undone** |
| **Language** | English and Korean. Follows your OS on first run; switch with `KO`/`EN` at the bottom of the sidebar |

### Command Log: learn the CLI from the GUI

<p align="center">
  <img src="docs/media/03-command-log.gif" width="820" alt="Command Log">
</p>

<p align="center"><sub>A restart is refused for want of privileges, so the app <b>asks</b>. The command it ran stays visible; the password went over stdin and does not.</sub></p>


A GUI that touches a production server is asking you to trust it. LiteDeck earns that by **showing exactly what it just ran**.

```
$ systemctl list-units --type=service --all --output=json               120ms
$ journalctl -u myapp.service -n 200 -f --no-pager -q
$ sudo -S -p '' -- systemctl restart -- myapp.service                   310ms
$ powershell -EncodedCommand ⟨utf8 prelude⟩ Restart-Service -Name 'Spooler' -Force
```

Passwords go over stdin, so **the command line is safe to display verbatim**. The log stays on your machine and is never sent anywhere.

### The editor: nothing installed on either end

<p align="center">
  <img src="docs/media/02-terminal-jump.gif" width="820" alt="code . and vi in the terminal">
</p>

<p align="center"><sub>Typing <code>code .</code> is caught <b>before the line reaches the server</b> and opens the file tab instead. <code>vi</code> on a new path opens an editor tab. Neither VS Code nor vi needs to exist on the server.</sub></p>


Editing a remote file usually means installing something. Either `vscode-server` goes on the
server (hundreds of MB), or you live in the server's `vi` through a terminal. LiteDeck does
**neither.**

- **On the server.** Nothing is needed. Files are read and written over SFTP, which SSH
  already provides. Whether the server has an editor at all makes no difference
- **On your machine.** You do not need VS Code. The editor ships inside the app (CodeMirror,
  24 language modes) and loads when you open the first file, so browsing a directory and
  quitting costs nothing

Typing `code .` or `vi foo.conf` in the terminal is **caught by the app before the line reaches
the server**, and the file tab opens instead. The server never learns this feature exists. So
neither VS Code nor vi has to be installed there, and equally **nothing opens on the server side.**

Saving shows a diff against the server's current copy first, then writes to a temp file and swaps
it in with `rename`, so an interrupted save cannot leave the original half-written.

> For contrast: VS Code Remote-SSH installs a server on your server. `vscode-server` eating
> memory on a small VPS is a well-known problem. If you want a real remote IDE, that is the right
> tool. **If you do not want to upload hundreds of megabytes to change one config file**, this is.

## When this is the right tool

- You look after **a handful of servers**, not a fleet. You read logs, restart services and fix
  config files on them
- You do not want to **install anything else** on those servers. SSH is already open; you would
  like that to be enough
- You want a GUI, but you want to **see what it ran**
- You are not giving up your terminal. You just want the frequent things to be a click

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

## Install

Grab a build from the [releases page](https://github.com/cpprhtn/LiteDeck/releases).

| File | For |
|---|---|
| `litedeck-macos.zip` | macOS (universal, Intel and Apple Silicon) |
| `litedeck-windows-amd64.zip` | Windows 10/11 (amd64). Unzip to a single `litedeck.exe`, no installer |
| `litedeck-linux-amd64.tar.gz` | Linux (amd64) |

> [!WARNING]
> **These builds are not code-signed.** Signing and notarisation both cost money, so early releases ship unsigned, and
> the SHA256 checksums published alongside them **are not a substitute for a signature.** If you need that assurance,
> [build from source](#build-from-source).

### First launch: getting past the warning

**macOS 15 (Sequoia) and later.** You will see `Apple could not verify "litedeck" is free of malware…`, and the dialog offers only "Move to Trash" and "Cancel".

Open **System Settings → Privacy & Security**, scroll down to the Security section, and click **Open Anyway** next to `"litedeck" was blocked`. Once is enough. Or from a terminal:

```bash
xattr -d com.apple.quarantine /Applications/litedeck.app
```

> The commonly cited **right-click → Open** trick was removed by Apple in macOS 15. It still works on 14 and earlier.

**Windows.** SmartScreen shows *Windows protected your PC*. Click **More info → Run anyway**. If WebView2 is missing you will be prompted to install it.

**Defender may delete the file.** An unsigned new executable has no reputation, so it is sometimes quarantined on download with no prompt. That is the absence of a signature, not a detection. Signing and notarisation cost money and early releases go out unsigned.

If it disappears, **Windows Security → Virus & threat protection → Protection history** has the entry; **Actions → Allow** restores it. To pre-empt it, add the unzipped folder under **Manage settings → Add or remove exclusions**.

**Linux.** Nothing special: unpack and make it executable.

```bash
tar xzf litedeck-linux-amd64.tar.gz && chmod +x litedeck && ./litedeck
```

## Preparing the server

**Linux.** If you can already SSH into it, you are done. There is nothing else to set up.

**Windows.** Enable the OpenSSH server. From an elevated PowerShell:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service -Name sshd -StartupType Automatic
```

It does not matter whether the default shell is cmd.exe or PowerShell. LiteDeck sends commands as `-EncodedCommand`, so the shell's quoting rules never come into it.

**To reach a machine at home from outside**, a mesh VPN like Tailscale beats opening a port on
your router. Setup, and how to do it without an account (Headscale, WireGuard), are in
[docs/remote-access.en.md](docs/remote-access.en.md).

## Security

This is a tool that reaches production servers over SSH, so the following is not design
intent. It is **what the code does today**, with the file to check it against.

### Host key verification

**Yes.** Keys are verified against an OpenSSH-format `known_hosts`
([`internal/sshcore/hostkey.go`](internal/sshcore/hostkey.go)).

- On first contact you get the address, the key type and the **SHA256 fingerprint**, and you choose:
  reject / trust once / trust always. Only "always" writes to the file
- **A key that contradicts the recorded one drops the connection.** You get a full-screen warning,
  and **there is no option anywhere in the code to continue past it**, and you are not even asked.
  That is the signature of a man-in-the-middle
- Permissions are `0700` on the directory, `0600` on the file

> [!IMPORTANT]
> **LiteDeck does not use `~/.ssh/known_hosts`.** It keeps its own file at
> `<OS config dir>/litedeck/known_hosts`. A host you have already reached with `ssh` will therefore
> still prompt once here. The format is identical, so copying the existing line across skips it.

### Authentication

ssh-agent, private key file (with passphrase), password, and keyboard-interactive for 2FA and OTP.
**The order tried is configured per host.**

- **Put ssh-agent first if you can.** The key never enters this process
- OTP and 2FA answers are **never stored.** A one-time code is worthless the second time

### Credential storage

If a secret is stored, it goes **only into the OS credential store**: macOS Keychain, Windows
Credential Manager, Linux Secret Service ([`internal/secret/secret.go`](internal/secret/secret.go)).

- **LiteDeck never writes a secret to a file of its own.** Where no credential store is reachable
  the answer is not a weaker file format but **not storing it**. You are asked every time, and the
  "remember this" checkbox is not shown at all rather than promising something it cannot keep
- Storing is opt-in at each prompt. Every host has a **forget stored passwords** action
- If a stored sudo password stops working because it changed on the server, **it deletes itself.**
  Otherwise the keychain keeps answering before the dialog appears and you can never correct it
- `hosts.json` holds addresses, usernames and **paths** to key files. No secrets

### sudo

**Nothing is escalated on your behalf** ([`internal/app/sudo.go`](internal/app/sudo.go)). Commands
run as the user you logged in as. If the server refuses, the UI **asks** whether to retry as
administrator and you press the button. Silently prefixing `sudo` would make the Command Log stop
matching what you believe you asked for, and that log is the only reason to trust this app.

- The password travels **on stdin only**: `sudo -S -p '' -- <cmd>`. In argv it would be visible in
  the remote process table to every other user on that machine, and would appear verbatim in the
  Command Log. **That is why the Command Log is safe to display in full**
- NOPASSWD is detected with `sudo -n true`, and then nothing is asked. Prompting for a password the
  server does not want is not just pointless. **It trains you to type your password into any dialog**

### The MCP endpoint

Turning on [MCP integration](#mcp-integration) opens one local HTTP endpoint. That single endpoint
**speaks for every server you have shared**, so what is written here decides whether the feature is
safe ([`internal/mcp/http.go`](internal/mcp/http.go)).

- **Bound to `127.0.0.1` only.** There is no setting to expose it elsewhere. The absence of that
  option is the only thing making this endpoint safe
- **Bearer token**, stored in settings, compared in constant time, and changed **only by the rotate
  button**. Rotating on the app's schedule would break the client config you pasted in, at a moment
  you did not choose
- **Origin is checked.** A browser page can POST to loopback, and DNS rebinding can make that look
  same-origin. The MCP spec asks for this; a real MCP client sends no Origin at all
- **Rate limited** to 1.5 calls a second, burst of 8. Not politeness: the Exec pool is three
  channels wide, so an agent burst does not slow the server down, it **starves the GUI you are
  looking at**
- **Per-server opt-in, and deleting files is a separate switch.** Registering a host, or sharing it
  to be read, does not grant deletion
- **The approval policy belongs to the app.** No tool changes it and no parameter relaxes it, so
  **a model has no way to request its own approval.** That the protocol carries no trustworthy
  statement of a client's intent was verified against Claude Code 2.1.22
- **Undo copies live on this machine** and clear after 24 hours. Nothing is left on the server

**Stated plainly:**

- **With "stop asking" on, prompt injection wins.** Text planted in a log or a file can steer the
  model, and in that mode nobody sees it in time. What remains is not defence but **attribution and
  blast radius**: everything in the Command Log, the mode expiring on its own, and the fact that it
  is set per host
- **Only files can be undone.** A service restart or a killed process has no copy behind it. Those
  are the recoverable kind, which is why they are offered; the unrecoverable ones (arbitrary command
  execution, recursive directory deletion, removing containers or images) are not offered at all
- **The MCP layer never touches credentials.** The token is for this endpoint only, and SSH
  credentials stay in the OS keychain as described above

### What it does not do, stated up front

- **It cannot bound how long a password stays in memory.** Go strings are immutable and the runtime
  may copy them at any time, so a secret held as a string cannot be erased on demand;
  `x/crypto/ssh` takes passwords as strings too. It becomes garbage immediately and is freed at the
  next collection, but **there is no zero-on-use implemented.** A core or heap dump could show it
- **Release binaries are unsigned.** A SHA256 checksum is not a signature
- **On real Linux hardware only the read side and file writes have been exercised**; transfers, completing a sudo escalation, the terminal PTY and log tailing are still container-only. See
  [what is and is not verified](#what-is-and-is-not-verified)
- There is no audit log. The Command Log stays on your machine and goes nowhere.
  **A log the client writes is not an audit**
- Dependency vulnerabilities are checked by `govulncheck ./...` in CI, on pushes to `main` and every PR

Found a vulnerability? Please email **cpprhtn@naver.com** rather than opening a public issue.

## What is and is not verified

This section separates **what has actually been run** from what merely ought to work. "Not verified" below does not mean the code is missing. It means nobody has tried it.

### Verified

| | Where | What |
|---|---|---|
| **Client** | macOS 26.5.2 (arm64) | Development, daily use, every feature. Release binary launch confirmed |
| **Client** | Windows | Release `litedeck.exe` launch confirmed |
| **Server** | **Windows 10 Pro / PowerShell 5.1 (real hardware)** | Services, processes, network, monitoring. The only target that is not a container |
| **Server** | **Ubuntu 24.04.4 (real machine)** | The whole read side (metrics, services and failed detection, containers, exposed ports, journal) plus reading, writing and deleting files, including the permission-denied path |
| **Server** | Ubuntu 22.04.5 / systemd 249 | Full flow: services (JSON path), files, processes, timers, log tailing, privilege escalation |
| **Server** | Ubuntu 20.04.6 / systemd 245 | Service **table-parsing fallback** (the version with no JSON output) |
| **Server** | Alpine 3.20 + OpenSSH | Transport: SFTP, reconnect, injection defence, concurrent sessions |
| **Containers** | docker 28 (dind) | Containers, images, volumes |

**The one real Linux machine is the row above.** An Ubuntu 24.04.4 server was driven through the
MCP integration, covering the read side and reading, writing and deleting files. **The paths the
GUI writes through** — file transfers, completing a sudo escalation, the terminal PTY, live log
tailing — **are still container fixtures only** (`testdata/`).

The Linux client is **link-verified only**. Ubuntu 22.04 resolves `libwebkit2gtk-4.0.so.37` and 24.04 resolves `libwebkit2gtk-4.1.so.0`. Nobody has opened the window.

### Not verified

| | Status |
|---|---|
| **Write paths on real Linux hardware** | Transfers, completing a sudo escalation, the terminal PTY and log tailing were only exercised in containers |
| **Linux client at runtime** | Links correctly; window never opened |
| **Debian, RHEL/Rocky 8** | Share the systemd 245 table-parsing path, so they should work, but untested |
| **Podman** | Docker-compatible CLI, so the parser is shared, but never run |
| **Windows containers** | The test machine had no Docker |
| **ProxyJump / multi-hop SSH** | Not implemented |
| **macOS and BSD servers** | No adapter. Connecting works and **files and terminal do too**; the other tabs explain themselves |
| **Remote → local drag** | Wails v2 has no drag-out API. Use the download button |

If you run a combination that is not listed, please [open an issue](https://github.com/cpprhtn/LiteDeck/issues).

### How the server OS is decided

`uname -s` first. If nothing answers, PowerShell is asked for `Win32_OperatingSystem`. A reply is itself proof of Windows, and it carries the edition name (`Windows 10 Pro`) in the same round trip.

systemd below 246 (Ubuntu 20.04, RHEL 8) has no JSON output, so LiteDeck **falls back to parsing the table** automatically. It detects the version and picks the format; there is nothing for you to configure.

Connecting to an OS with no adapter still gives you **files and a terminal**: SFTP and PTY come from SSH itself. The remaining tabs say which OS was detected and why they cannot help.

## MCP integration

<p align="center">
  <img src="docs/media/04-mcp-approval.gif" width="820" alt="MCP approval">
</p>

<p align="center"><sub>An MCP client tries to change a file and the <b>diff against what is on the server</b> comes up. Anything approved can be undone.</sub></p>


An MCP client (Claude Code, Claude Desktop) can **read and change** your servers through this
app. Turn it on with the **MCP** button at the bottom of the sidebar.

> [!IMPORTANT]
> **Changes are confirmed one at a time by default**, and the dialog shows the literal
> command or file diff. That policy is **owned by the app; the AI cannot turn it off.**

```
Claude Code  ──MCP (local HTTP)──▶  LiteDeck  ──existing SSH──▶  server
```

The client **sits where the GUI sat**: same adapters, same already-authenticated SSH connection,
same Command Log. What it asks for scrolls past tagged `MCP`. And **still nothing is installed
on the server.** Putting an AI tool there means a runtime and a resident process, and its
context-gathering hammers a small box's I/O. All of that load stays on the client.

**Twelve read tools**: `hosts_list`, `health_snapshot`, `sys_stats`, `svc_list`, **`svc_logs`**,
`proc_list`, `container_list`, **`container_logs`**, `net_ports`, `fs_list`, `fs_read`,
`sessions_list`. One `health_snapshot` returns CPU, memory, disk, failed units, unhealthy
containers and exposed ports; `svc_logs` is what says *why* something died.

**Five write tools**: `svc_control` (start/stop/restart), `container_control`, `proc_signal`
(TERM/KILL), `fs_write` and `fs_delete`.

**They can be undone.** Before MCP overwrites or deletes a file, the previous contents are kept
**on this machine**, and the **Changed files** tab restores them one at a time. When you have told
it to stop asking and walked away, this is what you have instead of prevention. **Copies clear
themselves after 24 hours**: this is a guard for one night, not an archive. Nothing is left on the
server, and `fs_delete` refuses outright when no copy can be made (binary, or too large).

**Deleting is enabled per server**, separately from sharing and from the approval mode: whether the
tool exists and whether using it interrupts you are different questions.

**Deliberately absent**: arbitrary command execution, recursive directory deletion, and removing
containers or images. An arbitrary-command tool makes the per-tool allowlist decorative; the
other three cannot be copied first, so nothing could put them back.

**What holds it back**

| | |
|---|---|
| Per-server opt-in | **Everything off by default.** Adding a host does not expose it; only what you switch on can be read, and deleting files is a separate switch again |
| Change approval | **Only file changes are confirmed** by default, because the dialog shows a diff against what is on the server right now, which is information no client has. A restart just runs; the client already showed you the same thing |
| Per host | A badge in the header: **ask always / files only / don't ask overnight**. While it is not asking the badge stays red, and the window **reverts on its own** |
| Not settable remotely | No tool flips that switch and no parameter relaxes it. **A model has no way to request its own approval** |
| Reading ≠ writing | Sharing a server to be read does not make it changeable |
| Binding | `127.0.0.1` only. No setting exposes it on another interface |
| Auth | Bearer token, stored in settings, changed only by the rotate button |
| Rate limit | 1.5 calls/sec, burst of 8, so an agent loop cannot hammer a small server |
| Audit | Every tool call lands in the Command Log. Local only, sent nowhere |

**Connecting.** Press **Copy** on the MCP panel's **Connection** tab and paste:

```bash
claude mcp add --transport http litedeck http://127.0.0.1:<port>/mcp \
  --header "Authorization: Bearer <token>"
```

> [!NOTE]
> **Verification status.** Confirmed end to end from Claude Code 2.1.22 against a **real
> Ubuntu 24.04 server**. Asked *"how is the server doing"*, the model calls `health_snapshot`
> by itself and comes back with the metrics, the failed unit, the stopped containers and the
> exposed ports. Writes do raise the approval dialog; approving sends the command through to
> the server, and a write nobody answers does not run.
> MCP against a **Windows** server has not been tried yet.

## Non-goals

- **Screen streaming.** For anything that cannot expose structured state (GUI apps, designers, debuggers) RDP is the right answer, and LiteDeck does not try to replace it
- **Configuration management.** Declarative state belongs to Ansible and Terraform. LiteDeck is an imperative tool
- **Agent-based watching.** Real-time watching (inotify and friends) needs something resident on the server, which breaks principle 1
- **Fleet management.** This is for two to five servers. Treating dozens as a set is a different product

## Build from source

```bash
# Needs: Go 1.25+, Node 20+, Wails v2
#
# An older Go is fine. Go 1.21+ means the toolchain fetches what it needs
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
sudo apt install libwebkit2gtk-4.1-dev   # 24.04+  (wails build -tags webkit2_41)
sudo apt install libwebkit2gtk-4.0-dev   # 22.04   (no tag needed)
```

## Development

```bash
go test ./... -short          # unit tests only (no Docker)
go test ./... -race           # includes integration tests (needs Docker)
```

Integration tests bring up real servers. `testdata/` holds sshd, systemd and Docker-in-Docker fixtures. Without Docker they skip rather than fail, so **if `-race` finishes in a few seconds the integration tests did not run** (with Docker up it takes about a minute).

v1.0.0 was built with Go 1.26.5, Node 22.13.1, Wails 2.13.0, Docker 29.4.0 on macOS 26.5.2 arm64.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The design goal is that **supporting a new server OS means writing one adapter**, which is how Windows support landed. OpenRC (Alpine), launchd (macOS) and FreeBSD are all open.

## License

[Apache-2.0](LICENSE)
