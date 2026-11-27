# Routing, account pools & security

## Model namespace

Every enabled account contributes models to one merged namespace served at `GET /v1/models`:

- **Pooled model:** `<backend>/<model>` (e.g. `agy/gemini-3.1-pro`, `deepseek/deepseek-chat`). It is routed across matching accounts using the active routing rule.
- **Manual account targeting:** `<backend>/<account>/<model>` (e.g. `agy/work/gemini-3.1-pro`, `agy/personal/gemini-3.1-pro`). It always routes to that exact account.
- **Aliases & overrides:** On the **Models** screen (`3`), press `a` to assign short aliases (e.g. `pro` for `agy/work/gemini-3.1-pro`), `r` to rename a model (the NAME column shows what clients see; save a blank name to restore the automatic one), or `h` to hide models from `/v1/models`.

## Routing strategies

Configured per model pattern (`agy/*`, `deepseek/deepseek-chat`, or `*`) on the **Routing** screen (`4`):

| Strategy | Behavior | Subscription Accounts |
|---|---|---|
| `failover` **(default)** | Uses the primary account fully and switches to the next account only when the active one hits quota/429, cooldown, concurrency cap, or outage. | Recommended default |
| `sticky` | Keeps a session (`X-Apx-Session` or `user`) pinned to one account; new sessions start on the primary account (never alternating/rotating). | Enabled by default |
| `manual` | Routes to the exact account named in `<backend>/<account>/<model>` or `<prefix><model>`. | Enabled by default |
| `round-robin` | Rotates new threads/requests across eligible accounts. Intended for API-key pools. | **Blocked unless explicitly opted in** (ban-risk confirmation required) |
| `least-busy` | Picks the eligible account with the fewest in-flight requests. Intended for API-key pools. | **Blocked unless explicitly opted in** (ban-risk confirmation required) |

### Why `round-robin` and `least-busy` require explicit confirmation

Spreading or alternating traffic across multiple subscription (ACP/CLI) accounts can violate provider Terms of Service and cause account bans. Both strategies work freely for API-key pools, while `apx` prompts with a ban-risk warning in the TUI and blocks subscription accounts at runtime unless `subscription_warning_acked = true` is explicitly confirmed on that rule. For subscriptions, stick with the default primary-first `failover`.

## Project-based routing

As a local proxy, `apx` routes by project using an explicit identifier supplied by the client:

1. **Project-scoped API key (recommended for isolation):**
   ```sh
   apx keys create work-bot --role client --project work
   ```
   Every request using that key is automatically pinned to `project = "work"` for both routing and usage reporting (the key cannot spoof another project via header).
2. **Per-request header (for admin or unscoped keys):**
   Pass `X-Apx-Project: work` (or legacy `X-Acpx-Project`).

Configure project-scoped routing rules with `apx routing add <pattern> --project <name>` or `p` in the TUI (`4 Routing`). Matching project rules always take priority over global rules.

## Health & protection rules

Applied automatically across all strategies:

- **HTTP 429 / quota / rate limit:** Places that account into cooldown (respecting `Retry-After` headers and parsed reset durations like `"Resets in 48m24s"` up to 6h, or exponential backoff from 60s up to 10m). By default, `failover` switches to the next eligible account (`allow_cross_account_retry_on_429 = true`, toggleable with `4` in Routing).
- **Live quota:** Every strategy skips an account whose quota for the requested model is used up. Only the windows that meter that model count: agy uses its Gemini or Claude & GPT group (Gemini ranks by weekly, but a used-up 5h window still blocks it), and Claude adds the Fable/Opus/Sonnet weekly limit only for that model. If every account looks used up, apx tries them anyway, since quota data can be a few minutes old.
- **Conversations keep their account while it has quota:** Requests that share a session key (`X-Apx-Session`, the OpenAI `user` field, or the ACP session) prefer the account they last succeeded on. When that account runs out of quota or is cooling down after a 429, apx switches the conversation to another eligible account by default. To keep the conversation on the same account and return 429 until quota resets instead:
  ```toml
  [sessions]
    wait_for_reset_when_exhausted = true
  ```
  New conversations always go to an account with quota left.
- **Repeated 401 / 403:** First auth error marks the account `login_needed`. Consecutive auth errors auto-disable the account and display a red health banner.
- **Account restricted / suspended:** Immediately auto-disables the account and displays a critical banner advising you to stop using it.
- **Concurrency caps:** Default `1` concurrent request per subscription (ACP) account and `8` per API-key account.
- **Budgets:** Optional `hourly_limit` and `daily_limit` per account in `config.toml`.
- **Low-quota alerts:** apx sends a desktop notification or webhook when any limit crosses `quota_threshold_pct`, if alerts are enabled. That covers account-wide limits and per-model ones (agy Gemini and Claude & GPT, Claude Fable). Each limit alerts once per period:
  ```toml
  [alerts]
    enabled = true
    desktop = true
    quota_threshold_pct = 90
  ```
- **Low-balance alerts:** Set `balance_threshold`, and apx sends one when a prepaid balance drops below that amount, in the balance's own currency. It works for DeepSeek and for OpenRouter keys that have a spending limit, with or without `quota_threshold_pct`. An account with several currencies is low only when all of them are below the threshold, so an unused `0.00 CNY` entry next to a funded USD balance stays quiet. `reminder_hours` repeats the alert while the balance stays low (`0` alerts once). Once the balance is back at or above the threshold, the next drop alerts again:
  ```toml
  [alerts]
    enabled = true
    desktop = true
    balance_threshold = 5.0
    reminder_hours = 24
  ```
  Balances are checked every 5 minutes, and config changes apply at the next check. Sent alerts are remembered only while apx runs, so a restart can repeat one. Failed desktop or webhook deliveries are logged as warnings.

## Security Model

- **Localhost only by default:** Binds to `127.0.0.1:8787`. Binding to a non-loopback address is refused unless `--allow-remote` is passed, which logs an explicit warning.
- **Local API key:** Every `/v1/*` and `/admin/*` endpoint requires the local key via `Authorization: Bearer <key>` or `x-api-key: <key>`. Rotate it anytime from **Settings** (`g`).
- **Secret storage:** API keys are stored in the OS keychain (Secret Service on Linux, Keychain on macOS, Credential Manager on Windows). If no keychain is available, `apx` falls back to `<data-dir>/secrets.json` with `0600` permissions and shows a warning banner in the TUI and `apx doctor`.
- **No secrets in logs:** Known API keys, bearer tokens, and local keys are redacted before entering the in-memory log buffer or `logs/apx.log`.
- **Clean deletion:** Removing an ACP account in the TUI asks for confirmation and permanently deletes its isolated `profiles/<backend>/<account>/` directory.
