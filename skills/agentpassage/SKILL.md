---
name: agentpassage
description: Manage AgentPassage (apx) accounts, models, routing rules, project routing, model fallbacks, reversible privacy redaction, scoped API keys, usage/quotas, embeddings/rerank, and IDE connections via CLI.
---

# AgentPassage (`apx`) CLI Automation

Use `apx` non-interactively with `--json` for inspection and `--yes` to skip confirmation prompts when the user has already asked for the change. Every config mutation automatically hot-reloads the running `apx` background service.

## Inspect state first

```sh
apx status --json
apx accounts list --json
apx routing list --json
apx privacy status --json
apx usage --period 24h --json
apx health --json
```

## Common operations

- **Accounts:**
  - `apx add --kind acp --backend agy --name work`
  - `apx add --kind api_key --backend deepseek --name main --api-key <key>`
  - `apx accounts enable|disable <id|name>`
  - `apx accounts edit <id|name> --expose pool|prefix|both --prefix p- --hourly-limit 50 --weekly-limit 500`
  - `apx accounts remove <id|name> --yes`
- **Models:**
  - `apx models list --json`
  - `apx models fetch <account>`
  - `apx models enable|disable <account> <model|all>`
- **Routing, project scoping, and model fallback:**
  - Default without a rule is primary-first `failover` with `429` failover enabled.
  - Never pass `--accept-sub-risk` for `round-robin` or `least-busy` on subscription (`acp`/`cli`) accounts unless the user explicitly accepts the ban risk.
  - `apx routing add "agy/*" --strategy failover --fallback "codex/gpt-5,deepseek/deepseek-chat"`
  - `apx routing add "deepseek/*" --project work --accounts ds-work --strategy round-robin`
  - `apx routing remove "deepseek/*" --project work`
- **Reversible privacy filter:**
  - `apx privacy enable` / `apx privacy disable`
  - `apx privacy config --api-keys true --emails true --phones true --add-pattern ticket="PROJ-[0-9]+"`
  - `apx privacy test --json "sample text with alice@example.com"`
- **Scoped API keys:**
  - `apx keys create work-bot --role client --project work --models "deepseek/*" --json`
  - `apx keys list --json`
  - `apx keys disable|enable|revoke work-bot`
- **Embeddings & reranking:**
  - `apx embed --model text-embedding-3-small --project search "text"`
  - `apx rerank --model rerank-v3 --query "query" --top-n 3 "doc1" "doc2"`
- **IDE connections & service:**
  - `apx connect <client> --yes` / `apx disconnect <client|all> --yes`
  - `apx service status|start|stop|restart|install|uninstall`
