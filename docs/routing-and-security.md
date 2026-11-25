# Routing, Account Pools & Security

## Model Namespace

Every enabled account contributes models to one merged namespace served at `GET /v1/models`:

- **Pooled model:** `<backend>/<model>` (e.g. `agy/gemini-3.1-pro`, `deepseek/deepseek-chat`) — routed across matching accounts using the active routing rule.
- **Manual account targeting:** `<backend>/<account>/<model>` (e.g. `agy/work/gemini-3.1-pro`, `agy/personal/gemini-3.1-pro`) — always routes to that exact account.
- **Aliases & overrides:** On the **Models** screen (`3`), press `a` to assign short aliases (e.g. `pro` -> `agy/work/gemini-3.1-pro`), `r` to rename display names, or `h` to hide models from `/v1/models`.

## Routing Strategies

Configured per model pattern (`agy/*`, `deepseek/deepseek-chat`, or `*`) on the **Routing** screen (`4`):

| Strategy | Behavior | Subscription Accounts |
|---|---|---|
| `sticky` **(default)** | Keeps a session (`X-Acpx-Session` or `user`) pinned to one account; new sessions go to the least-recently-used eligible account. | Enabled by default |
| `manual` | Routes to the account named in `<backend>/<account>/<model>`. | Enabled by default |
| `failover` | Uses the primary account and falls over to the next account **only** on hard failures (network errors or HTTP 5xx) — never on 429, 401, or 403. | Enabled by default |
| `round-robin` | Rotates requests across eligible accounts. | **Opt-in only** (TUI warning required) |
| `least-busy` | Picks the eligible account with the fewest in-flight requests. | **Opt-in only** (TUI warning required) |

### Why `round-robin` and `least-busy` Require Opt-In

Spreading load across multiple subscription accounts can violate provider terms of service and result in account restrictions. `acpx` blocks both strategies for rules containing ACP subscription accounts until you explicitly acknowledge the warning in the TUI (persisted as `subscription_warning_acked = true` on that rule).

## Health & Protection Rules

Applied automatically across all strategies:

- **HTTP 429 / quota / rate limit:** Places that account into cooldown (respecting `Retry-After`, or exponential backoff from 60s up to 10m). Requests are **not** retried on another subscription account unless `allow_cross_account_retry_on_429` is explicitly toggled on (`4` in Routing).
- **Repeated 401 / 403:** First auth error marks the account `login_needed`. Consecutive auth errors auto-disable the account and display a red health banner.
- **Account restricted / suspended:** Immediately auto-disables the account and displays a critical banner advising you to stop using it.
- **Concurrency caps:** Default `1` concurrent request per subscription (ACP) account and `8` per API-key account.
- **Budgets:** Optional `hourly_limit` and `daily_limit` per account in `config.toml`.

## Security Model

- **Localhost only by default:** Binds to `127.0.0.1:8787`. Binding to a non-loopback address is refused unless `--allow-remote` is passed, which logs an explicit warning.
- **Local API key:** Every `/v1/*` and `/admin/*` endpoint requires the local key via `Authorization: Bearer <key>` or `x-api-key: <key>`. Rotate it anytime from **Settings** (`g`).
- **Secret storage:** API keys are stored in the OS keychain (Secret Service on Linux, Keychain on macOS, Credential Manager on Windows). If no keychain is available, `acpx` falls back to `<data-dir>/secrets.json` with `0600` permissions and shows a warning banner in the TUI and `acpx doctor`.
- **No secrets in logs:** Known API keys, bearer tokens, and local keys are redacted before entering the in-memory log buffer or `logs/acpx.log`.
- **Clean deletion:** Removing an ACP account in the TUI asks for confirmation and permanently deletes its isolated `profiles/<backend>/<account>/` directory.
