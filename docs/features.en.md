# Features in detail

[← README](../README.en.md)

## Command Log: learn the CLI from the GUI

<p align="center">
  <img src="media/03-command-log.gif" width="820" alt="Command Log">
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

## The editor: nothing installed on either end

<p align="center">
  <img src="media/02-terminal-jump.gif" width="820" alt="code . and vi in the terminal">
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

<p align="center">
  <img src="media/05-editor.gif" width="820" alt="Open a file, edit it, see the diff before saving">
</p>

<p align="center"><sub>Opening a file from the tree puts a syntax-highlighted editor beside it. Pressing save brings up <b>a diff against what is on the server right now</b>.</sub></p>

Once you accept it, the write goes to a temp file and swaps in with `rename`, so an interrupted
save cannot leave the original half-written.

> For contrast: VS Code Remote-SSH installs a server on your server. `vscode-server` eating
> memory on a small VPS is a well-known problem. If you want a real remote IDE, that is the right
> tool. **If you do not want to upload hundreds of megabytes to change one config file**, this is.

## Filtering the tree by name

Type part of a name in the box beside the address bar (`⌘F` · `Ctrl+F`) and the listing narrows.

**Nothing is asked of the server.** It filters the listings the app already holds, so typing as fast
as you like costs the server nothing. Folders you have opened once are searched even when collapsed,
so a file inside one surfaces with the path leading to it.

The trade is that **folders you have never opened are not searched.** A remote `find` would reach
them, but that is work the server pays for, and it is the side of the line this tool stays on. When
nothing matches, the screen says so.

## Transfers: whole folders, and resuming after an interruption

Directories go up and down in one piece. The queue gets **one row per tree**, not one per file, and
that row says which file it is on.

The server's share of the cost was the design constraint. Both walking the tree and moving it are
**SFTP and nothing else** — no `find`, no `tar`, so no process starts on the server. The walk runs
inside the job, which means **the Cancel button reaches it**: pick the wrong directory by accident
and there is a way to stop.

**An interrupted transfer picks up where it stopped.** SFTP addresses every read and write with an
absolute offset, so continuing is one seek — there is no Range header to negotiate. The hard part is
knowing that what you are about to append to came from the same file, and LiteDeck checks it twice:

- the source's **size and timestamp** are the ones recorded when the transfer was queued
- the **64KB immediately before the resume point is read back from both sides and compared**

The second check earns its keep because SFTP carries mtime in **whole seconds**. A file rebuilt to
the same length inside the same second is indistinguishable by size and timestamp alone. If the
bytes at the seam disagree, the resume is refused and the half-finished file is deleted.

> This checks the seam, not the whole prefix. Verifying every byte already transferred would mean
> reading the source in full, which is the download resuming exists to avoid. **A source edited to
> exactly the same length, only outside that 64KB window, would still get through.** Written down
> because a limit nobody states is a limit people trust past.

Folder transfers are not resumable. Skipping already-copied files on size alone would keep a stale
copy of anything that changed without changing length.

## Reviewing the sshd configuration

Below the network tab, LiteDeck reads the server's sshd configuration and says what is worth
knowing: `PermitRootLogin yes`, `PermitEmptyPasswords yes`, a low `MaxSessions`, and so on.

**It does not run `sshd -T`.** That would give the effective configuration with every default filled
in — and it needs root, and a read-only view that demands a password before it shows you anything is
a view nobody opens. Instead the files are **read over the SFTP that is already open**: they are
world-readable everywhere they ship, and not a single command runs.

The cost of that trade is this. **It knows what the files declare.** Where
they are silent, sshd's built-in default applies, and that differs between distributions (Debian's
`PermitRootLogin` is not upstream's), so nothing is guessed.

Two things it gets right that are easy to get wrong. **A value under `Match` is not the server's
setting** — reporting the `PermitRootLogin yes` beneath `Match Address 10.0.0.0/8` as though it
applied to everyone is the classic misreading of this file. And **sshd keeps the first value it
reads**, the opposite of most config formats, which is why distributions put their `Include` at the
top of the file.

## ProxyJump: one hop through a bastion

Put `jump@bastion:22` in the host editor's ProxyJump field and that server is dialled first.

A bastion is **a server you log in to**, so it is treated as one: **its fingerprint is confirmed
separately and its password asked for separately.** A ProxyJump that skips the bastion's host key
hands the whole session to whoever answers on that port, which is the thing the hop was there to
prevent. The target's own host key is checked exactly as it would be otherwise — the bastion
forwards bytes, it does not vouch for who is on the other end of them.

One hop. A chain is **refused, not quietly truncated to its first host**: connecting somewhere other
than what you wrote is worse than not connecting.

The bastion's sshd needs `AllowTcpForwarding yes`. With it off, sshd refuses with
`administratively prohibited`, which reads as a problem with your account and sends people looking
at the wrong machine — so LiteDeck says **which server and which setting** instead.
