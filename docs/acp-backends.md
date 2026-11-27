# ACP Backends & Custom Descriptors

`apx` drives subscription CLIs through the **Agent Client Protocol (ACP)** over stdio JSON-RPC 2.0. It always runs the official CLI binary installed on your machine and never alters client identity or user agents.

## How ACP Mode Works

1. When a request targets an ACP account, `apx` lazily spawns that account's official CLI command in an isolated profile directory:
   ```
   <data-dir>/profiles/<backend>/<account>/
   ```
2. `apx` performs the ACP `initialize` handshake (`protocolVersion: 1`), opens or reuses a session (`session/new`), optionally selects the requested model (`session/set_config_option` / `session/set_model`), and executes `session/prompt`.
3. Streaming `session/update` notifications (`agent_message_chunk`, and optionally `tool_call`/`tool_call_update`) are converted into OpenAI or Anthropic SSE deltas in real time.
4. OpenAI `tools` and `functions` parameters are ignored for ACP backends because ACP agents run their own tools. Responses include `X-Apx-Tools-Ignored: true` when client tools were sent.

## Sessions & working directory

- **Session reuse:** Pass `X-Apx-Session: <id>` (or set the OpenAI `user` / Anthropic `metadata.user_id` field). Subsequent requests with the same key reuse both the sticky account and the live ACP session.
- **No session hint:** Each request opens a fresh ACP session.
- **Working directory:** Pass `X-Apx-Cwd: /path/to/dir` per request, or configure a default working directory per account on the **Accounts** screen (`e`).

## Permission Handling

When an ACP agent calls `session/request_permission`, `apx` answers it with the account's `permission_mode` (press `p` on the Accounts screen to cycle):

- `deny`: Rejects tool permission requests automatically.
- `ask`: Opens an interactive approval modal in the TUI (`[y] Approve once / [n] Deny`). In headless mode (`--no-tui`), falls back to deny. Under `apx acp`, the request goes to the host.
- `auto-approve`: Approves tool permission requests for that account without asking anyone, the host included. This is the default when `permission_mode` is not set.

`apx acp` hosts (T3 Code, Zed and other ACP clients) can set a session mode. Once a host sets one, that mode replaces the account's `permission_mode`:

- Manual: `ask`, so each request goes to the host.
- Accept edits, Auto, Full access: `auto-approve`, so the host is never asked.
- Plan: `deny`.

Until the host sets a mode, and while `[acp] expose_modes` is off (the default), the account's `permission_mode` decides. apx shows no Mode menu of its own by default. To show apx's Mode menu (Manual, Accept edits, Auto, Full access, Plan):

```toml
[acp]
  expose_modes = true
```

With the menu shown, new sessions start in Auto.

## Tool Activity Streaming

In Settings (`7`), toggle ACP Tool Activity:
- `drop` (**default**): Only assistant text chunks are streamed.
- `text`: Formats tool calls and updates inline as plain text (e.g. `[tool: read_file]`).

## Built-in ACP Descriptors

| ID | Name | Detect Binary | Isolation Mechanism |
|---|---|---|---|
| `agy` | Google Antigravity | `agy` | `--app-data-dir antigravity-cli`, `HOME={profile}`, `XDG_CONFIG_HOME={profile}/.config` |
| `gemini` | Gemini CLI | `gemini` | `GEMINI_CLI_HOME={profile}`, `XDG_CONFIG_HOME={profile}/.config` |
| `codex` | OpenAI Codex (ACP) | `codex-acp` | `CODEX_HOME={profile}` |
| `claude` | Claude Code (ACP) | `claude-agent-acp` | `CLAUDE_CONFIG_DIR={profile}` |
| `custom-acp` | Custom ACP Command | *(user supplied)* | Custom command & env with `{profile}` substitution |

> **Terms notice:** Check each provider's terms of service before driving a subscription CLI from third-party clients or across multiple accounts.

## Adding Custom Descriptors (`backends.d/*.yaml`)

Drop a YAML file into `<data-dir>/backends.d/` (on Linux, `~/.config/agentpassage/backends.d/`) to add or override an ACP tool without rebuilding `apx`:

```yaml
id: myagent
name: My ACP Agent
detect: myagent
command: ["myagent", "acp", "--stdio"]
profile_args: ["--data-dir", "{profile}"]
profile_env:
  MYAGENT_HOME: "{profile}"
login: ["myagent", "login", "--data-dir", "{profile}"]
default_models:
  - default
  - myagent-pro
terms_note: "Runs the official myagent CLI over ACP."
```

