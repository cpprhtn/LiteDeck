# Reaching your home machine from anywhere (Tailscale)

> 한국어: [remote-access.md](remote-access.md)

LiteDeck connects to anything SSH can reach. The hard part is usually **making SSH
reachable**. A home machine or homelab sits behind a router, so getting to it from
outside normally means port forwarding, dynamic DNS, or standing up a VPN. Exposing
the SSH port to the internet is not something to do casually.

Tailscale removes all three. The two tools split the work cleanly:

| | What it removes |
|---|---|
| **Tailscale** | Network setup (port forwarding, DDNS, firewall holes) |
| **LiteDeck** | Server install (agents, daemons, packages) |

Together you get **no port forwarding and nothing installed on the server**: open files
on your home machine from a café, restart a service, edit a config and save it.

---

## Read this first: Tailscale needs an account

LiteDeck's fourth principle is "no account, no telemetry", and **Tailscale requires an
account with a coordination server on their infrastructure.** Traffic itself flows
directly between your devices over WireGuard, but the thing that tells your devices
about each other is theirs.

If you want that part too, see "Going without an account" below. LiteDeck cannot tell
the difference either way. To LiteDeck it is just an address with SSH on it.

It does not cost anything. One person connecting their own machines fits inside the free
plan ([pricing](https://tailscale.com/pricing) — the terms change, so take the numbers
from there rather than from here).

---

## 1. Enable SSH on the home machine

**Linux.** If you can already SSH in, there is nothing to do.

**Windows.** Three lines in an elevated PowerShell:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd
Set-Service -Name sshd -StartupType Automatic
```

**macOS.** System Settings → General → Sharing → Remote Login.

## 2. Install Tailscale on both machines

Get it from [tailscale.com/download](https://tailscale.com/download), install it on the
home machine and on your laptop, and sign in **with the same account** on both. They
land in the same tailnet.

The router is not touched. No ports opened, no DDNS.

## 3. Find the home machine's tailnet address

On the home machine:

```bash
tailscale ip -4          # prints something like 100.x.y.z
```

With MagicDNS enabled you can use a name like `mypc` instead of the address.

> Pick one and stay with it. LiteDeck keys its `known_hosts` entries by the address string,
> so connecting once by address and once by name **asks for the fingerprint twice for the
> same machine.** Not wrong, but there is no reason to check it twice.

## 4. Add the host in LiteDeck

Press **+ Add** and fill in that address.

| Field | Value |
|---|---|
| Host | `100.x.y.z`, or the MagicDNS name |
| Port | whatever sshd listens on. `22` by default |
| User | the account on the home machine |
| Auth | put ssh-agent first if you can |

On first connect LiteDeck shows the host key fingerprint. **Check it against the home
machine itself.** Tailscale builds the path; verifying who is on the other end is still
SSH's job, and still yours.

```bash
# run on the home machine and compare with what LiteDeck shows
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

That is the whole setup. The machine now appears in the sidebar from anywhere.

---

## If you have Tailscale SSH turned on

Tailscale has an SSH feature of its own. With it enabled, tailscaled intercepts **port 22 on
the tailnet address only** and answers those connections itself rather than handing them to
your sshd. It leaves `sshd_config` and `authorized_keys` alone, and anything arriving from
outside the tailnet still reaches your normal sshd.

To LiteDeck this is **a different server on the other end.** The tailnet policy does the
authentication, so it connects without asking for a key or a password, and the host key comes
from tailscaled rather than from sshd. Which means:

- **The fingerprint will not match the server's `ssh_host_ed25519_key.pub`.** The check in
  step 4 above does not apply in this case. Nothing is wrong; the other end is a different
  thing.
- **Turning Tailscale SSH on or off changes the host key.** LiteDeck drops the connection when
  a recorded key is replaced and offers no way past it. That is deliberate — remove the stored
  entry and verify the new fingerprint.

Pick one or the other. If you mean to use the server's own sshd, leave Tailscale SSH off.

## Using it with Claude (MCP)

This is where the combination pays. LiteDeck's MCP endpoint binds to **`127.0.0.1` and
nothing else**, which puts the pieces in this order:

```
Claude Code ──local HTTP──▶ LiteDeck ──Tailscale (WireGuard)──▶ home machine
  (laptop)                   (laptop)                            (nothing installed)
```

- **The MCP endpoint never reaches the tailnet.** It does not leave the laptop, so no other
  device on the tailnet can read or change your servers through it.
- **Still nothing installed on the server.** This is not the same as putting Claude Code on
  the home machine and reaching it over the tailnet; that leaves a runtime and a resident
  process there.
- Approval dialogs appear on the laptop. If you are stepping away, read the bypass mode and
  the undo list in the [MCP doc](mcp.en.md) first.

## Several servers behind a subnet router

If there are several machines at home and you would rather not install Tailscale on all of
them, make one a [subnet router](https://tailscale.com/kb/1019/subnets) advertising that
range. The rest go into LiteDeck under their ordinary LAN addresses. As far as those servers
are concerned **Tailscale does not exist**, and LiteDeck cannot tell the difference either.

Principle 1 survives the arrangement: the install lands on the router alone.

---

## Going without an account

If you want the coordination server to be yours as well, there are two routes.

**[Headscale](https://github.com/juanfont/headscale)** is an open-source, self-hosted
implementation of the Tailscale coordination server (BSD-3). You keep the official
Tailscale clients and point their login at your own server, which removes both the
account and the third-party infrastructure. The cost is that the coordination server has
to live somewhere reachable, so you need a VPS for it.

**[WireGuard](https://www.wireguard.com/) by hand** removes the coordinator entirely.
You exchange keys yourself and write the endpoints out. For two or three devices this is
very manageable, especially when only one side is behind NAT. It scales badly as devices
are added.

**LiteDeck cannot tell Tailscale, Headscale and plain WireGuard apart.** It needs an
address with SSH listening on it.

---

## No VPN at all: when one machine is already reachable

If **one machine at home or at work already takes SSH from outside**, there is no VPN to set up.
Use it as the way in to everything else on that network: put it in the host editor's **ProxyJump**
field.

```
Host        192.168.0.20        ← the machine inside, invisible from outside
ProxyJump   me@myserver.com     ← the one that is reachable
```

The bastion is treated as **a server you log in to**: its fingerprint is confirmed separately and
its password asked for separately. The target's host key check is unchanged — the bastion forwards
bytes and vouches for nobody.

Its sshd needs `AllowTcpForwarding yes`. One hop only; a chain is refused rather than quietly
truncated to its first host.

**Against Tailscale** — nothing to install and no account, but it needs a machine that is already
reachable, with that port open to the internet. If avoiding exactly that is why you are reading
this page, Tailscale is the better answer.

---

## Worth knowing

**Speed.** Tailscale makes a direct WireGuard connection when it can. If both ends are
behind awkward NAT it falls back to a DERP relay and latency goes up. `tailscale status`
tells you which you got. LiteDeck does not stream a screen, so a relay is still usable,
but file transfer speed is noticeably different.

**A sleeping machine is an unreachable machine.** Disabling sleep is the reliable answer.
Wake-on-LAN sends its magic packet **from inside the same LAN**, so it cannot wake the
machine over the tailnet from outside. It does work if something else on that LAN stays
awake and has Tailscale on it — you reach that, and send the packet from there. Neither
LiteDeck nor Tailscale solves this for you.

**Tailscale ACLs.** With several devices on the tailnet you can restrict which of them
may reach the SSH port. The default allows everything.

**How far this has been verified.** Steps 1–4 have been: a host added at its tailnet
address, connected, with files and metrics read over it. The later sections have not —
**Tailscale SSH, subnet routers, Headscale and hand-rolled WireGuard** are held to the
same standard used in [what is and is not verified](support.en.md), where
**unverified means unverified.** If you try one, please report back in an
[issue](https://github.com/cpprhtn/LiteDeck/issues).
