# acpx

`acpx` is a single-binary local OpenAI- and Anthropic-compatible AI gateway with a keyboard-driven TUI.

Run `acpx`, add accounts in the TUI (no config files to hand-edit), and point any OpenAI or Anthropic client at `http://127.0.0.1:8787/v1`.

## Features

- **Subscription agents over ACP:** Launches official CLIs (`agy`, Gemini CLI, Codex via `codex-acp`, Claude Code via `claude-agent-acp`, or any custom ACP stdio tool) in isolated per-account profile directories without spoofing client identity.
- **Plain API-key providers:** Built-in presets for DeepSeek, OpenRouter, OpenAI, Anthropic, Google AI Studio, Groq, Together, Mistral, xAI, local Ollama, and custom OpenAI-compatible URLs.
- **Unified API surface:** One local endpoint (`127.0.0.1:8787`), one model namespace (`<backend>/<model>`, `<backend>/<account>/<model>`, plus aliases), supporting both `/v1/chat/completions` and `/v1/messages` (streaming & non-streaming).
- **Routing & account health:** `sticky` (default), `manual`, `failover` (network/5xx only), and opt-in `round-robin`/`least-busy` with explicit subscription warnings, 429 cooldowns, repeated 401/403/restricted auto-disable banners, concurrency caps, and hourly/daily budgets.
- **Zero-dependency static binary:** Written in Go with Bubble Tea, works over SSH and 80×24 / 16-color terminals, ~9 MB binary, <10 ms startup.

## Install

### With Go (recommended right now)

```sh
go install github.com/bberka/acpx/cmd/acpx@latest
```

### Build from source

```sh
git clone https://github.com/bberka/acpx.git
cd acpx
make build
./acpx
```

### Release installer (once a GitHub Release is published)

```sh
curl -fsSL https://raw.githubusercontent.com/bberka/acpx/main/install.sh | sh
```

See the full [Installation Guide](docs/installation.md) for Linux, macOS, Windows, and `PATH` setup.

## Quick Start

1. Start `acpx`:
   ```sh
   acpx
   ```
   On first run, the **Welcome & Detect** screen scans your `PATH` for installed ACP CLIs (`agy`, `gemini`, `codex-acp`, `claude-agent-acp`) and lets you add them or jump straight to adding an API key.

2. Export environment variables for any OpenAI/Anthropic client:
   ```sh
   eval "$(acpx export-env)"
   ```

3. Test a request:
   ```sh
   curl -s "$OPENAI_BASE_URL/models" \
     -H "Authorization: Bearer $OPENAI_API_KEY"
   ```

See the [Quickstart Guide](docs/quickstart.md) for end-to-end examples with DeepSeek, `agy`, OpenAI SDKs, and Claude-compatible clients.

## CLI Commands

| Command | Description |
|---|---|
| `acpx` | Start the localhost gateway and open the interactive TUI |
| `acpx --no-tui` | Start the gateway without the TUI |
| `acpx serve [--port N] [--bind ADDR] [--allow-remote]` | Run headless server |
| `acpx add [flags]` | Add an API-key or ACP account (interactive or flag-based) |
| `acpx list [--json]` | List configured accounts and merged models |
| `acpx doctor` | Check data dir, secret store, installed ACP CLIs, and accounts |
| `acpx export-env [--shell sh\|fish\|powershell]` | Print client environment exports |
| `acpx version` | Print binary version |

## TUI Navigation

- `1`–`6` or `Tab` / `Shift+Tab`: Switch screens (**Dashboard**, **Accounts**, **Models**, **Routing**, **Logs**, **Settings**)
- `?`: Help overlay
- `/`: Filter current list
- `q`: Back to Dashboard / Quit from Dashboard
- `c` / `k` / `v` (Dashboard): Copy URL / Copy local key / Reveal local key
- `a` (Dashboard or Accounts): Add account

## Documentation

- [Installation Guide](docs/installation.md)
- [Quickstart Guide](docs/quickstart.md)
- [ACP Backends & Custom Descriptors](docs/acp-backends.md)
- [Routing, Pools & Security](docs/routing-and-security.md)
- [HTTP API Reference](docs/api-reference.md)

## License

[MIT](LICENSE)
