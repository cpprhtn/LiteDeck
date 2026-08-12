# Build from source

[← README](../README.en.md)

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

v1.2.1 was built with Go 1.26.5, Node 22.13.1, Wails 2.14.0, Docker 29.4.0 on macOS 26.5.2 arm64.
