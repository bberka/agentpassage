# CLI & Agent Automation Guide

Every AgentPassage feature is available from the CLI with `--json` output and automatic hot-reload of the running `apx` background service. An Agent Skill is bundled at [skills/agentpassage/SKILL.md](../skills/agentpassage/SKILL.md), and `apx agent-guide` prints a compact reference directly to stdout.

## Status & Diagnostics

```sh
apx status --json
apx health --json
apx doctor
```

## Accounts & Models

```sh
apx accounts list --json
apx add --kind acp --backend codex --name work
apx add --kind api_key --backend deepseek --name main --api-key "$DEEPSEEK_API_KEY"
apx accounts enable deepseek/main
apx accounts disable deepseek/main
apx accounts edit agy/bb --expose pool --hourly-limit 50 --weekly-limit 500
apx accounts remove deepseek/main --yes

apx models list --json
apx models fetch deepseek/main
apx models enable deepseek/main all
apx models disable deepseek/main deepseek-reasoner
```

## Routing, Project Scoping & Model Fallback

```sh
apx routing list --json
apx routing add "agy/*" --strategy failover --retry-429 true --fallback "codex/gpt-5,deepseek/deepseek-chat"
apx routing add "deepseek/*" --project work --accounts deepseek-work --strategy round-robin
apx routing remove "deepseek/*" --project work
```

`round-robin` and `least-busy` work directly on API-key rules. If a rule matches subscription (`acp`/`cli`) accounts, the CLI refuses unless `--accept-sub-risk` is explicitly passed.

## Privacy Filter

```sh
apx privacy status --json
apx privacy enable
apx privacy disable
apx privacy config --api-keys true --emails true --phones true --add-pattern ticket="PROJ-[0-9]+"
apx privacy test --json "Contact alice@example.com with sk-proj-1234567890abcdef"
```

## Scoped API Keys (Client, Admin & Project)

```sh
apx keys list --json
apx keys create work-ci --role client --project work --models "deepseek/*,openai/*" --json
apx keys disable work-ci
apx keys enable work-ci
apx keys revoke work-ci
```

Keys pinned with `--project <name>` automatically bind all requests made with that key to that project for both routing and usage reporting. Unpinned keys and the primary admin key can pass `X-Apx-Project: <name>` per request.

## Usage, Costs & Quotas

```sh
apx usage --period 1h --json
apx usage --period 24h --refresh
apx usage --period 7d --json
apx usage --period 30d --json
```

Cost fields (`estimated_cost_usd`) are always labeled as reference-rate estimates (`cost_estimated: true`).

## Embeddings & Reranking

```sh
apx embed --model text-embedding-3-small --project search "AgentPassage local gateway"
apx rerank --model rerank-v3 --query "local proxy" --top-n 2 "unrelated text" "local OpenAI gateway"
```

## IDE Connections & Background Service

```sh
apx connect t3code --yes
apx connect zed --yes
apx disconnect t3code --yes
apx uninstall --yes
apx service status
apx service restart
```
