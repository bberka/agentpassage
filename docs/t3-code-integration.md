# T3 Code integration

What T3 Code reads from apx. These were found by reading the installed T3 Code nightly app in November 2026, so check them again after a T3 Code update. When you change one, assert the field in a test.

## Models and chat (`apx acp`)

T3 Code runs `apx acp` through its ACP registry provider.

- **Model names:** the `name` of each model option. apx builds them in `internal/acpbridge/names.go` and the router.
- **Thinking effort:** a config option with category `thought_level`.
- **Mode menu:** hidden unless `[acp] expose_modes = true`. With no mode from the host, apx follows the account's `permission_mode` for permission requests and sends the agent no mode, so each agent uses its own default. Claude's default is `auto`, whose safety check can deny an action without asking the host.
- **Skills and slash commands:** T3 Code reads them only from the `available_commands_update` session update. A command whose name starts with `$` is a skill. apx doesn't send this update yet, so T3 Code lists no skills for apx.
- **Updates:** a running `apx acp` process keeps its binary until T3 Code restarts it.

## Usage limits (CLIProxyAPI compatibility)

T3 Code's Limits view reads apx as a CLIProxyAPI server through `/v0/management/*`, which needs an admin key. Handlers are in `internal/gateway/cliproxy_compat.go`.

| Account | Provider name apx sends | What T3 Code reads |
|---|---|---|
| Codex | `codex` | the `wham/usage` reply |
| Claude | `claude` | the `oauth/usage` reply; per-model limits come from `limits` entries with `kind: "weekly_scoped"` |
| AGY | `antigravity` | `retrieveUserQuotaSummary` groups with 5h and weekly buckets |
| Grok | `xai` | the `v1/billing?format=credits` reply |

T3 Code has no usage reader for other providers, so DeepSeek balances show only in apx.
