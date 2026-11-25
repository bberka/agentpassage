# Privacy Filter & Model Fallback Chains

## Reversible Privacy Filter

When enabled, AgentPassage detects sensitive values in outgoing requests, replaces each unique value with a deterministic placeholder (`{{APX_KEY_1}}`, `{{APX_EMAIL_1}}`, `{{APX_PHONE_1}}`, or `{{APX_CUSTOM_1}}`), forwards only the redacted request upstream, and restores the original values in both non-streaming and streaming responses.

Because placeholders are reversible in memory per request (not hashed), the upstream model can reference `{{APX_EMAIL_1}}` in its answer and the client still receives the exact original value.

### What is detected

- **API keys & tokens (`redact_api_keys`):** OpenAI/Anthropic/Stripe `sk-...`, GitHub `ghp_`/`github_pat_`, GitLab `glpat-`, Slack `xox...`, AWS `AKIA...`, Google `AIza...`, AgentPassage `apx_`/`acpx_`, Groq/HF keys, JWTs, and `api_key=`/`access_token=` assignments.
- **Email addresses (`redact_emails`)**
- **Phone numbers (`redact_phones`):** Formatted 10–15 digit US/international numbers (ISO dates like `2026-11-25` are ignored).
- **Custom regex patterns (`custom_patterns`):** User-defined `name = "pattern"` entries.

### Inspecting redactions

- Responses include `X-Apx-Redacted: api_key=1,email=1` when one or more items were redacted.
- Local logs record only category counts, never raw secret values.
- Test locally without calling any upstream provider:

```sh
apx privacy enable
apx privacy config --api-keys true --emails true --phones true --add-pattern acct="ACCT-[0-9]{6}"
apx privacy test --json "Send sk-proj-1234567890abcdef to dev@acme.io for ACCT-123456"
```

### Configuration (`config.toml`)

```toml
[privacy]
enabled = true
redact_api_keys = true
redact_emails = true
redact_phones = true

[[privacy.custom_patterns]]
name = "acct"
pattern = "ACCT-[0-9]{6}"
```

## Model Fallback Chains

Model fallback is separate from account failover:

1. **Account failover** retries the **same model** across eligible accounts for that model (e.g. `agy/a` → `agy/b`).
2. **Model fallback** runs only after eligible accounts for the primary model are exhausted (or unavailable) and the failure matches a configured safe error kind.

### Safe default triggers (`fallback_on`)

By default, model fallback triggers only on:

- `rate_limit` (HTTP 429 / quota exhausted)
- `transient` (HTTP 5xx / network error)
- `unavailable` (all accounts for the primary model are in cooldown, busy, or disabled)

It does **not** fall back on `auth` (401/403), `restricted`, or `client` (400) errors unless explicitly added to `fallback_on`, and it **never** retries once streaming bytes have already been sent to the client.

When a fallback model handles a request, AgentPassage sets:

- `X-Apx-Model: <actual-model>`
- `X-Apx-Fallback-From: <requested-model>`

### Configure via CLI or TUI

```sh
apx routing add "agy/*" \
  --strategy failover \
  --retry-429 true \
  --fallback "codex/gpt-5,deepseek/deepseek-chat" \
  --fallback-on "rate_limit,transient,unavailable"
```

In the TUI (`4 Routing`), select a rule and press `b` to edit fallback models or `p` to set a project scope.
