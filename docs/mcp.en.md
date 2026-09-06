# MCP integration

[← README](../README.en.md)

<p align="center">
  <img src="media/04-mcp-approval.gif" width="820" alt="MCP approval">
</p>

<p align="center"><sub>An MCP client tries to change a file and the <b>diff against what is on the server</b> comes up. Anything approved can be undone.</sub></p>


An MCP client (Claude Code, Claude Desktop) can **read and change** your servers through this
app. Turn it on with the **MCP** button at the bottom of the sidebar.

> [!IMPORTANT]
> **File changes are confirmed by default**, and the dialog shows the literal
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

**Running commands is enabled per server too — off by default.** `run_command` runs one line with `sh -c` and returns stdout, stderr and the exit code.
It is a separate switch from deletion, because letting an agent clear a log file is not the same
decision as letting it run anything.

This used to be on the deliberately-absent list. Three things took it off.

- **The objection is answered by giving it its own switch.** The argument was that an
  arbitrary-command tool makes the per-tool allowlist decorative, and that only holds while it
  shares the allowlist's switch
- **The auditing was backwards.** A command typed in the terminal tab leaves **nothing** in the
  Command Log. One that goes through MCP leaves the tool call, the command itself and its exit
  code. Refusing to run commands was reducing the record, not protecting it
- **The line was already nominal.** `fs_write` can write `~/.bashrc` or `~/.ssh/authorized_keys`,
  which is arbitrary code on the next login

**Approval follows the same modes as every other write tool.** The default asks before a command:
`svc_control(restart, nginx.service)` is fully described by the call the client already showed, and
a shell line is not — the dialog is the only place a person reads it. Turn on "don't ask overnight"
and commands go through with everything else.

The first cut put commands outside the modes and made them always ask. That broke the split this
page sets out — the toggle answers *does the tool exist*, the mode answers *does it interrupt you*
— and a mode switched on for unattended work that stops at the first command has not left the agent
anywhere. Being irreversible is not what separates it either: `proc_signal` (KILL) and
`svc_control` are equally irreversible and the modes already cover them.

**What does not change is undo.** A command leaves no copy, so it never appears in the Changed
files tab, and the dialog says so. There is no sudo — it runs as the login user with no tty, so
anything wanting a password fails instead of waiting.

**Still absent**: recursive directory deletion and removing containers or images. Nothing can copy
those first, so nothing could put them back.

**What holds it back**

| | |
|---|---|
| Per-server opt-in | **Everything off by default.** Adding a host does not expose it; only what you switch on can be read, and deleting files and running commands are each a separate switch again |
| Execution is gated twice | The per-server toggle decides whether the tool **exists**; the approval mode decides whether it **interrupts you**. With the toggle off no mode will run a command, and with it on the default mode still asks every time |
| Change approval | **Only file changes are confirmed** by default, because the dialog shows a diff against what is on the server right now, which is information no client has. A restart just runs; the client already showed you the same thing |
| Per host | A badge in the header: **ask always / files only / don't ask overnight**. While it is not asking the badge stays red, and the window **reverts on its own** |
| Not settable remotely | No tool flips that switch and no parameter relaxes it. **A model has no way to request its own approval** |
| Reading ≠ writing | Sharing a server to be read does not make it changeable |
| Binding | `127.0.0.1` only. No setting exposes it on another interface |
| Auth | Bearer token, stored in settings, changed only by the rotate button |
| Rate limit | 1.5 calls/sec, burst of 8, so an agent loop cannot hammer a small server |
| Audit | Every tool call lands in the Command Log. Local only, sent nowhere |

## Connecting

Pick a client on the MCP panel's **Connection** tab and press **Copy**. The app assembles the
line, so the port and the token never have to be copied by hand.

| Client | Status | How it attaches |
|---|---|---|
| **Claude Code** | ✅ **verified on real hardware** | one `claude mcp add --transport http …` line |
| **Claude Desktop** | ⬜ not verified | the same Streamable HTTP settings |
| **Codex CLI** | ✅ **verified by a contributor** | takes the **name** of an environment variable, not the token |
| Any other MCP client | ⬜ not verified | Streamable HTTP with a Bearer token is all it needs |

```bash
# Claude Code
claude mcp add --transport http litedeck http://127.0.0.1:<port>/mcp \
  --header "Authorization: Bearer <token>"

# Codex CLI — the variable's name goes in the command, the token goes in the environment
export LITEDECK_MCP_TOKEN=<token>
codex mcp add litedeck --url http://127.0.0.1:<port>/mcp \
  --bearer-token-env-var LITEDECK_MCP_TOKEN
```

> [!NOTE]
> **The two statuses are different and are written down separately.**
>
> **Claude Code 2.1.22 — verified on the author's machine.** Confirmed end to end against a
> **real Ubuntu 24.04 server**. Asked *"how is the server doing"*, the model calls
> `health_snapshot` by itself and comes back with the metrics, the failed unit, the stopped
> containers and the exposed ports. Writes do raise the approval dialog; approving sends the
> command through to the server, and a write nobody answers does not run.
>
> **Codex CLI — a contributor attached it and it works.** The author has no Codex install,
> which is why this sat unverified for so long. [@INMD1](https://github.com/INMD1) connected it
> against a real server and confirmed it.
>
> What the author checked is still only the protocol: replaying the requests Codex makes, the
> endpoint answers correctly to all of them — 404 on the OAuth discovery paths, which reads as
> "no OAuth, use the token"; `401 WWW-Authenticate: Bearer` without credentials; the 405 the
> spec prescribes for a `GET` that would open an SSE stream; 200 for a POST with no `Accept`
> header.
>
> MCP against a **Windows** server has not been tried yet.
