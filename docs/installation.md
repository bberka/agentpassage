# Installation

AgentPassage (`apx`) ships as a single static binary (`CGO_ENABLED=0`).

## One-line script install (builds from source, no release archive needed)

**Linux / macOS:**

```sh
curl -fsSL https://raw.githubusercontent.com/bberka/agentpassage/main/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/bberka/agentpassage/main/install.ps1 | iex
```

Both scripts require Go 1.22+, build a static binary, install it into your user bin directory (`~/.local/bin/apx` on Linux/macOS, `%LOCALAPPDATA%\apx\bin\apx.exe` on Windows), and restart the `apx` background service automatically if already installed.

## Build from a local clone

```sh
git clone https://github.com/bberka/agentpassage.git
cd agentpassage
make install       # installs to ~/.local/bin/apx
```

Ensure `~/.local/bin` is on your `PATH`:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

## Install with Go

```sh
go install github.com/bberka/agentpassage/cmd/apx@latest
```

## Background service

Install `apx` as a user service (`systemd --user` on Linux, `launchd` on macOS, Task Scheduler on Windows):

```sh
apx service install --auto-start
apx service status
```

When you launch `apx` (the TUI) while the service is running, it attaches to the running daemon and hot-reloads any changes automatically.

## Storage paths

- **Linux:** `~/.config/agentpassage` (override with `APX_HOME`)
- **macOS:** `~/Library/Application Support/agentpassage`
- **Windows:** `%APPDATA%\agentpassage`

Legacy `ACPX_HOME` and keychain entries under `acpx` are supported as automatic fallbacks.

## Uninstall cleanly

```sh
apx uninstall           # previews and confirms disconnecting all IDEs/CLIs + removing background service
rm -f ~/.local/bin/apx
```
