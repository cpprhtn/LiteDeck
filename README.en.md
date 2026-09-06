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
  <a href="docs/features.en.md">Features</a> ·
  <a href="docs/security.en.md">Security</a> ·
  <a href="docs/mcp.en.md">MCP</a> ·
  <a href="docs/support.en.md">What's verified</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

<p align="center">
  <img src="docs/media/01-tour.gif" width="880"
       alt="LiteDeck: files, editor, services, processes, containers, network, sessions, monitoring and terminal in one window">
</p>

<p align="center">
  <sub>One connection covers <b>files, the editor, services, processes, containers, the network, sessions and a terminal</b>.<br>
  Nothing was installed on the server.</sub>
</p>

---

> [!NOTE]
> **Runs on macOS, Windows and Ubuntu** — all three confirmed by opening it there. What was tested
> where is written down in [What is and is not verified](docs/support.en.md).
> Try the irreversible actions — deleting files, killing processes — on a throwaway server first.
>
> The documentation is primarily maintained in Korean; this file is kept in step with it.

## Five principles

1. **Zero server install.** No agent, no daemon, no package. What is left on the server is whatever you asked it to do
2. **SSH only.** The SSH port you already have open. No web server, no relay
3. **Nothing hidden.** Every command the GUI runs shows up verbatim in the Command Log. sudo is never added behind your back — it asks
4. **No account, no telemetry, open source.** Nothing to sign up for, nothing collected, all source public
5. **Lightweight.** Not Electron. A 5–10 MB download, 13–16 MB installed, cold start under a second

> One exception to the first. Saving from the editor writes a temp file in the same directory and
> swaps it in with `rename`, so an interrupted save cannot leave the original half-written. On
> success nothing is left behind. If the `rename` fails, the temp file's path is shown on screen and
> the file is deliberately not deleted, which beats losing the edit.

## The monitoring tab <sub>v1.5.0</sub>

<p align="center">
  <img src="docs/media/06-monitoring.png" width="880" alt="Monitoring tab: CPU breakdown, per-core, memory, GPU, network, disk I/O, system facts, filesystems">
</p>

When the summary bar says **CPU 40%**, this tab says **what the 40% is**.

- **CPU split into user, kernel, IO wait and steal.** 90% that is all IO wait is not short of CPU, it is waiting for a disk; all steal is not busy at all, its hypervisor is handing the time to somebody else. Before they are separated, all three read as "busy"
- **A core die.** Thirty-two cores at "40%" is either every core half busy or **one pinned and the rest idle**, and the second is what a single-threaded bottleneck looks like
- **Inodes.** A disk with room that cannot create a file — and every tool then says `no space left on device`, the same words as running out of bytes
- **Network errors and drops**, **disk I/O**, **PSI** (how long things waited, rather than how much was used), runnable and blocked counts, open descriptors
- **NVIDIA cards** add utilisation, fan, temperature and VRAM

Trends are drawn over real time for as long as the app was watching, and **stretches it did not see are drawn as breaks.** It does not join up a reading nobody took.

All of it read from `/proc` and `df`. **There is still nothing installed on the server.**

> The screenshot is a demo container ([`testdata/demo`](testdata/demo)). The GPU in it is a stand-in; every other figure came out of that container.

## Command history <sub>v1.7.0</sub>

**"I worked on that server three months ago. What did I do?"**

A collapsible panel beside the terminal tab. The question is always two questions —
*which directory was I working in*, and *what did I run there* — so the top is a
directory tree and the bottom is what ran in the directory you picked. It opens
wherever the terminal is standing.

Three sources, **kept apart and labelled** rather than merged:

| | |
|---|---|
| **sudo's journal** | What was run with elevated rights. sudo **records the working directory** at the moment the command runs, so the path here is a fact rather than a guess |
| **This app's terminal** | What you typed here. Kept on this machine, not the server, so nothing is appended to anybody's history file |
| **The shell's history file** | `~/.bash_history`, `~/.zsh_history`. Most of it is here |

A history file knows **what** was typed and not **where**. Replaying `cd` recovers the
path, and a line the replay could not follow **says on screen that it is an estimate**.
bash also writes no timestamps at all unless `HISTTIMEFORMAT` is set, so entries keep
the order the file gave them — **an absent time is not invented**.

> **Reading the shell history is enabled per server, and off by default.** It is the
> densest credential file on most machines, and a feature that reads it every time a tab
> opens changes what that means. Anything that looks like a password is **masked in Go**
> before it crosses, and the panel says how many it masked. `/root/.bash_history` is read
> **only when you elevate on purpose**.

