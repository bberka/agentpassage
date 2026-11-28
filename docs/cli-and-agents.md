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
apx routing explain agy/gemini-3.8-flash                # which account a new conversation would get, and why
apx routing explain claude/claude-fable-5-2 --session <id>   # the same for an existing conversation
```

`apx routing explain` uses the running service's live quota and account health and sends no request, so it costs no quota. `--json` prints the full result.

`round-robin` and `least-busy` work directly on API-key rules. If a rule matches subscription (`acp`/`cli`) accounts, the CLI refuses unless `--accept-sub-risk` is explicitly passed.

## Privacy Filter

```sh
apx privacy status --json
apx privacy enable
apx privacy disable
apx privacy config --api-keys true --emails true --phones true --add-pattern ticket="PROJ-[0-9]+"
apx privacy test --json "Contact alice@example.com with sk-proj-1234567890abcdef"
```

## Scoped API keys (client, admin & project)

```sh
apx limits list --json
apx limits set --every 1d --weekly 20    # default for every account
apx limits set claude/work --every 3d --weekly 40
apx limits allow claude/work            # one more allowance for this period
apx tools list --json
apx tools off pr --skill        # skills are on by default
apx tools on browse             # new MCP servers are off by default
apx keys list --json
apx keys create work-ci --role client --project work --models "deepseek/*,openai/*" --json
apx keys agents work-ci on      # let it run ACP and CLI accounts, which act on this machine
apx keys disable work-ci
apx keys enable work-ci
apx keys revoke work-ci
```

Keys pinned with `--project <name>` automatically bind all requests made with that key to that project for both routing and usage reporting. Unpinned keys and the primary admin key can pass `X-Apx-Project: <name>` per request.

## Usage, costs & quotas

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

## IDE connections & background service

```sh
apx connect t3code --yes
apx connect zed --yes
apx connect delta
apx disconnect t3code --yes
apx uninstall --yes
apx service status
apx service restart
```

## Cost estimates

`apx usage`, the Usage tab and the Usage Hub price each request at the provider's public list price, using the table in `internal/usage/pricing.go`. That includes cached input, long-prompt tiers, announced price changes and DeepSeek's peak hours. A subscription account isn't billed per token, so its figure is what the same tokens would cost through the API.

- A model that isn't in the table is counted as unpriced and left out of the total.
- `apx acp` turns use the token counts the agent reports. When it reports none, apx estimates them from the newest message and the reply, which leaves out the agent's own context and so reads low.
- Anthropic cache writes are priced at the 5-minute rate.
