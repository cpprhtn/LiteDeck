# Install and server setup

[← README](../README.en.md)

## First launch: getting past the warning

**macOS 15 (Sequoia) and later.** You will see `Apple could not verify "litedeck" is free of malware…`, and the dialog offers only "Move to Trash" and "Cancel".

Open **System Settings → Privacy & Security**, scroll down to the Security section, and click **Open Anyway** next to `"litedeck" was blocked`. Once is enough. Or from a terminal:

```bash
xattr -d com.apple.quarantine /Applications/litedeck.app
```

> The commonly cited **right-click → Open** trick was removed by Apple in macOS 15. It still works on 14 and earlier.

**Windows.** SmartScreen shows *Windows protected your PC*. Click **More info → Run anyway**. If WebView2 is missing you will be prompted to install it.

**Defender may delete the file.** An unsigned new executable has no reputation, so it is sometimes quarantined on download with no prompt. That is the absence of a signature, not a detection. Signing and notarisation cost money and early releases go out unsigned.

If it disappears, **Windows Security → Virus & threat protection → Protection history** has the entry; **Actions → Allow** restores it. To pre-empt it, add the unzipped folder under **Manage settings → Add or remove exclusions**.

**Linux.** Nothing special: unpack and make it executable — but the released binary links
against **`libwebkit2gtk-4.1`**, so it needs **Ubuntu 24.04 or newer**; that is the package the
CI runner has. On a distribution that only ships `libwebkit2gtk-4.0`, such as 22.04, it will not
start — [build from source](building.en.md) instead.

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
[docs/remote-access.en.md](remote-access.en.md).
