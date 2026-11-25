# Installation Guide

`acpx` compiles to a single static Go binary with no runtime dependencies on Linux, macOS, and Windows (amd64 and arm64).

## Option 1: Install with Go

Requires Go 1.22+ installed on your machine:

```sh
go install github.com/bberka/acpx/cmd/acpx@latest
```

Make sure Go's `bin` directory is in your `PATH`:

```sh
# bash / zsh
export PATH="$(go env GOPATH)/bin:$PATH"
```

## Option 2: Build from Source

```sh
git clone https://github.com/bberka/acpx.git
cd acpx
make build
```

This creates a static `./acpx` binary in the repository root. Optionally install it into your `PATH`:

```sh
# Linux / macOS
install -m 0755 ./acpx ~/.local/bin/acpx
```

To cross-compile binaries for Linux, macOS, and Windows (`amd64` + `arm64`):

```sh
make cross
ls -lh dist/
```

## Option 3: Install Script (GitHub Releases)

Once a tagged release (e.g. `v0.1.0`) is published via GitHub Releases:

```sh
curl -fsSL https://raw.githubusercontent.com/bberka/acpx/main/install.sh | sh
```

Environment overrides supported by `install.sh`:
- `ACPX_INSTALL_DIR` — destination directory (default: `~/.local/bin`)
- `ACPX_VERSION` — specific tag such as `v0.1.0` (default: `latest`)
- `ACPX_REPO` — GitHub `owner/repo` (default: `bberka/acpx`)

## Verify Installation

Run `acpx doctor` to verify storage paths, keychain availability, port binding, and detected ACP CLIs:

```sh
acpx doctor
```

## Installing Optional ACP CLIs

`acpx` never bundles or spoofs subscription tools — it launches the official CLI installed on your `PATH`:

| Tool | Binary checked | Notes |
|---|---|---|
| Google Antigravity | `agy` | Official Antigravity CLI (`agy acp`) |
| Gemini CLI | `gemini` | `npm i -g @google/gemini-cli` |
| OpenAI Codex (ACP) | `codex-acp` | `npm i -g @agentclientprotocol/codex-acp` (plus `codex login`) |
| Claude Code (ACP) | `claude-agent-acp` | `npm i -g @agentclientprotocol/claude-agent-acp` (plus `claude` login) |

## Storage Directory

By default `acpx` stores everything in one folder:

- **Linux:** `~/.config/acpx` (respects `XDG_CONFIG_HOME`)
- **macOS:** `~/Library/Application Support/acpx`
- **Windows:** `%APPDATA%\acpx`

Override with `ACPX_HOME`:

```sh
ACPX_HOME=/path/to/dir acpx
```