Every `{profile}` placeholder in `command`, `profile_args`, `profile_env`, and `login` is expanded to the account's isolated profile directory.

## Skills

`apx acp` lists the skills it finds to the host: the project's `.agents/skills` and `.claude/skills`, then the same folders in your home. A project skill wins over a user skill with the same name.

Accounts run in isolated profiles that hide your agent's own folders, so apx links your skills into each profile before a request. A skill you add to an agent reaches every account of that agent:

| Agent | Skills linked from |
|---|---|
| Claude | `~/.claude/skills`, `~/.agents/skills` |
| Codex | `~/.codex/skills` (Codex reads `~/.agents/skills` itself) |
| Antigravity | `~/.gemini/config/skills`, `~/.agents/skills` |
| Grok | `~/.grok/skills`, `~/.agents/skills` |
| Cursor | `~/.cursor/skills`, `~/.agents/skills` |
| Pi | `~/.pi/agent/skills` (Pi reads `~/.agents/skills` itself) |
| OpenCode | `~/.config/opencode/skills` (OpenCode reads `~/.agents/skills` itself) |
| Gemini | `~/.gemini/skills` (Gemini reads `~/.agents/skills` itself) |
| Cline | `~/.cline/skills`, `~/.agents/skills` |
| Muse Code | `~/.config/muse/skills`, `~/.claude/skills`, `~/.codex/skills`, `~/.agents/skills` |
| Any other | `~/.config/<agent>/skills` and `~/.config/agents/skills` (`<agent>` is the backend ID without `-acp`) |

CLI-backend accounts get the same skills.

The agent's own folder wins over `~/.agents/skills` for a shared name. apx never replaces an entry already in a profile, and removes only its own links to skills you deleted. It doesn't install or update skills; use your skill manager for that.

## MCP servers

`apx acp` sessions also get the MCP servers from your own agent config, read when the session starts:

| Agent | Read from |
|---|---|
| Claude | `~/.claude.json` (user servers and the project's local servers) |
| Codex | `~/.codex/config.toml` |
| Antigravity | `~/.gemini/config/mcp_config.json` |
| Grok | `~/.grok/config.toml` |
| Cursor | `~/.cursor/mcp.json` |
| Gemini | `~/.gemini/settings.json` |
| OpenCode | `~/.config/opencode/opencode.json` and `opencode.jsonc` |
| Pi | `~/.pi/agent/mcp.json`, linked into the account's profile (its adapter ignores servers sent over ACP) |
| Cline | `~/.cline/data/settings/cline_mcp_settings.json` |
| Muse Code | `~/.config/muse/settings.json` |
| Kilo | `~/.config/kilo/` (`kilo.json`, `opencode.json` and their `.jsonc` forms) |
| Goose | `~/.config/goose/config.yaml` (command and streamable HTTP extensions) |
| Amp | `~/.config/amp/settings.json` and `settings.jsonc` |

Other agents get no servers from apx. Those that apx isolates only through `XDG_CONFIG_HOME` still read files in your home folder, but not ones under `~/.config`.

apx sends these servers over ACP; whether an agent uses them is up to the agent. This is tested live with OpenCode, but not with Cline, Muse Code, Kilo, Goose, Amp, Cursor or Gemini.

CLI-backend accounts used through `apx acp` get the servers for one run at a time:

| CLI backend | How |
|---|---|
| `claude` | `--mcp-config` with a temporary file |
| `codex` | `-p` with a temporary profile file |
| `opencode` | `OPENCODE_CONFIG_CONTENT` |

The `agy`, `grok`, `cursor` and `gemini` CLI backends get none: apx would have to write the servers into the profile, where API requests would load them too. Use an ACP account for these. An account with a custom command gets none either. The CLI's own permission rules still apply to MCP tools.

A server the host sends wins over one with the same name. Disabled servers are skipped, as are HTTP and SSE servers when the agent doesn't accept them. Servers that need an OAuth sign-in stored by the agent aren't covered. Each server starts with your real home folder, so it keeps its own settings and sign-ins.

HTTP API requests never get these servers, because anyone with an API key could then use them. For Pi, API requests start it with `--no-mcp`. That switch covers Pi's built-in MCP support, not an extension that replaces it. To turn this off for `apx acp` too:

```toml
[acp]
disable_host_mcp = true
```

Accounts with `use_host_profile = true` read your config directly, so apx adds nothing to them.
