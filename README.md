# acpx

`acpx` is a single-binary local OpenAI- and Anthropic-compatible AI gateway with a keyboard-driven TUI.

Run `acpx`, add accounts in the TUI (no config files to hand-edit), and point any OpenAI or Anthropic client at `http://127.0.0.1:8787/v1`.

## Features

- **45+ ACP subscription agents:** Full embedded [Agent Client Protocol Registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) — Antigravity (`agy`), Codex, Claude, Cursor, Grok, OpenCode, Muse Code, Pi, Cline, Gemini CLI, Agoragentic, Amp, Auggie, Autohand, Bob, Codebuddy, Cortex, Corust, crow-cli, DeepAgents, Devin, DimCode, Dirac, Factory Droid, fast-agent, GitHub Copilot, GLM Agent, goose, Harn, Junie, Kilo, Kimchi, Kimi Code, Kiro, MiniMax, Minion, Mistral Vibe, Nova, Poolside, Qoder, Qwen Code, siGit, Stakpak, VT Code, plus custom ACP commands.
- **In-app ACP installer:** Install npm/uvx/binary ACP adapters right inside the TUI (`i`) without exiting `acpx`, or via `acpx install <tool>`.
- **Primary-first failover (subscriptions) + load balancing (API keys) + account-prefixed models:** Defaults to primary-first `failover` (exhaust one account before switching, avoiding subscription bans), supports `round-robin`/`least-busy` for API-key pools (with explicit ban-risk confirmation for subscriptions), OR per-account prefixes (`a-gemini-4-neon`, `b-gemini-4-neon`) with `m` / `P` in Accounts.
- **1-click client setup (`acpx connect` & `acpx acp`):** Auto-register `acpx` into **T3 Code** (via native `acpx acp` stdio bridge or OpenAI-compatible URL), **OpenCode**, **Codex**, **Claude Code**, **Pi**, **Grok**, **Cursor**, **Cline**, **Antigravity**, and **Muse Code**.
- **Plain API-key providers:** Built-in presets for DeepSeek, OpenRouter, OpenAI, Anthropic, Google AI Studio, Groq, Together, Mistral, xAI, local Ollama, and custom OpenAI-compatible URLs.
- **Full-screen OpenCode-style TUI:** Responsive full-terminal layout with centered modals, works over SSH and 80×24 / 16-color terminals, ~9.5 MB static Go binary.

## Install

### With Go (recommended right now)

```sh
go install github.com/bberka/acpx/cmd/acpx@latest
```

### Build & install globally from source

```sh
git clone https://github.com/bberka/acpx.git
cd acpx
make install   # builds static binary and installs to ~/.local/bin/acpx
acpx
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
| `acpx` | Start the localhost gateway and open the full-screen TUI (auto-attaches if background service is already running) |
| `acpx --no-tui` | Start the gateway without the TUI |
| `acpx serve [--port N] [--bind ADDR] [--allow-remote]` | Run headless HTTP server |
| `acpx service <install\|uninstall\|start\|stop\|restart\|status\|logs>` | Run `acpx` permanently as a background OS service (`systemd --user` / `launchd` / Windows Task) |
| `acpx acp` | Run `acpx` as a native ACP stdio server (register directly in T3 Code / Zed) |
| `acpx add [--expose pool\|prefix\|both] [--prefix a-]` | Add an API-key or ACP account |
| `acpx install <tool\|all>` | Install ACP CLI/adapter from the 45+ agent registry |
| `acpx connect [t3code\|zed\|vscode\|jetbrains\|...]` | Connect IDEs & coding CLIs (with confirmation preview) |
| `acpx disconnect <client\|all>` | Cleanly remove `acpx` from connected IDEs & CLIs |
| `acpx uninstall [--purge]` | Disconnect all IDEs/CLIs and uninstall the background service |
| `acpx keys [list\|create\|enable\|disable\|revoke]` | Manage multiple client/admin API keys (with optional model scoping) |
| `acpx remote` | Show Tailscale Serve & Cloudflare Tunnel remote access commands |
| `acpx usage [--refresh] [--json]` | Show Usage Report Hub URL, traffic stats, and live hourly/weekly limits |
| `acpx health [--json] [account]` | Run live health probes across configured accounts |
| `acpx registry sync` | Refresh official ACP Registry agent index |
| `acpx list [--json]` | List configured accounts and merged models |
| `acpx doctor` | Check data dir, secret store, installed ACP CLIs, and accounts |
| `acpx export-env [--shell sh\|fish\|powershell]` | Print client environment exports |
| `acpx version` | Print binary version |

## TUI Navigation

- `1`–`7` or `Tab` / `Shift+Tab`: Switch screens (**Dashboard**, **Accounts**, **Models**, **Routing**, **Logs**, **Usage**, **Settings**)
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
- [Remote Access (Tailscale & Cloudflare Tunnel)](docs/remote-access.md)
- [HTTP API Reference](docs/api-reference.md)

## License

[MIT](LICENSE)
