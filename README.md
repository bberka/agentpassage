# AgentPassage (`apx`)

AgentPassage (`apx`) is a lightweight, single-binary, multi-platform (Linux, macOS, Windows) local gateway and credential bridge. It is a TUI-first alternative to **CLIProxyAPI** and other web-based AI proxies.

It unifies ACP subscription agents, CLI providers, and HTTP API-key providers behind OpenAI-compatible (`/v1/chat/completions`, `/v1/embeddings`, `/v1/rerank`, `/v1/models`), Anthropic-compatible (`/v1/messages`), CLIProxyAPI-compatible management/usage endpoints, and native ACP stdio (`apx acp`). It has a keyboard-driven terminal UI and a full non-interactive CLI.

AgentPassage manages authentication, isolated profiles, protocol bridging, routing, health, quotas, and privacy filtering. It is intentionally not a chat client or coding harness.

## Why AgentPassage (vs CLIProxyAPI & web proxies)

- **Single static binary (~12 MB), zero runtime:** No Node, Python, Docker, or separate web dashboard process required. It runs natively on Linux, macOS, and Windows (x64 & arm64).
- **Keyboard-driven TUI + headless CLI:** Manage accounts, models, thinking effort levels, routing, logs, and live quotas right in the terminal (`apx`), over SSH, or via `apx <cmd> --json` scripts.
- **Native ACP stdio bridge (`apx acp`):** Connect directly as a custom ACP agent in T3 Code, Zed, Delta (Zed Agent IDE), JetBrains, and Neovim, not just HTTP.
- **CLIProxyAPI-compatible endpoints:** Drop-in management and usage hub endpoints (`/v0/management/*`, `/usage`) for tools that already integrate with CLIProxyAPI.
- **Real official CLIs, no spoofing:** Each account runs its own official binary in an isolated profile directory, and client identity is never disguised.

## Core capabilities

- **Multi-account ACP, CLI & API-key profiles:** Isolated per-account auth/profile directories for 45+ agents from the ACP Registry (`agy`, `codex`, `claude`, `grok`, `opencode`, `pi`, `cursor`, `cline`, etc.) plus HTTP presets (`deepseek`, `openrouter`, `openai`, `anthropic`, `groq`, `mistral`, `xai`, `ollama`, custom URLs).
- **Safe primary-first failover:** Uses your first account fully and switches only when it hits quota/429, cooldown, or outage. Parses provider reset durations (e.g. `Resets in 48m24s`, `try again in 2h 15m`) into real cooldown windows. `round-robin` and `least-busy` are available for API-key pools and gated behind an explicit ban-risk confirmation for subscription accounts.
- **Model fallback chains:** Retry eligible failures (`rate_limit`, `transient`, `unavailable`) across configured alternate models or providers, separate from account failover.
- **Project-based routing:** Route requests to specific accounts or rules via `X-Apx-Project` headers or project-scoped API keys (`apx keys create work --project work`).
- **Reversible privacy filter:** Replace API keys, email addresses, phone numbers, and custom regex matches with per-request placeholders (`{{APX_KEY_1}}`, `{{APX_EMAIL_1}}`) before sending upstream, then restore originals across both non-streaming and streaming responses.
- **Embeddings & reranking:** OpenAI-compatible `POST /v1/embeddings` and standard `POST /v1/rerank` endpoints routed through the same auth, project rules, failover, and usage pipeline.
- **Usage, estimated costs & subscription limits:** Track requests, tokens, latency, and reference-rate cost estimates by period (`1h`/`24h`/`7d`/`30d`/`all`), model, account, API key, and project, alongside live subscription limits (Codex, DeepSeek balance, OpenRouter credits, Claude local 5h/7d logs, HTTP rate-limit headers).
- **Agent-driveable CLI & IDE connectors:** Every feature is controllable via `apx <cmd> --json` and via TUI, with preview-and-confirm `apx connect` / `apx disconnect` / `apx uninstall` for T3 Code, Zed, Delta (Zed Agent IDE), VS Code, JetBrains, Continue, Neovim, OpenCode, Codex, Claude Code, Pi, Grok, Aider, Cursor, Windsurf, and Cline.

## Install

**Linux / macOS:**

```sh
curl -fsSL https://raw.githubusercontent.com/bberka/agentpassage/main/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/bberka/agentpassage/main/install.ps1 | iex
```

**Or from a local clone / Go:**

```sh
git clone https://github.com/bberka/agentpassage.git
cd agentpassage
make install        # builds static binary -> ~/.local/bin/apx
apx service install --auto-start
apx                 # open TUI (attaches to running service)
```

Or with Go:

```sh
go install github.com/bberka/agentpassage/cmd/apx@latest
```

## Quick commands

```sh
apx status --json
apx add --kind acp --backend codex --name main
apx add --kind api_key --backend deepseek --name main --api-key "$DEEPSEEK_API_KEY"
apx routing add "agy/*" --strategy failover --fallback "codex/gpt-5,deepseek/deepseek-chat"
apx routing add "deepseek/*" --project work --accounts ds-work --strategy round-robin
apx keys create laptop --role client --project work --models "deepseek/*"
apx privacy enable && apx privacy test "Email alice@example.com with sk-proj-1234567890abcdef"
apx usage --period 24h --refresh
apx connect t3code
apx agent-guide
```

## Documentation

- [Installation](docs/installation.md): Linux, macOS, Windows, and background service setup
- [Quickstart](docs/quickstart.md): First account, TUI keys, and client connection
- [Routing & Security](docs/routing-and-security.md): Primary-first failover, project routing, API-key vs subscription rules, key scoping
- [Privacy Filter & Model Fallbacks](docs/privacy-and-fallbacks.md): Reversible redaction and cross-model fallback chains
- [API Reference](docs/api-reference.md): Chat completions, Anthropic Messages, embeddings, reranking, admin endpoints
- [CLI & Agent Automation](docs/cli-and-agents.md): Full CLI command reference and `skills/agentpassage/SKILL.md`
- [Remote Access](docs/remote-access.md): Tailscale Serve and Cloudflare Tunnel setup
- [ACP Backends](docs/acp-backends.md): Embedded 45+ agent registry and custom descriptors
- [T3 Code Integration](docs/t3-code-integration.md): What T3 Code reads from apx
- [Development](docs/development.md): Checks, deploy and package layout. Agent rules are in [AGENTS.md](AGENTS.md)
