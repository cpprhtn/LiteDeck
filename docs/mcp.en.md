# MCP integration

[← README](../README.en.md)

<p align="center">
  <img src="media/04-mcp-approval.gif" width="820" alt="MCP approval">
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

**Thirteen read tools**: `hosts_list`, `health_snapshot`, `sys_stats`, `svc_list`, **`svc_logs`**,
`proc_list`, `container_list`, **`container_logs`**, `net_ports`, `fs_list`, `fs_read`,
`sessions_list`, **`kuma_status`**. One `health_snapshot` returns CPU, memory, disk, failed units,
unhealthy containers and exposed ports; `svc_logs` is what says *why* something died.

**Five write tools**: `svc_control` (start/stop/restart), `container_control`, `proc_signal`
(TERM/KILL), `fs_write` and `fs_delete`.

**`kuma_status` — Uptime Kuma's monitors.** Answers "is anything down right now". That is a
different question from `health_snapshot`, which is about *this server* rather than the things it
watches. It reads exactly one surface: Kuma's **`/metrics`**, in Prometheus text format. Kuma's own
README says its API is built for its own web UI and that third-party integrations carry no
guarantee — `/metrics` is the one part that exists *for* outside readers, so `/api/*` is left
alone.

**There are no writes.** No tool creates, edits or deletes a monitor; that is Kuma's job, and
putting writes on top of a surface with no compatibility promise is a mistake in the direction you
cannot undo. The port and the API key are set per server in the **Kuma settings on the network
tab**. The key is kept only in this machine's credential store (§7.4), and the Command Log line
carries a placeholder in its place.

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
