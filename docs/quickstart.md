# Quickstart Guide

This guide walks through running `acpx`, adding your first API-key or ACP account, and sending requests from any OpenAI or Anthropic client.

## 1. Launch `acpx`

```sh
acpx
```

This does two things immediately:
1. Starts the local HTTP gateway on `http://127.0.0.1:8787/v1` with an auto-generated local API key (`acpx_...`).
2. Opens the interactive TUI.

On first run, the **Welcome & Detect** modal scans your `PATH` for supported ACP CLIs (`agy`, `gemini`, `codex-acp`, `claude-agent-acp`).
- Press `Space` to toggle detected tools and `Enter` to add them.
- Or press `a` to add an API-key account immediately.
- Or press `s` to skip to the Dashboard.

## 2. Add an API-Key Account (Example: DeepSeek)

### Inside the TUI
1. Press `a` from the **Dashboard** (`1`) or **Accounts** (`2`) screen.
2. Select **1. API Key provider**, then choose **DeepSeek** (or OpenRouter, OpenAI, Anthropic, Ollama, Custom).
3. Enter an account name (e.g. `personal`), paste your API key, and press `Enter` / `Ctrl+S`.
4. `acpx` stores the key in your OS keychain (or a `0600` fallback file) and runs a live connection test to fetch available models.

### Or via CLI (headless / scripts)
```sh
acpx add \
  --kind api_key \
  --backend deepseek \
  --name personal \
  --key "sk-your-deepseek-key"
```

For local Ollama (no API key needed):
```sh
acpx add --kind api_key --backend ollama --name local
```

## 3. Add an ACP Subscription Account (Example: `agy`)

### Inside the TUI
1. Press `a` -> choose **2. Subscription tool over ACP** -> choose **Google Antigravity**.
2. Enter an account name (e.g. `work`).
3. `acpx` creates an isolated profile directory (`profiles/agy/work/`) and launches `agy acp --login` inside your terminal.
4. Complete login, then press `t` on the Accounts screen to test the ACP session and discover models.

You can add a second account named `personal` the same way — each account gets its own profile directory and can be targeted directly via `agy/work/<model>` vs `agy/personal/<model>`.

### Or via CLI
```sh
acpx add --kind acp --backend agy --name work
```

## 4. Point Clients at `acpx`

### Export environment variables
```sh
# bash / zsh
eval "$(acpx export-env)"

# fish
acpx export-env --shell fish | source

# PowerShell
acpx export-env --shell powershell | Invoke-Expression
```

This sets `OPENAI_BASE_URL`, `OPENAI_API_KEY`, `ANTHROPIC_BASE_URL`, and `ANTHROPIC_API_KEY`.

### Test with `curl` (OpenAI Chat Completions)

Non-streaming:
```sh
curl -s "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek/deepseek-chat",
    "messages": [{"role": "user", "content": "Say hello in one sentence."}]
  }'
```

Streaming over ACP with session reuse:
```sh
curl -N "$OPENAI_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Acpx-Session: my-thread-1" \
  -H "X-Acpx-Cwd: /path/to/repo" \
  -d '{
    "model": "agy/work/gemini-3.1-pro",
    "stream": true,
    "messages": [{"role": "user", "content": "Summarize this repository."}]
  }'
```

### Test with Anthropic-compatible clients (`/v1/messages`)

```sh
curl -s "$ANTHROPIC_BASE_URL/v1/messages" \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek/deepseek-chat",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## 5. Running Headlessly (Background / Systemd / SSH)

Run without the TUI:
```sh
acpx serve
# or
acpx --no-tui --port 8787
```

Check live state anytime:
```sh
curl -s http://127.0.0.1:8787/healthz
curl -s http://127.0.0.1:8787/admin/state -H "Authorization: Bearer $OPENAI_API_KEY"
```