**Also new, for the time you were not looking:** a digest of what changed since you last
looked, successful and failed logins (thousands a day, so a summary rather than a list),
and whether security updates or a reboot are pending. → [Features in detail](docs/features.en.md)

## Claude works your servers through this app

MCP clients like Claude Code and Claude Desktop **sit where the GUI sat**: the same adapter, the same
already-authenticated SSH connection, the same Command Log. They get 12 read tools and 6 write tools,
and **file changes are held for approval by default** — the dialog shows a diff against what is on the
server right now, which is information no client has. Per host you can raise that to confirming
everything. The policy is owned by the app and cannot be relaxed from the client side. Files MCP
changed can be rolled back, and nothing is installed on the server.

**Running commands can be granted too** <sub>v1.7.0</sub> — anything the GUI cannot express
eventually needs it. Like deleting, it is **enabled per server and off by default**. Once on it
follows the same approval policy as every other write tool: if you mean to let it drive, the
commands travel with everything else. Singling them out for a permanent prompt would empty the
relaxed modes of meaning.

```bash
claude mcp add --transport http litedeck http://127.0.0.1:<port>/mcp \
  --header "Authorization: Bearer <token>"
```

→ [MCP integration](docs/mcp.en.md)

## Install

Grab a build from the [releases page](https://github.com/cpprhtn/LiteDeck/releases).

| File | For |
|---|---|
| `litedeck-desktop-macos.zip` | macOS (universal, Intel and Apple Silicon) |
| `litedeck-desktop-windows-amd64.zip` | Windows 10/11 (amd64). Unzip to a single `litedeck.exe`, no installer |
| `litedeck-desktop-linux-amd64.tar.gz` | Linux (amd64). **Ubuntu 24.04 or newer** — it needs `libwebkit2gtk-4.1`. On 22.04, [build from source](docs/building.en.md) |

> [!WARNING]
> **These builds are not code-signed.** Signing and notarisation both cost money, so early releases ship unsigned, and
> the SHA256 checksums published alongside them **are not a substitute for a signature.** If you need that assurance,
> [build from source](docs/building.en.md).
>
> That is also why the first launch trips macOS Gatekeeper and Windows SmartScreen. How to get past
> both is in [Install and server setup](docs/install.en.md).

**Use it from a browser — server mode.** There is also a headless build you put on one server and
open in a browser instead of running the desktop app — `litedeck-server-linux-amd64.tar.gz` ·
`litedeck-server-linux-arm64.tar.gz`. Like Grafana, you just open a URL. Running it, exposing it and
login are covered in [server mode](docs/server-mode.en.md).

**On the server side.** Linux needs nothing if you can already SSH into it. Windows needs the OpenSSH
server switched on. To reach a machine at home from outside, a mesh VPN beats opening a port on your
router — [preparing the server](docs/install.en.md#preparing-the-server) ·
[reaching it over Tailscale](docs/remote-access.en.md)

## When this is the right tool

- You look after **a handful of servers**, not a fleet. You read logs, restart services and fix
  config files on them
- You do not want to **install anything else** on those servers. SSH is already open; you would
  like that to be enough
- You want a GUI, but you want to **see what it ran**
- You are not giving up your terminal. You just want the frequent things to be a click

If you need dozens of servers at once, declarative state, or a real remote development environment,
something else is better. Which cases those are is written down in
[when it is not](docs/support.en.md#when-it-is-not).

## Further reading

| | |
|---|---|
| [Security](docs/security.en.md) | Host key verification, authentication, credential storage, sudo, the MCP endpoint. **What it cannot do comes first** |
| [What is and is not verified](docs/support.en.md) | What was checked where, and what was not. Plus when this is not the right tool, and the non-goals |
| [MCP integration](docs/mcp.en.md) | 18 tools, the approval policy, undo, the safeguards |
| [Features in detail](docs/features.en.md) | **The full feature list**, plus the Command Log, the editor, transfers, Compose, the sshd check and ProxyJump |
| [Install and server setup](docs/install.en.md) | Getting past the first-launch warning, enabling OpenSSH on Windows |
| [Reaching a machine over Tailscale](docs/remote-access.en.md) | Your home machine without port forwarding. Tailscale SSH, MCP, subnet routers, and doing it without an account |
| [Build from source](docs/building.en.md) | Building and testing |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The design goal is that **supporting a new server OS means writing one adapter**, which is how Windows support landed. OpenRC (Alpine), launchd (macOS) and FreeBSD are all open.

Found a vulnerability? Please email **cpprhtn@naver.com** rather than opening a public issue.

## License

[Apache-2.0](LICENSE)
