# Server mode (access from a web browser)

[← README](../README.en.md)

You can run LiteDeck on a server and use it from a browser instead of as a
desktop app. For environments that cannot run a native binary (execution
blocked by policy, managed laptops), put it on one separate server and reach it
by URL — the shape Grafana is used in.

```
browser ──HTTP──▶ litedeck-server ──existing SSH──▶ the servers you manage
```

It runs the **same core** as the desktop app; the screen is the same. Only the
shell differs — it serves the UI over HTTP instead of in a Wails webview, and
with no webview it is a **statically linked single binary with no WebKit/GTK
dependency**.

## Running

```bash
# On a Linux server (release artifact litedeck-server-linux-amd64)
./litedeck-server                       # default: 127.0.0.1:8765
./litedeck-server --addr 127.0.0.1:9000 # a specific port
```

Open that address in a browser. Servers, credentials and connections behave
exactly as on the desktop: register a host, connect (host keys and passwords are
asked in browser dialogs), use files, terminal, monitoring and MCP.

## Exposure and authentication are your job

This endpoint **opens SSH sessions to your servers.** Anyone who can reach the
port can drive them. So:

- **It binds `127.0.0.1` by default.** Left there, it cannot be reached from off
  the server.
- **To expose it, put a reverse proxy in front** (oauth2-proxy, Cloudflare
  Access, Tailscale) and **do authentication and TLS there.** LiteDeck itself
  stays single-user; auth is delegated outward. This is the Grafana standard.

```nginx
# nginx example — do auth here (auth_request etc.), keep LiteDeck on loopback
location / {
    proxy_pass http://127.0.0.1:8765;
    proxy_http_version 1.1;
    # for the WebSocket (events, terminal, logs)
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    # a connect prompt can hold a request open for minutes; give it room
    proxy_read_timeout 600s;
}
```

> [!IMPORTANT]
> To bind a non-loopback address directly without a proxy (`--addr 0.0.0.0:...`)
> a **`--token` is required**; the server refuses to start otherwise — so an
> unauthenticated SSH gateway is never opened by accident.

## Password (login)

The server binary has **login on by default.** Open the port and you get a login
page; pass it and a session cookie lets you in — no URL token, no reverse proxy
(the Grafana way).

- **The default password is `litedeck`** — the app's own name, a memorable
  default so a fresh instance works in one guess. It is not a secret.
- **Changing it is a deploy-config edit only.** There is no web change form
  (single-user, and a change form is only attack surface). From a terminal:
  ```bash
  # via the environment (systemd unit, docker run, a shell, ...)
  LITEDECK_PASSWORD='your-password' litedeck-server --self <user>
  # or a flag (but argv is visible in ps, so prefer the env var)
  litedeck-server --password 'your-password' ...
  ```
  To change it, edit that config and **restart** — the same as Grafana or Cockpit.
- **Leaving the default prints a warning at startup.** Change it before exposing
  the instance — anyone who knows it is `litedeck` can log in.
- To turn login off entirely, `--no-auth` (loopback only — refused on a
  non-loopback bind).

## What to know (verification and limits)

- **Passwords leave the process over the network.** A headless server has no OS
  keychain, so it asks for each server's password in the browser, and that value
  travels over HTTP to the server. Unless it is pure loopback, **put it behind
  TLS (a proxy)** — plain HTTP over the network exposes the credential.
- **File upload uses the browser file picker** (instead of the desktop's native
  dialog): the chosen files stream to the server and are written to the remote
  over SFTP. **Whole-folder upload, drag-and-drop, and remote→local drag** are
  not there yet — use the desktop app.
- **It is single-user.** One instance is one person's world (one host list, one
  set of connections, one Command Log). Multiple tabs share the same state.
  Multi-user with per-person logins is out of scope — that is a different tool's
  job (Teleport, Warpgate).
- **MCP** comes up on the server's loopback only if you enabled it in settings
  (same as desktop).

## Verified

| | Status |
|---|---|
| **Static build** | `CGO_ENABLED=0 GOOS=linux` produces a statically linked ELF binary |
| **Web transport round-trip** | Against a real sshd: connect prompts (host key, password) arrive over the WebSocket and are answered over RPC, completing the handshake — covered by an integration test |
| **File upload** | Connecting to a real sshd, a browser upload lands on the remote with the right contents, and a hostile file name cannot escape the chosen folder |
| **UI render** | The app served by the headless server boots and renders in a real browser |
| **Real multi-user load** | Not verified — single-user by design |
| **Behind a reverse proxy** | Not verified — the example config is above |

For the desktop app and the release process, see [support](support.en.md).
