# Quickstart

## 1. Install and start the background service

```sh
make install
apx service install --auto-start
apx status
```

## 2. Add accounts

Open the interactive TUI with `apx`, or add accounts via CLI:

```sh
# ACP subscription agent (isolated profile)
apx add --kind acp --backend codex --name main

# HTTP API-key provider
apx add --kind api_key --backend deepseek --name main --api-key "$DEEPSEEK_API_KEY"
```

Verify connectivity:

```sh
apx health
apx models list
```

## 3. Connect an IDE or CLI

`apx connect` previews the exact file change and asks for confirmation before editing any config:

```sh
apx connect t3code
apx connect zed
apx connect delta
apx connect opencode
```

To print shell environment variables instead:

```sh
eval "$(apx export-env)"
```

## 4. Configure routing, fallbacks, and privacy (optional)

- Default routing is **primary-first failover** (uses Account 1 fully, switches to Account 2 only on quota/429 or outage).
- Add a model fallback chain:
  ```sh
  apx routing add "agy/*" --strategy failover --fallback "codex/gpt-5,deepseek/deepseek-chat"
  ```
- Add project-scoped routing + dedicated project API key:
  ```sh
  apx routing add "deepseek/*" --project work --accounts deepseek-work --strategy round-robin
  apx keys create work-client --role client --project work
  ```
- Enable the reversible privacy filter:
  ```sh
  apx privacy enable
  ```

## 5. Inspect usage and quotas

```sh
apx usage --period 24h --refresh
```

Or press `6` in the TUI and `o` to open the local Web Usage Report Hub (`http://127.0.0.1:8787/usage`).
