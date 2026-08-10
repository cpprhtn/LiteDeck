# Security

[← README](../README.en.md)

This is a tool that reaches production servers over SSH, so the following is not design
intent. It is **what the code does today**, with the file to check it against.

## Host key verification

**Yes.** Keys are verified against an OpenSSH-format `known_hosts`
([`internal/sshcore/hostkey.go`](../internal/sshcore/hostkey.go)).

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

## Authentication

ssh-agent, private key file (with passphrase), password, and keyboard-interactive for 2FA and OTP.
**The order tried is configured per host.**

- **Put ssh-agent first if you can.** The key never enters this process
- OTP and 2FA answers are **never stored.** A one-time code is worthless the second time

## Credential storage

If a secret is stored, it goes **only into the OS credential store**: macOS Keychain, Windows
Credential Manager, Linux Secret Service ([`internal/secret/secret.go`](../internal/secret/secret.go)).

- **LiteDeck never writes a secret to a file of its own.** Where no credential store is reachable
  the answer is not a weaker file format but **not storing it**. You are asked every time, and the
  "remember this" checkbox is not shown at all rather than promising something it cannot keep
- Storing is opt-in at each prompt. Every host has a **forget stored passwords** action
- If a stored sudo password stops working because it changed on the server, **it deletes itself.**
  Otherwise the keychain keeps answering before the dialog appears and you can never correct it
- `hosts.json` holds addresses, usernames and **paths** to key files. No secrets

## sudo

**Nothing is escalated on your behalf** ([`internal/app/sudo.go`](../internal/app/sudo.go)). Commands
run as the user you logged in as. If the server refuses, the UI **asks** whether to retry as
administrator and you press the button. Silently prefixing `sudo` would make the Command Log stop
matching what you believe you asked for, and that log is the only reason to trust this app.

- The password travels **on stdin only**: `sudo -S -p '' -- <cmd>`. In argv it would be visible in
  the remote process table to every other user on that machine, and would appear verbatim in the
  Command Log. **That is why the Command Log is safe to display in full**
- NOPASSWD is detected with `sudo -n true`, and then nothing is asked. Prompting for a password the
  server does not want is not just pointless. **It trains you to type your password into any dialog**

## The MCP endpoint

Turning on [MCP integration](mcp.en.md) opens one local HTTP endpoint. That single endpoint
**speaks for every server you have shared**, so what is written here decides whether the feature is
safe ([`internal/mcp/http.go`](../internal/mcp/http.go)).

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

## What it does not do, stated up front

- **It cannot bound how long a password stays in memory.** Go strings are immutable and the runtime
  may copy them at any time, so a secret held as a string cannot be erased on demand;
  `x/crypto/ssh` takes passwords as strings too. It becomes garbage immediately and is freed at the
  next collection, but **there is no zero-on-use implemented.** A core or heap dump could show it
- **Release binaries are unsigned.** A SHA256 checksum is not a signature
- **On real Linux hardware only the read side and file writes have been exercised**; transfers, completing a sudo escalation, the terminal PTY and log tailing are still container-only. See
  [what is and is not verified](support.en.md)
- There is no audit log. The Command Log stays on your machine and goes nowhere.
  **A log the client writes is not an audit**
- Dependency vulnerabilities are checked by `govulncheck ./...` in CI, on pushes to `main` and every PR

Found a vulnerability? Please email **cpprhtn@naver.com** rather than opening a public issue.
