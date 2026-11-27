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

## Sessions & Working Directory

- **Session reuse:** Pass `X-Apx-Session: <id>` (or set the OpenAI `user` / Anthropic `metadata.user_id` field). Subsequent requests with the same key reuse both the sticky account and the live ACP session.
- **No session hint:** Each request opens a fresh ACP session.
- **Working directory:** Pass `X-Apx-Cwd: /path/to/dir` per request, or configure a default working directory per account on the **Accounts** screen (`e`).

## Permission Handling

When an ACP agent calls `session/request_permission`, `apx` applies the account's `permission_mode` (press `p` on the Accounts screen to cycle):

- `deny` (**default**): Rejects tool permission requests automatically.
- `ask`: Opens an interactive approval modal in the TUI (`[y] Approve once / [n] Deny`). In headless mode (`--no-tui`), falls back to deny.
- `auto-approve`: Automatically approves tool permission requests for that account.

Through `apx acp` (T3 Code, Zed and other ACP hosts), the host decides instead. apx forwards each permission request to the host, so the host's own setting applies (for example T3 Code's approval or full-access mode). apx doesn't show a Mode menu of its own. If the host sets a mode, apx uses it. To show apx's Mode menu (Manual, Accept edits, Auto, Full access, Plan) again:

```toml
[acp]
  expose_modes = true
```

With the menu shown, new sessions start in Auto.

## Tool Activity Streaming

Under **Settings** (`6`), toggle **ACP Tool Activity**:
- `drop` (**default**): Only assistant text chunks are streamed.
- `text`: Formats tool calls and updates inline as plain text (e.g. `[tool: read_file]`).

## Built-in ACP Descriptors

| ID | Name | Detect Binary | Isolation Mechanism |
|---|---|---|---|
| `agy` | Google Antigravity | `agy` | `--app-data-dir {profile} --gemini-dir {profile}/.gemini` |
| `gemini` | Gemini CLI | `gemini` | `GEMINI_CLI_HOME={profile}`, `XDG_CONFIG_HOME={profile}/.config` |
| `codex` | OpenAI Codex (ACP) | `codex-acp` | `CODEX_HOME={profile}` |
| `claude` | Claude Code (ACP) | `claude-agent-acp` | `CLAUDE_CONFIG_DIR={profile}` |
| `custom-acp` | Custom ACP Command | *(user supplied)* | Custom command & env with `{profile}` substitution |

> **Terms notice:** Check each provider's terms of service before driving a subscription CLI from third-party clients or across multiple accounts.

## Adding Custom Descriptors (`backends.d/*.yaml`)

Drop a YAML file into `<data-dir>/backends.d/` (default `~/.config/apx/backends.d/`) to add or override an ACP tool without rebuilding `apx`:

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
